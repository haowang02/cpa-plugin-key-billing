package plugin

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

// maxRetryAfterSeconds caps the Retry-After hint. A monthly plan can be weeks
// from resetting, and a client that sleeps literally for that long is worse off
// than one that retries hourly and gets another 429.
const maxRetryAfterSeconds = 3600

// Enforcement runs before auth so an over-quota request never occupies an
// upstream credential.
func (a *App) interceptBeforeAuth(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析请求拦截参数：%w", errUnmarshal)
	}
	if a == nil || a.store == nil || !a.store.Enabled() {
		return OKEnvelope(RequestInterceptResponse{})
	}
	if metadataString(req.Metadata, MetadataSource) == SourcePluginHostModelCallback {
		// A nested helper model is an implementation detail of another plugin and
		// need not be among the models granted to the client. Its reported usage is
		// still billed by usage.handle when the host attributes it to the client.
		return OKEnvelope(RequestInterceptResponse{})
	}
	scope := metadataString(req.Metadata, MetadataCallerScope)
	if scope == "" {
		return OKEnvelope(RequestInterceptResponse{})
	}

	// Derive Retry-After from the same instant used for the quota decision.
	now := a.store.Now()
	endpoint := metadataString(req.Metadata, MetadataRequestPath)

	// A model the key may not call is refused ahead of the quota check. The
	// refusal is permanent rather than temporal, so reporting it as an exhausted
	// budget would send the client back to retry; it also must not open a
	// subscription period that the request was never admitted into.
	if access := a.store.AuthorizeModel(scope, req.Model, req.RequestedModel); !access.Allowed {
		a.store.ReportModelBlock(scope, endpoint, access)
		return OKEnvelope(modelForbiddenResponse(req.SourceFormat, access))
	}

	generate := true
	if value, ok := req.Metadata[MetadataGenerate].(bool); ok {
		generate = value
	}
	slot := billing.SlotDecision{Allowed: true}
	admitted := false
	if generate {
		slot = a.store.AcquireSlot(scope, req.RequestID)
		if !slot.Allowed {
			return OKEnvelope(concurrencyLimitResponse(req.SourceFormat, slot))
		}
		defer func() {
			// A panic or a later admission refusal must not leak the slot. Once
			// admitted, request.complete owns the release path.
			if slot.Acquired && !admitted {
				a.store.ReleaseSlot(req.RequestID)
			}
		}()
	}

	decision := a.store.Authorize(scope, now)
	if !decision.Allowed {
		a.store.ReportQuotaBlock(scope, endpoint, decision)
		return OKEnvelope(quotaExhaustedResponse(req.SourceFormat, decision, now))
	}

	admitted = true
	return OKEnvelope(RequestInterceptResponse{})
}

func (a *App) completeRequest(raw []byte) ([]byte, error) {
	var completion struct {
		RequestID string `json:"RequestID"`
	}
	if errUnmarshal := json.Unmarshal(raw, &completion); errUnmarshal != nil {
		return nil, fmt.Errorf("解析请求完成事件：%w", errUnmarshal)
	}
	if a != nil && a.store != nil {
		a.store.ReleaseSlot(completion.RequestID)
	}
	return OKEnvelope(struct{}{})
}

func (a *App) handleUsage(raw []byte) ([]byte, error) {
	var record UsageRecord
	if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
		return nil, fmt.Errorf("解析用量记录：%w", errUnmarshal)
	}
	if a == nil || a.store == nil || !a.store.Enabled() {
		return OKEnvelope(struct{}{})
	}
	scope := billing.CallerScope(record.APIKey)
	if scope == "" || !record.Generate {
		return OKEnvelope(struct{}{})
	}
	if record.Failed {
		model := strings.TrimSpace(record.Alias)
		if model == "" {
			model = record.Model
		}
		a.store.ReportRequestFailure(scope, record.AuthIndex, record.Provider, record.AuthType, record.Source,
			model, usageFailureReason(record.Failure))
		if !usageHasTokens(record.Detail) {
			return OKEnvelope(struct{}{})
		}
	}
	_, _ = billing.EnsureBuiltinCatalog()
	a.store.RecordUsage(billing.UsageEvent{
		Scope:           scope,
		AuthIndex:       record.AuthIndex,
		Provider:        record.Provider,
		ExecutorType:    record.ExecutorType,
		AuthType:        record.AuthType,
		Account:         record.Source,
		ReasoningEffort: record.ReasoningEffort,
		ServiceTier:     record.ServiceTier,
		UpstreamModel:   record.Model,
		RouteModel:      record.Alias,
		RequestedAt:     record.RequestedAt,
		Latency:         record.Latency,
		TTFT:            record.TTFT,
		Failed:          record.Failed,
		Breakdown:       usageBreakdown(record),
		At:              a.store.Now(),
	})
	return OKEnvelope(struct{}{})
}

// metadataString reads a string value from the host's metadata snapshot. The
// host sanitizes metadata into JSON-native types before the RPC hop, so a value
// that survives is either a string or something safely formatted as one.
func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	raw, exists := metadata[key]
	if !exists || raw == nil {
		return ""
	}
	if value, ok := raw.(string); ok {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}

// Use the client's API format so its SDK surfaces an error instead of a parse failure.
func quotaExhaustedResponse(sourceFormat string, decision billing.Decision, now time.Time) RequestInterceptResponse {
	headers := http.Header{
		"Content-Type": []string{"application/json; charset=utf-8"},
	}
	if retryAfter := retryAfterSeconds(decision.ResetAt, now); retryAfter > 0 {
		headers.Set("Retry-After", strconv.Itoa(retryAfter))
	}
	return RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      http.StatusTooManyRequests,
		ResponseHeaders: headers,
		ResponseBody:    refusalBody(sourceFormat, quotaExhaustedError, quotaExhaustedMessage(decision)),
	}
}

func concurrencyLimitResponse(sourceFormat string, decision billing.SlotDecision) RequestInterceptResponse {
	message := fmt.Sprintf("API key concurrency limit reached: %d active requests of %d allowed.",
		decision.Active, decision.Limit)
	return RequestInterceptResponse{
		Terminate:  true,
		StatusCode: http.StatusTooManyRequests,
		ResponseHeaders: http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
			"Retry-After":  []string{"1"},
		},
		ResponseBody: refusalBody(sourceFormat, quotaExhaustedError, message),
	}
}

// A refused model carries no Retry-After: waiting changes nothing, and only an
// operator can.
func modelForbiddenResponse(sourceFormat string, decision billing.ModelDecision) RequestInterceptResponse {
	message := modelForbiddenMessage(decision)
	return RequestInterceptResponse{
		Terminate:  true,
		StatusCode: http.StatusForbidden,
		ResponseHeaders: http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
		},
		ResponseBody: refusalBody(sourceFormat, modelForbiddenError, message),
	}
}

// Refusals are worded in English, the language CLIProxyAPI writes its own errors
// in; the plugin log stays in the panel's language, because an operator reads
// that one and a client SDK reads these.
func quotaExhaustedMessage(decision billing.Decision) string {
	var builder strings.Builder
	builder.WriteString("API key subscription quota exhausted: $")
	builder.WriteString(formatUSD(decision.SpentUSD))
	builder.WriteString(" spent of $")
	builder.WriteString(formatUSD(decision.LimitUSD))
	plan := strings.TrimSpace(decision.PlanName)
	if plan == "" {
		plan = strings.TrimSpace(decision.PlanID)
	}
	if plan != "" {
		builder.WriteString(" on plan ")
		builder.WriteString(strconv.Quote(plan))
	}
	builder.WriteString(".")
	if !decision.ResetAt.IsZero() {
		builder.WriteString(" Quota resets at ")
		builder.WriteString(decision.ResetAt.UTC().Format(time.RFC3339))
		builder.WriteString(".")
	}
	return builder.String()
}

// The refusal names what the key may call instead, so the client can correct the
// request rather than probe for a model that works.
func modelForbiddenMessage(decision billing.ModelDecision) string {
	shown, omitted := decision.Sample()
	message := "API key is not allowed to use model " + strconv.Quote(decision.Model) +
		". Allowed models: " + strings.Join(shown, ", ")
	if omitted > 0 {
		message += fmt.Sprintf(" and %d more", omitted)
	}
	return message + "."
}

// refusal is one reason for turning a request away, spelled the way CLIProxyAPI
// spells an error of that status. The proxy derives both fields from the status
// alone, which is why an exhausted budget reads as a rate limit and a refused
// model as a quota problem: a client branching on type and code must not have to
// know which of the two wrote the body.
type refusal struct {
	anthropicType string
	openaiType    string
	openaiCode    string
}

var (
	quotaExhaustedError = refusal{
		anthropicType: "rate_limit_error",
		openaiType:    "rate_limit_error",
		openaiCode:    "rate_limit_exceeded",
	}
	modelForbiddenError = refusal{
		anthropicType: "permission_error",
		openaiType:    "permission_error",
		openaiCode:    "insufficient_quota",
	}
)

// Anthropic clients use their native envelope; other formats use the
// OpenAI-compatible envelope. Substring matching covers format variants such
// as "claude-code".
func refusalBody(sourceFormat string, kind refusal, message string) []byte {
	normalized := strings.ToLower(strings.TrimSpace(sourceFormat))
	var payload any
	switch {
	case strings.Contains(normalized, "claude") || strings.Contains(normalized, "anthropic"):
		payload = map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    kind.anthropicType,
				"message": message,
			},
		}
	default: // openai, openai-response, codex, gemini, interactions, and anything new.
		payload = map[string]any{
			"error": map[string]any{
				"message": message,
				"type":    kind.openaiType,
				"code":    kind.openaiCode,
			},
		}
	}
	body, _ := json.Marshal(payload)
	return body
}

func retryAfterSeconds(resetAt, now time.Time) int {
	if resetAt.IsZero() {
		return 0
	}
	remaining := resetAt.Sub(now)
	if remaining <= 0 {
		return 1
	}
	seconds := int(math.Ceil(remaining.Seconds()))
	if seconds > maxRetryAfterSeconds {
		return maxRetryAfterSeconds
	}
	return seconds
}

func formatUSD(amount float64) string {
	return strconv.FormatFloat(amount, 'f', 4, 64)
}

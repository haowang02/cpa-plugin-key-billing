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

// An exhausted budget is reported as a rate limit, which is the one refusal
// every client SDK already knows how to wait out.
const QuotaExhaustedStatus = http.StatusTooManyRequests

// A model the key may not call is a permission problem rather than a missing
// one: answering 404 would tell the client the model does not exist, and the
// next thing it does is fall back to another name.
const ModelForbiddenStatus = http.StatusForbidden

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
		// Nested plugin executions may also appear in the outer usage. Prefer a
		// possible undercharge to double-charging a key for work it did not request.
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

	decision := a.store.Authorize(scope, now)
	if !decision.Allowed {
		a.store.ReportQuotaBlock(scope, endpoint, decision)
		return OKEnvelope(quotaExhaustedResponse(req.SourceFormat, decision, now))
	}

	a.store.BeginRequest(req.RequestID, billing.PendingRequest{
		Scope:        scope,
		Endpoint:     endpoint,
		CyclePlanID:  decision.PlanID,
		CycleStartAt: decision.CycleStartAt,
	})
	generate := true
	if value, ok := req.Metadata["generate"].(bool); ok {
		generate = value
	}
	a.usage.begin(req.RequestID, req.SourceFormat, req.Model, req.RequestedModel, generate, a.store.Now())
	return OKEnvelope(RequestInterceptResponse{})
}

// This hook runs per attempt, so its last credential is the one that served a
// retried request.
func (a *App) interceptAfterAuth(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析请求拦截参数：%w", errUnmarshal)
	}
	if a == nil || a.store == nil || !a.store.Enabled() {
		return OKEnvelope(RequestInterceptResponse{})
	}
	a.store.SetRequestCredential(req.RequestID, metadataString(req.Metadata, MetadataSelectedAuthIndex))
	a.usage.addRoute(req.RequestID, req.ToFormat, req.Model, req.RequestedModel, req.Stream, req.Body)
	return OKEnvelope(RequestInterceptResponse{})
}

// Only the pre-translation response has authoritative provider usage. An empty
// result means observe-only and avoids copying every chunk across the ABI again.
func (a *App) normalizeResponseBefore(raw []byte) ([]byte, error) {
	var req ResponseTransformRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析上游响应：%w", errUnmarshal)
	}
	if a.store.Enabled() {
		a.usage.observeUpstream(req, a.store.Now())
	}
	return OKEnvelope(struct{}{})
}

// interceptResponse handles both the single translated response and each
// translated stream chunk; the host sends the same shape for either. These
// carry the host request ID, which is what binds a stable response ID to the
// request that owns it. Their token fields are not trusted, because format
// translation may have estimated or synthesized them.
func (a *App) interceptResponse(raw []byte) ([]byte, error) {
	var req ResponseInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析客户端响应：%w", errUnmarshal)
	}
	// The header-only chunk that opens a stream carries no response data. The
	// non-stream call has no chunk index at all, so it reads as chunk 0.
	if req.ChunkIndex != StreamChunkHeaderInitIndex && a.store.Enabled() {
		a.usage.bindResponse(req.RequestID, req.Body)
	}
	return OKEnvelope(struct{}{})
}

// Usage events carry no request ID, so they can name credentials but never bill
// them; billing stays on the terminal lifecycle event.
func (a *App) handleUsage(raw []byte) ([]byte, error) {
	var record UsageRecord
	if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
		return nil, fmt.Errorf("解析用量记录：%w", errUnmarshal)
	}
	if a == nil || a.store == nil || !a.store.Enabled() {
		return OKEnvelope(struct{}{})
	}
	a.store.LearnCredential(record.AuthIndex, record.Provider, record.AuthType, record.Source)
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
		StatusCode:      QuotaExhaustedStatus,
		ResponseHeaders: headers,
		ResponseBody:    refusalBody(sourceFormat, quotaExhaustedError, quotaExhaustedMessage(decision)),
	}
}

// A refused model carries no Retry-After: waiting changes nothing, and only an
// operator can.
func modelForbiddenResponse(sourceFormat string, decision billing.ModelDecision) RequestInterceptResponse {
	message := modelForbiddenMessage(decision)
	return RequestInterceptResponse{
		Terminate:  true,
		StatusCode: ModelForbiddenStatus,
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

// Two shapes, because the proxy writes two: its Anthropic handler has an error
// envelope of its own, and every other route — Gemini included, which answers in
// the OpenAI shape rather than Google's — goes through one OpenAI-shaped builder.
// Substring matching keeps format variants such as "claude-code" working.
//
// A websocket client sees none of this: the proxy closes the connection instead
// of sending a terminated request's body, and only it can change that.
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

// The terminal lifecycle event is the single billing commit point, including
// failed and canceled requests. Rejected requests never reached an upstream.
func (a *App) handleRequestComplete(raw []byte) ([]byte, error) {
	var completion RequestCompletion
	if errUnmarshal := json.Unmarshal(raw, &completion); errUnmarshal != nil {
		// A malformed terminal event must not fail the host call; the pending
		// entry will expire from the bounded table.
		return OKEnvelope(struct{}{})
	}
	if a == nil || a.store == nil {
		return OKEnvelope(struct{}{})
	}
	if completion.Outcome == RequestCompletionRejected {
		a.store.DiscardRequest(completion.RequestID)
		a.usage.discard(completion.RequestID)
	} else {
		record, reason := a.usage.finish(completion.RequestID, a.store.BillingModel)
		if reason == "" {
			// What the client was shown comes first, because that is what the request
			// ended as for whoever made it. The host's own account stands in when
			// there was nothing to show: a websocket that was closed rather than
			// answered leaves the response hooks with no body to read.
			reason = completionFailure(completion.StatusCode, completion.Error)
		}
		a.store.FinishRequest(completion.RequestID, record, billingOutcome(completion.Outcome), reason)
	}
	return OKEnvelope(struct{}{})
}

func billingOutcome(outcome RequestCompletionOutcome) billing.RequestOutcome {
	switch outcome {
	case RequestCompletionSucceeded:
		return ""
	case RequestCompletionCanceled:
		return billing.OutcomeCanceled
	default:
		return billing.OutcomeFailed
	}
}

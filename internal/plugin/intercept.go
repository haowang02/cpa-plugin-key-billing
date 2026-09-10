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

// Bound the retry hint so clients periodically recheck quota availability.
const maxRetryAfterSeconds = 3600

// Only in-flight admission calls need a completion marker. Once they return,
// the Store's concurrency slots are sufficient for lifecycle bookkeeping.
type requestAdmission struct {
	calls     int
	completed bool
}

func (a *App) beginAdmission(requestID string) *requestAdmission {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil
	}
	a.admissionsMu.Lock()
	defer a.admissionsMu.Unlock()
	admission := a.admissions[requestID]
	if admission == nil {
		admission = &requestAdmission{}
		a.admissions[requestID] = admission
	}
	admission.calls++
	return admission
}

func (a *App) endAdmission(requestID string, admission *requestAdmission) {
	if admission == nil {
		return
	}
	a.admissionsMu.Lock()
	defer a.admissionsMu.Unlock()
	admission.calls--
	if admission.calls == 0 {
		delete(a.admissions, strings.TrimSpace(requestID))
	}
}

// Enforcement runs before auth so an over-quota request never occupies an
// upstream credential.
func (a *App) interceptBeforeAuth(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析请求拦截参数：%w", errUnmarshal)
	}
	if a == nil || a.store == nil {
		return OKEnvelope(RequestInterceptResponse{})
	}
	helper := metadataString(req.Metadata, MetadataSource) == SourcePluginHostModelCallback
	var admission *requestAdmission
	if !helper {
		admission = a.beginAdmission(req.RequestID)
		defer a.endAdmission(req.RequestID, admission)
	}
	if !a.store.Enabled() {
		return OKEnvelope(RequestInterceptResponse{})
	}
	scope := metadataString(req.Metadata, MetadataCallerScope)
	endpoint := metadataString(req.Metadata, MetadataRequestPath)

	// Reject disallowed models before quota checks can open a subscription period.
	if !helper {
		routing := a.store.ResolveRouting(scope, req.Model, req.RequestedModel)
		a.beginRouteLog(req.RequestID, scope, routing)
		if routing.ConfigurationError != "" {
			return OKEnvelope(routingConfigurationResponse(req.SourceFormat, routing.ConfigurationError))
		}
		if routing.RestrictsModels() && !routing.AllowsModel() {
			return OKEnvelope(modelForbiddenResponse(req.SourceFormat, routing))
		}
	}

	price, model, priceErr := a.store.ResolveModelPrice(req.Model, req.RequestedModel, true)
	if priceErr != nil {
		return OKEnvelope(priceRefusal(req.SourceFormat, "price_storage_error", "读取模型价格失败，请稍后重试"))
	}
	if price.Source == billing.PriceSourceNone {
		return OKEnvelope(priceRefusal(req.SourceFormat, "model_price_error", fmt.Sprintf("模型 %s 尚未定价", model)))
	}
	if helper {
		// Nested plugin helpers do not consume another client admission slot, but
		// still need a price: usage.handle can attribute their usage to the client.
		return OKEnvelope(RequestInterceptResponse{})
	}
	generate := true
	if value, ok := req.Metadata[MetadataGenerate].(bool); ok {
		generate = value
	}
	// Completion and the final admission commit share this lock. Checking the
	// marker alone would still let completion race with slot/cycle creation.
	a.admissionsMu.Lock()
	defer a.admissionsMu.Unlock()
	if admission != nil && admission.completed {
		return OKEnvelope(priceRefusal(req.SourceFormat, "request_completed", "请求已结束"))
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

	// Reference price refresh may have taken time. Quota and Retry-After share the
	// current instant, rather than the instant before the download.
	now := a.store.Now()
	decision := a.store.Authorize(scope, now)
	if !decision.Allowed {
		a.store.ReportQuotaBlock(scope, endpoint, decision)
		return OKEnvelope(quotaExhaustedResponse(req.SourceFormat, decision, now))
	}

	admitted = true
	return OKEnvelope(RequestInterceptResponse{})
}

func (a *App) interceptAfterAuth(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析凭证选择后请求拦截参数：%w", errUnmarshal)
	}
	if a != nil {
		a.observeRouteCredential(
			req.RequestID,
			metadataString(req.Metadata, MetadataSelectedAuth),
			metadataString(req.Metadata, MetadataSelectedIndex),
		)
	}
	return OKEnvelope(RequestInterceptResponse{})
}

func (a *App) completeRequest(raw []byte) ([]byte, error) {
	var completion RequestCompletion
	if errUnmarshal := json.Unmarshal(raw, &completion); errUnmarshal != nil {
		return nil, fmt.Errorf("解析请求完成事件：%w", errUnmarshal)
	}
	if a != nil && a.store != nil {
		func() {
			a.admissionsMu.Lock()
			defer a.admissionsMu.Unlock()
			if admission := a.admissions[strings.TrimSpace(completion.RequestID)]; admission != nil {
				admission.completed = true
			}
			a.store.ReleaseSlot(completion.RequestID)
		}()
		a.finishRouteLog(completion)
	}
	return OKEnvelope(struct{}{})
}

func routingConfigurationResponse(sourceFormat, message string) RequestInterceptResponse {
	return RequestInterceptResponse{Terminate: true, StatusCode: http.StatusServiceUnavailable, ResponseHeaders: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, ResponseBody: refusalBody(sourceFormat, refusal{anthropicType: "api_error", openaiType: "server_error", openaiCode: "routing_configuration_error"}, message)}
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
	var recordError billing.RequestError
	if record.Failed {
		failure := usageFailureDetails(record.Failure)
		recordError = billing.RequestError{StatusCode: failure.StatusCode, ErrorType: failure.ErrorType,
			Reason: failure.Reason, Body: failure.Body}
	}
	event := billing.UsageEvent{
		Scope:           scope,
		KeyPreview:      billing.PreviewKey(record.APIKey),
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
		Breakdown:       usageBreakdown(record),
		At:              a.store.Now(),
	}
	if record.Failed {
		a.store.RecordUsageError(event, recordError)
	} else {
		a.store.RecordUsage(event)
	}
	a.observeCredentialUsage(record.AuthIndex, record.AuthType, record.Source, scope)
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
	if retryAfter := retryAfterSeconds(decision.RetryAt, now); retryAfter > 0 {
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
func modelForbiddenResponse(sourceFormat string, decision billing.RoutingDecision) RequestInterceptResponse {
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

func quotaExhaustedMessage(decision billing.Decision) string {
	var builder strings.Builder
	builder.WriteString("API key subscription quota exhausted:")
	for _, window := range decision.Windows {
		for _, balance := range window.Dimensions {
			if !balance.Blocked {
				continue
			}
			fmt.Fprintf(&builder, " %q %s, resets at %s;", window.Name,
				balance.Description(), window.EndAt.UTC().Format(time.RFC3339))
		}
	}
	plan := strings.TrimSpace(decision.PlanName)
	if plan == "" {
		plan = strings.TrimSpace(decision.PlanID)
	}
	if plan != "" {
		builder.WriteString(" on plan ")
		builder.WriteString(strconv.Quote(plan))
	}
	builder.WriteString(".")
	if !decision.RetryAt.IsZero() {
		builder.WriteString(" Quota resets at ")
		builder.WriteString(decision.RetryAt.UTC().Format(time.RFC3339))
		builder.WriteString(".")
	}
	return builder.String()
}

func modelForbiddenMessage(decision billing.RoutingDecision) string {
	for _, model := range decision.DeniedModels {
		if strings.EqualFold(model, decision.Model) {
			return "API key is not allowed to use model " + strconv.Quote(decision.Model) + ". The model is denied by a routing rule."
		}
	}
	// Do not suggest allowlisted models that another bound route denies.
	allowed := make([]string, 0, len(decision.Models))
	for _, model := range decision.Models {
		candidate := decision
		candidate.Model = model
		if candidate.AllowsModel() {
			allowed = append(allowed, model)
		}
	}
	if len(allowed) == 0 {
		return "API key is not allowed to use model " + strconv.Quote(decision.Model) + ". No models are allowed by the routing rules."
	}
	shown := min(len(allowed), 5)
	omitted := len(allowed) - shown
	message := "API key is not allowed to use model " + strconv.Quote(decision.Model) +
		". Allowed models: " + strings.Join(allowed[:shown], ", ")
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
				"code":    kind.openaiCode,
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

func priceRefusal(format, code, message string) RequestInterceptResponse {
	kind := refusal{
		anthropicType: "cpa_key_billing_error",
		openaiType:    "cpa_key_billing_error",
		openaiCode:    code,
	}
	return RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      http.StatusServiceUnavailable,
		ResponseHeaders: http.Header{"Content-Type": {"application/json"}},
		ResponseBody:    refusalBody(format, kind, message),
	}
}

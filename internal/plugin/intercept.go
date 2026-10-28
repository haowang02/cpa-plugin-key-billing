package plugin

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

// Bound the retry hint so clients periodically recheck quota availability.
const maxRetryAfterSeconds = 3600

const (
	maxRequestOrigins = 16384
	// Client headers are display metadata; longer values stay unknown rather
	// than widening every stored request.
	maxClientHeaderBytes = 1024
)

type requestOriginKey struct {
	traceID, scope string
}

type requestOrigin struct {
	key requestOriginKey
	billing.RequestOrigin
}

// Retain origins after request.complete: usage is asynchronous and retries or
// WebSocket turns can report more than one record for the same inbound trace.
func (a *App) rememberRequestOrigin(req RequestInterceptRequest, scope, endpoint string) {
	key := requestOriginKey{strings.TrimSpace(req.TraceID), scope}
	endpoint = requestEndpoint(endpoint)
	if key.traceID == "" || endpoint == "" {
		return
	}
	reported := billing.RequestOrigin{
		Endpoint:      endpoint,
		Transport:     "http",
		UserAgent:     clientHeader(req.Headers.Get("User-Agent")),
		XForwardedFor: clientHeader(strings.Join(req.Headers.Values("X-Forwarded-For"), ", ")),
	}
	if headerHasToken(req.Headers, "Connection", "upgrade") && headerHasToken(req.Headers, "Upgrade", "websocket") {
		reported.Transport = "websocket"
	}
	a.requestOriginsMu.Lock()
	defer a.requestOriginsMu.Unlock()
	if entry := a.requestOrigins[key]; entry != nil {
		origin := entry.Value.(*requestOrigin)
		if origin.RequestOrigin != reported {
			// Conflicting metadata stays unknown for the retained trace.
			origin.RequestOrigin = billing.RequestOrigin{}
		}
		a.requestOriginOrder.MoveToFront(entry)
		return
	}
	a.requestOrigins[key] = a.requestOriginOrder.PushFront(&requestOrigin{key, reported})
	if len(a.requestOrigins) > maxRequestOrigins {
		oldest := a.requestOriginOrder.Back()
		delete(a.requestOrigins, oldest.Value.(*requestOrigin).key)
		a.requestOriginOrder.Remove(oldest)
	}
}

func (a *App) findRequestOrigin(traceID, scope string) billing.RequestOrigin {
	a.requestOriginsMu.Lock()
	defer a.requestOriginsMu.Unlock()
	entry := a.requestOrigins[requestOriginKey{strings.TrimSpace(traceID), scope}]
	if entry == nil {
		return billing.RequestOrigin{}
	}
	a.requestOriginOrder.MoveToFront(entry)
	return entry.Value.(*requestOrigin).RequestOrigin
}

func clientHeader(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > maxClientHeaderBytes {
		return ""
	}
	return value
}

func requestEndpoint(raw string) string {
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || len(raw) > 4096 {
		return ""
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return ""
	}
	// Persist only the route path, never query parameters or headers.
	return parsed.EscapedPath()
}

func headerHasToken(headers http.Header, name, token string) bool {
	for _, value := range headers.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// Enforcement runs before auth so an over-quota request never occupies an
// upstream credential.
func (a *App) interceptBeforeAuth(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("Parse request interception parameters: %w", errUnmarshal)
	}
	helper := metadataString(req.Metadata, MetadataSource) == SourcePluginHostModelCallback
	if !a.store.Enabled() {
		return OKEnvelope(RequestInterceptResponse{})
	}
	scope := metadataString(req.Metadata, MetadataCallerScope)
	endpoint := metadataString(req.Metadata, MetadataRequestPath)

	// Reject disallowed models before quota checks can open a subscription period.
	if !helper {
		a.rememberRequestOrigin(req, scope, endpoint)
		routing := a.store.ResolveRouting(scope, req.Model, req.RequestedModel)
		a.beginRouteLog(req.RequestID, scope, routing)
		if routing.RestrictsModels() && !routing.AllowsModel() {
			return OKEnvelope(modelForbiddenResponse(req.SourceFormat, routing))
		}
	}

	price, model, priceErr := a.store.ResolveModelPrice(req.Model, req.RequestedModel, true)
	if priceErr != nil {
		return OKEnvelope(priceRefusal(req.SourceFormat, "price_storage_error", "Failed to load model pricing. Please try again later."))
	}
	if price.Source == billing.PriceSourceNone && !a.store.Config().AllowUnpricedModels {
		return OKEnvelope(priceRefusal(req.SourceFormat, "model_price_error", fmt.Sprintf("Model %s has no configured price", model)))
	}
	if helper {
		// Nested plugin helpers follow the same pricing policy because usage.handle
		// can attribute their usage to the client, but consume no extra admission slot.
		return OKEnvelope(RequestInterceptResponse{})
	}
	generate := true
	if value, ok := req.Metadata[MetadataGenerate].(bool); ok {
		generate = value
	}
	if generate {
		slot := a.store.AcquireSlot(scope, req.RequestID)
		if !slot.Allowed {
			return OKEnvelope(concurrencyLimitResponse(req.SourceFormat, slot))
		}
	}

	// Reference price refresh may have taken time. Quota and Retry-After share the
	// current instant, rather than the instant before the download.
	now := a.store.Now()
	decision := a.store.Authorize(scope, now)
	if !decision.Allowed {
		if generate {
			a.store.ReleaseSlot(req.RequestID)
		}
		a.store.ReportQuotaBlock(scope, endpoint, decision)
		return OKEnvelope(quotaExhaustedResponse(req.SourceFormat, decision, now))
	}

	return OKEnvelope(RequestInterceptResponse{})
}

func (a *App) interceptAfterAuth(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("Parse post-auth request interception parameters: %w", errUnmarshal)
	}
	result, detail := "unrestricted", ""
	var response RequestInterceptResponse
	if a.store.Enabled() && metadataString(req.Metadata, MetadataSource) != SourcePluginHostModelCallback {
		// Credential permissions are independent of model aliases. Recheck the
		// current policy even when another scheduler made the selection.
		scope := metadataString(req.Metadata, MetadataCallerScope)
		decision := a.store.ResolveRouting(scope, req.Model, req.RequestedModel)
		a.beginRouteLog(req.RequestID, scope, decision)
		if decision.RestrictsCredentials() {
			allowed, err := a.selectedCredentialAllowed(req, decision)
			switch {
			case err != nil:
				result, detail = "unverified", err.Error()
				response = routingErrorResponse(req.SourceFormat, "routing_credential_unverified", detail)
			case !allowed:
				result, detail = "deny", "The selected upstream credential is not permitted by this API key's routing rules"
				response = routingErrorResponse(req.SourceFormat, "routing_credential_mismatch", detail)
			default:
				result = "allow"
			}
		}
	}
	if id := metadataString(req.Metadata, MetadataSelectedAuth); !response.Terminate && id != "" {
		a.store.AssignSlotCredential(req.RequestID, billing.CredentialFingerprint(id))
	}
	a.observeRouteCredential(req, result, detail)
	return OKEnvelope(response)
}

func (a *App) completeRequest(raw []byte) ([]byte, error) {
	var completion RequestCompletion
	if errUnmarshal := json.Unmarshal(raw, &completion); errUnmarshal != nil {
		return nil, fmt.Errorf("Parse request completion event: %w", errUnmarshal)
	}
	a.store.ReleaseSlot(completion.RequestID)
	a.finishRouteLog(completion)
	return OKEnvelope(struct{}{})
}

func routingErrorResponse(sourceFormat, code, message string) RequestInterceptResponse {
	return RequestInterceptResponse{Terminate: true, StatusCode: http.StatusServiceUnavailable, ResponseHeaders: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, ResponseBody: refusalBody(sourceFormat, refusal{anthropicType: "api_error", openaiType: "server_error", openaiCode: code}, message)}
}

func (a *App) handleUsage(raw []byte) ([]byte, error) {
	var record UsageRecord
	if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
		return nil, fmt.Errorf("Parse usage record: %w", errUnmarshal)
	}
	if !a.store.Enabled() {
		return OKEnvelope(struct{}{})
	}
	scope := billing.CallerScope(record.APIKey)
	var recordError billing.RequestError
	if record.Failed {
		recordError = usageFailureDetails(record.Failure)
	}
	at := a.store.Now()
	event := billing.UsageEvent{
		RequestOrigin:       a.findRequestOrigin(record.TraceID, scope),
		Execution:           record.executionMetadata(),
		Stream:              record.Stream,
		Scope:               scope,
		KeyPreview:          billing.PreviewKey(record.APIKey),
		AuthIndex:           record.AuthIndex,
		Provider:            record.Provider,
		ExecutorType:        record.ExecutorType,
		AuthType:            record.AuthType,
		Quota:               usageQuotaReport(record, at),
		Account:             a.usageAccount(record),
		ReasoningEffort:     record.ReasoningEffort,
		ServiceTier:         record.ServiceTier,
		ResponseServiceTier: record.ResponseServiceTier,
		UpstreamModel:       record.Model,
		ResponseModel:       record.ResponseModel,
		RouteModel:          record.Alias,
		RequestedAt:         record.RequestedAt,
		Latency:             record.Latency,
		TTFT:                record.TTFT,
		Breakdown:           usageBreakdown(record),
		At:                  at,
	}
	if record.Failed {
		a.store.RecordUsageError(event, recordError)
	} else {
		a.store.RecordUsage(event)
	}
	a.observeCredentialUsage(record.AuthIndex, record.AuthType, event.Account)
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

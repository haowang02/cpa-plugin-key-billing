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

// QuotaExhaustedStatus is the HTTP status returned to a client whose key has
// spent its cycle budget. 429 with an insufficient_quota error is the shape
// OpenAI-compatible clients already understand.
const QuotaExhaustedStatus = http.StatusTooManyRequests

// maxRetryAfterSeconds caps the Retry-After hint. A monthly plan can be weeks
// from resetting, and a client that sleeps literally for that long is worse off
// than one that retries hourly and gets another 429.
const maxRetryAfterSeconds = 3600

// interceptBeforeAuth runs before credential selection and is where a key that
// has exhausted its subscription is stopped. Enforcement lands here rather than
// after auth so an over-quota request never occupies an upstream credential.
func (a *App) interceptBeforeAuth(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析请求拦截参数：%w", errUnmarshal)
	}
	if a == nil || a.store == nil || !a.store.Enabled() {
		return OKEnvelope(RequestInterceptResponse{})
	}
	if metadataString(req.Metadata, MetadataSource) == SourcePluginHostModelCallback {
		// A nested execution another plugin started through the host, such as a
		// router asking a small model to classify the request.
		//
		// Skipping it under-charges when its tokens do not show up in the outer
		// response, which is the usual case: the host runs these as executions
		// of their own. Billing it instead would double-charge whenever they do
		// show up, and would charge them against a key that never asked for
		// them. Between an error that costs the operator a little and one that
		// overcharges their user, this takes the first.
		return OKEnvelope(RequestInterceptResponse{})
	}
	scope := metadataString(req.Metadata, MetadataCallerScope)
	if scope == "" {
		// No access provider principal, so there is no key to charge. This is
		// the unauthenticated-deployment case.
		return OKEnvelope(RequestInterceptResponse{})
	}

	// One clock read for the whole decision: the Retry-After hint is derived
	// from the same instant the budget was judged against, so it can never come
	// out a second short because the window rolled in between.
	now := a.store.Now()
	decision := a.store.Authorize(scope, now)
	if !decision.Allowed {
		return OKEnvelope(quotaExhaustedResponse(req.SourceFormat, decision, now))
	}

	// Remember who to bill. Token counts arrive later, off the response, and
	// are committed by the terminal lifecycle event under this same request ID.
	//
	// Before credential selection both model fields hold the client-facing
	// name; interceptAfterAuth fills in the upstream one.
	// No semantics are recorded here on purpose. CPA translates between
	// protocols, so the shape of the request says nothing reliable about the
	// shape of the counters that come back; those are read off the response
	// payloads themselves.
	a.store.BeginRequest(req.RequestID, billing.PendingRequest{
		Scope: scope,
		Model: req.RequestedModel,
		Alias: req.RequestedModel,
	})
	return OKEnvelope(RequestInterceptResponse{})
}

// interceptAfterAuth runs once a credential has been selected, which is the
// only point where the upstream model name is known. Recording it lets an
// operator price a model under either the name their client asks for or the one
// the provider bills them for.
//
// It never enforces: the budget check already happened before auth, so that an
// over-quota request never occupies a credential.
func (a *App) interceptAfterAuth(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析请求拦截参数：%w", errUnmarshal)
	}
	if a != nil && a.store != nil && a.store.Enabled() {
		a.store.NoteUpstreamModel(req.RequestID, req.Model)
	}
	return OKEnvelope(RequestInterceptResponse{})
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

// quotaExhaustedResponse builds the downstream rejection, shaped for the
// client's own protocol so its SDK surfaces a real error instead of a parse
// failure.
func quotaExhaustedResponse(sourceFormat string, decision billing.Decision, now time.Time) RequestInterceptResponse {
	message := quotaExhaustedMessage(decision)
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
		ResponseBody:    quotaExhaustedBody(sourceFormat, message),
	}
}

func quotaExhaustedMessage(decision billing.Decision) string {
	var builder strings.Builder
	builder.WriteString("API Key 计费额度已用尽：当前周期已消费 ")
	builder.WriteString(formatUSD(decision.SpentUSD))
	builder.WriteString(" USD，周期额度为 ")
	builder.WriteString(formatUSD(decision.LimitUSD))
	builder.WriteString(" USD")
	if name := strings.TrimSpace(decision.PlanName); name != "" {
		builder.WriteString("，订阅计划：")
		builder.WriteString(name)
	} else if id := strings.TrimSpace(decision.PlanID); id != "" {
		builder.WriteString("，订阅计划：")
		builder.WriteString(id)
	}
	builder.WriteString("。")
	if !decision.ResetAt.IsZero() {
		builder.WriteString("额度将在 ")
		builder.WriteString(decision.ResetAt.UTC().Format(time.RFC3339))
		builder.WriteString(" 重置。")
	}
	return builder.String()
}

// quotaExhaustedBody renders the error in the client's protocol. Matching on
// substrings rather than exact names keeps variants such as "gemini-cli"
// working.
func quotaExhaustedBody(sourceFormat, message string) []byte {
	normalized := strings.ToLower(strings.TrimSpace(sourceFormat))
	var payload any
	switch {
	case strings.Contains(normalized, "claude") || strings.Contains(normalized, "anthropic"):
		payload = map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "rate_limit_error",
				"message": message,
			},
		}
	case strings.Contains(normalized, "gemini") || strings.Contains(normalized, "antigravity"):
		payload = map[string]any{
			"error": map[string]any{
				"code":    QuotaExhaustedStatus,
				"message": message,
				"status":  "RESOURCE_EXHAUSTED",
			},
		}
	default: // openai, openai-response, codex, interactions, and anything new.
		payload = map[string]any{
			"error": map[string]any{
				"message": message,
				"type":    "insufficient_quota",
				"code":    "insufficient_quota",
			},
		}
	}
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return []byte(`{"error":{"message":"API Key 计费额度已用尽。","type":"insufficient_quota","code":"insufficient_quota"}}`)
	}
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

func (a *App) interceptResponse(raw []byte) ([]byte, error) {
	var req ResponseInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析响应拦截参数：%w", errUnmarshal)
	}
	a.observeTokens(req.RequestID, req.Body)
	return OKEnvelope(struct{}{})
}

func (a *App) interceptStreamChunk(raw []byte) ([]byte, error) {
	var req StreamChunkInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析流式响应拦截参数：%w", errUnmarshal)
	}
	if req.ChunkIndex != StreamChunkHeaderInitIndex {
		a.observeTokens(req.RequestID, req.Body)
	}
	return OKEnvelope(struct{}{})
}

func (a *App) observeTokens(requestID string, body []byte) {
	if a == nil || a.store == nil || !a.store.Enabled() || requestID == "" || len(body) == 0 {
		return
	}
	usage, semantics, ok := billing.ParseUsage(body)
	if ok {
		a.store.ObserveTokens(requestID, semantics, usage)
	}
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
	} else {
		a.store.FinishRequest(completion.RequestID, completion.Outcome != RequestCompletionSucceeded)
	}
	return OKEnvelope(struct{}{})
}

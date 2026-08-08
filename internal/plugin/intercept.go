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

	a.store.BeginRequest(req.RequestID, billing.PendingRequest{
		Scope:          scope,
		ClientProtocol: req.SourceFormat,
		CyclePlanID:    decision.PlanID,
		CycleStartAt:   decision.CycleStartAt,
		CycleEndAt:     decision.ResetAt,
		CycleLimitUSD:  decision.LimitUSD,
	})
	if a.hostSchema.Load() < CanonicalUsageSchemaVersion {
		generate := true
		if value, ok := req.Metadata["generate"].(bool); ok {
			generate = value
		}
		a.protocol2.begin(req.RequestID, req.SourceFormat, req.Model, req.RequestedModel, generate, a.store.Now())
	}
	return OKEnvelope(RequestInterceptResponse{})
}

func (a *App) interceptAfterAuth(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析请求拦截参数：%w", errUnmarshal)
	}
	if a.hostSchema.Load() < CanonicalUsageSchemaVersion {
		a.protocol2.addRoute(req.RequestID, req.ToFormat, req.Model, req.RequestedModel, req.Stream, req.Body)
	}
	return OKEnvelope(RequestInterceptResponse{})
}

// Protocol 2 exposes the raw upstream response only here, before response
// translation. The response is returned byte-for-byte; this hook observes
// authoritative provider usage and never modifies client-visible data.
func (a *App) normalizeResponseBefore(raw []byte) ([]byte, error) {
	var req ResponseTransformRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析上游响应：%w", errUnmarshal)
	}
	if a.hostSchema.Load() < CanonicalUsageSchemaVersion && a.store.Enabled() {
		a.protocol2.observeUpstream(req, a.store.Now())
	}
	return OKEnvelope(PayloadResponse{Body: req.Body})
}

// Translated responses carry the host request ID. Their token fields are not
// trusted because protocol translation may have estimated or synthesized them.
func (a *App) interceptResponseAfter(raw []byte) ([]byte, error) {
	var req ResponseInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析客户端响应：%w", errUnmarshal)
	}
	if a.hostSchema.Load() < CanonicalUsageSchemaVersion && a.store.Enabled() {
		a.protocol2.bindResponse(req.RequestID, req.Body)
	}
	return OKEnvelope(PayloadResponse{})
}

func (a *App) interceptResponseStreamChunk(raw []byte) ([]byte, error) {
	var req ResponseInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析客户端流式响应：%w", errUnmarshal)
	}
	if req.ChunkIndex != StreamChunkHeaderInitIndex && a.hostSchema.Load() < CanonicalUsageSchemaVersion && a.store.Enabled() {
		a.protocol2.bindResponse(req.RequestID, req.Body)
	}
	return OKEnvelope(PayloadResponse{})
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
	builder.WriteString("API Key 订阅额度已用尽：本期费用 $")
	builder.WriteString(formatUSD(decision.SpentUSD))
	builder.WriteString("，额度为 $")
	builder.WriteString(formatUSD(decision.LimitUSD))
	if name := strings.TrimSpace(decision.PlanName); name != "" {
		builder.WriteString("，计划：")
		builder.WriteString(name)
	} else if id := strings.TrimSpace(decision.PlanID); id != "" {
		builder.WriteString("，计划：")
		builder.WriteString(id)
	}
	builder.WriteString("。")
	if !decision.ResetAt.IsZero() {
		builder.WriteString("额度将于 ")
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
		a.protocol2.discard(completion.RequestID)
	} else {
		records := canonicalUsageRecords(completion.UsageRecords)
		if a.hostSchema.Load() < CanonicalUsageSchemaVersion {
			records = a.protocol2.finish(completion.RequestID)
		}
		a.store.FinishRequest(completion.RequestID, records, completion.Outcome != RequestCompletionSucceeded)
	}
	return OKEnvelope(struct{}{})
}

func canonicalUsageRecords(records []RequestUsageRecord) []billing.UsageRecord {
	if len(records) == 0 {
		return nil
	}
	result := make([]billing.UsageRecord, 0, len(records))
	for _, record := range records {
		result = append(result, billing.UsageRecord{
			Provider:    record.Provider,
			Model:       record.Model,
			Alias:       record.Alias,
			Generate:    record.Generate,
			Failed:      record.Failed,
			RequestedAt: record.RequestedAt,
			Breakdown: billing.TokenBreakdown{
				SchemaVersion:      record.Breakdown.SchemaVersion,
				Quality:            record.Breakdown.Quality,
				TotalTokens:        record.Breakdown.TotalTokens,
				UnclassifiedTokens: record.Breakdown.UnclassifiedTokens,
				Input: billing.TokenInputBreakdown{
					TotalTokens:      record.Breakdown.Input.TotalTokens,
					UncachedTokens:   record.Breakdown.Input.UncachedTokens,
					CacheReadTokens:  record.Breakdown.Input.CacheReadTokens,
					CacheWriteTokens: record.Breakdown.Input.CacheWriteTokens,
				},
				Output: billing.TokenOutputBreakdown{
					TotalTokens:        record.Breakdown.Output.TotalTokens,
					NonReasoningTokens: record.Breakdown.Output.NonReasoningTokens,
					ReasoningTokens:    record.Breakdown.Output.ReasoningTokens,
				},
			},
		})
	}
	return result
}

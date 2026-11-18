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

// CLIProxyAPI derives type and code from the status alone, so an exhausted budget reads as a rate limit and a
// refused model as a quota problem; clients cannot tell whether the proxy or the plugin refused.
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

// Use the client's API format so its SDK surfaces an error instead of a parse failure.
func refusalResponse(sourceFormat string, status int, kind refusal, message string) RequestInterceptResponse {
	return RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      status,
		ResponseHeaders: http.Header{"Content-Type": {"application/json; charset=utf-8"}},
		ResponseBody:    refusalBody(sourceFormat, kind, message),
	}
}

// Anthropic formats, including variants like "claude-code", get their native envelope; others the OpenAI one.
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

func quotaExhaustedResponse(sourceFormat string, decision billing.Decision, now time.Time) RequestInterceptResponse {
	response := refusalResponse(sourceFormat, http.StatusTooManyRequests, quotaExhaustedError, quotaExhaustedMessage(decision))
	if retryAfter := retryAfterSeconds(decision.RetryAt, now); retryAfter > 0 {
		response.ResponseHeaders.Set("Retry-After", strconv.Itoa(retryAfter))
	}
	return response
}

func concurrencyLimitResponse(sourceFormat string, decision billing.SlotDecision) RequestInterceptResponse {
	message := fmt.Sprintf("API key concurrency limit reached: %d active requests of %d allowed.", decision.Active, decision.Limit)
	response := refusalResponse(sourceFormat, http.StatusTooManyRequests, quotaExhaustedError, message)
	response.ResponseHeaders.Set("Retry-After", "1")
	return response
}

// A refused model carries no Retry-After: waiting changes nothing, and only an operator can.
func modelForbiddenResponse(sourceFormat string, routing billing.RouteRule, model string) RequestInterceptResponse {
	return refusalResponse(sourceFormat, http.StatusForbidden, modelForbiddenError, modelForbiddenMessage(routing, model))
}

func modelUnservedResponse(sourceFormat, model string) RequestInterceptResponse {
	return refusalResponse(sourceFormat, http.StatusForbidden, modelForbiddenError, routeNoCredential.message(model))
}

func routingErrorResponse(sourceFormat, code, message string) RequestInterceptResponse {
	return refusalResponse(sourceFormat, http.StatusServiceUnavailable, refusal{anthropicType: "api_error", openaiType: "server_error", openaiCode: code}, message)
}

func priceRefusal(sourceFormat, code, message string) RequestInterceptResponse {
	return refusalResponse(sourceFormat, http.StatusServiceUnavailable, refusal{anthropicType: "cpa_key_billing_error", openaiType: "cpa_key_billing_error", openaiCode: code}, message)
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

func modelForbiddenMessage(routing billing.RouteRule, model string) string {
	refused := "API key is not allowed to use model " + strconv.Quote(model) + "."
	for _, denied := range routing.DeniedModels {
		if strings.EqualFold(denied, model) {
			return refused + " The model is denied by a routing rule."
		}
	}
	// Do not suggest allowlisted models that another bound route denies.
	allowed := make([]string, 0, len(routing.Models))
	for _, candidate := range routing.Models {
		if routing.AllowsModel(candidate) {
			allowed = append(allowed, candidate)
		}
	}
	if len(allowed) == 0 {
		return refused + " No models are allowed by the routing rules."
	}
	shown := min(len(allowed), 5)
	message := refused + " Allowed models: " + strings.Join(allowed[:shown], ", ")
	if omitted := len(allowed) - shown; omitted > 0 {
		message += fmt.Sprintf(" and %d more", omitted)
	}
	return message + "."
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

// Credential selection refuses with an error envelope, which reaches request.complete only as its message; the
// route log maps that message back to the result.
type routingShortfall struct {
	result, prefix string
	status         int
}

var (
	routeUnavailable  = routingShortfall{"unavailable", "The upstream credentials allowed by this API key's routing rules are unavailable for model", http.StatusServiceUnavailable}
	routeNoCredential = routingShortfall{"no_credential", "None of the upstream credentials allowed by this API key's routing rules serves model", http.StatusForbidden}
	routingShortfalls = []routingShortfall{routeUnavailable, routeNoCredential}
)

func (s routingShortfall) message(model string) string {
	return s.prefix + " " + strconv.Quote(billing.ModelWithoutThinkingSuffix(model)) + "."
}

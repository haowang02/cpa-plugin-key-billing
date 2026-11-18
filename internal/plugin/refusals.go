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

func routingErrorResponse(sourceFormat, code, message string) RequestInterceptResponse {
	return RequestInterceptResponse{Terminate: true, StatusCode: http.StatusServiceUnavailable, ResponseHeaders: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, ResponseBody: refusalBody(sourceFormat, refusal{anthropicType: "api_error", openaiType: "server_error", openaiCode: code}, message)}
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

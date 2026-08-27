package plugin

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

const testAPIKey = "sk-test-key-0001"

func exhaustedApp(t *testing.T, resetAfter time.Duration) *App {
	t.Helper()
	app := newAppWithPrice(t, true)
	if _, errSync := app.store.SyncKeys([]string{testAPIKey}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	period := billing.Period{Kind: billing.PeriodCustom, Seconds: int64(resetAfter / time.Second)}
	if resetAfter == 0 {
		period = billing.Period{Kind: billing.PeriodNever}
	}
	if _, errCreate := app.store.CreatePlanWithBindings(billing.Plan{
		ID: "plan-5", Name: "Plan 5", AmountUSD: 5, Period: period,
	}, []string{billing.CallerScope(testAPIKey)}); errCreate != nil {
		t.Fatalf("CreatePlanWithBindings error = %v", errCreate)
	}
	admit(t, app, "openai", "/v1/chat/completions")
	billUsage(t, app, 0, 0, 0, 2_500_000, 0)
	return app
}

func callIntercept(t *testing.T, app *App, sourceFormat string) RequestInterceptResponse {
	t.Helper()
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		SourceFormat: sourceFormat,
		Metadata:     map[string]any{MetadataCallerScope: billing.CallerScope(testAPIKey)},
	}))
	if err != nil {
		t.Fatalf("request.intercept_before error = %v", err)
	}
	var resp RequestInterceptResponse
	decodeResult(t, raw, &resp)
	return resp
}

func TestInterceptTerminatesAnExhaustedKey(t *testing.T) {
	app := exhaustedApp(t, 30*time.Minute)
	resp := callIntercept(t, app, "openai")
	if !resp.Terminate || resp.StatusCode != QuotaExhaustedStatus {
		t.Fatalf("response = %+v, want quota rejection", resp)
	}
	retryAfter, err := strconv.Atoi(resp.ResponseHeaders.Get("Retry-After"))
	if err != nil || retryAfter != 1800 {
		t.Fatalf("Retry-After = %q, want 1800", resp.ResponseHeaders.Get("Retry-After"))
	}
}

func TestInterceptNeverResetPlanHasNoRetryHint(t *testing.T) {
	app := exhaustedApp(t, 0)

	resp := callIntercept(t, app, "openai")
	if !resp.Terminate || resp.ResponseHeaders.Get("Retry-After") != "" {
		t.Fatalf("response = %+v, want a rejection without Retry-After", resp)
	}
	if strings.Contains(string(resp.ResponseBody), "resets") {
		t.Fatalf("body = %s, should not promise an automatic reset", resp.ResponseBody)
	}
}

// The type and code are the ones CLIProxyAPI derives from the status, so a
// client branching on them cannot tell whether the proxy or the plugin refused.
// Anthropic routes get the proxy's own envelope; every other format, Gemini
// included, gets its OpenAI-shaped one.
func TestInterceptUsesTheClientErrorShape(t *testing.T) {
	app := exhaustedApp(t, 12*time.Hour)
	for _, test := range []struct {
		format string
		want   map[string]string
	}{
		{"openai", map[string]string{"type": "rate_limit_error", "code": "rate_limit_exceeded"}},
		{"claude", map[string]string{"type": "rate_limit_error"}},
		{"gemini", map[string]string{"type": "rate_limit_error", "code": "rate_limit_exceeded"}},
	} {
		t.Run(test.format, func(t *testing.T) {
			resp := callIntercept(t, app, test.format)
			var payload struct {
				Error map[string]any `json:"error"`
			}
			if err := json.Unmarshal(resp.ResponseBody, &payload); err != nil {
				t.Fatalf("invalid JSON error body: %v (raw=%s)", err, resp.ResponseBody)
			}
			for field, want := range test.want {
				if got, _ := payload.Error[field].(string); got != want {
					t.Fatalf("error.%s = %q, want %q (body=%s)", field, got, want, resp.ResponseBody)
				}
			}
			if message, _ := payload.Error["message"].(string); !strings.Contains(message, "quota exhausted") {
				t.Fatalf("message = %q, want it to state the exhausted quota", message)
			}
		})
	}
}

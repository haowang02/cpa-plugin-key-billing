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

func exhaustedApp(t *testing.T, now time.Time) *App {
	t.Helper()
	app := newAppWithPrice(t, true)
	app.store.SetNow(func() time.Time { return now })
	app.store.Update(func(state *billing.State) {
		state.Plans = []billing.Plan{{
			ID: "daily-5", Name: "Daily 5", AmountUSD: 5,
			Period: billing.Period{Kind: billing.PeriodDaily},
		}}
		state.Keys[billing.CallerScope(testAPIKey)] = &billing.KeyState{
			Scope: billing.CallerScope(testAPIKey), PlanID: "daily-5",
			Cycle: billing.Cycle{
				PlanID: "daily-5", SpentUSD: 5,
				StartAt: time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC),
				EndAt:   time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC),
			},
		}
	})
	return app
}

func callIntercept(t *testing.T, app *App, sourceFormat string) RequestInterceptResponse {
	t.Helper()
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		RequestID: "req-1", SourceFormat: sourceFormat, Model: "gpt-5.5",
		Metadata: map[string]any{MetadataCallerScope: billing.CallerScope(testAPIKey)},
	}))
	if err != nil {
		t.Fatalf("request.intercept_before error = %v", err)
	}
	var resp RequestInterceptResponse
	decodeResult(t, raw, &resp)
	return resp
}

func TestInterceptTerminatesAnExhaustedKey(t *testing.T) {
	app := exhaustedApp(t, time.Date(2026, 8, 3, 23, 30, 0, 0, time.UTC))
	resp := callIntercept(t, app, "openai")
	if !resp.Terminate || resp.StatusCode != QuotaExhaustedStatus {
		t.Fatalf("response = %+v, want quota rejection", resp)
	}
	retryAfter, err := strconv.Atoi(resp.ResponseHeaders.Get("Retry-After"))
	if err != nil || retryAfter != 1800 {
		t.Fatalf("Retry-After = %q, want 1800", resp.ResponseHeaders.Get("Retry-After"))
	}
}

func TestInterceptUsesTheClientErrorShape(t *testing.T) {
	app := exhaustedApp(t, time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	for _, test := range []struct {
		format string
		marker string
	}{
		{"openai", "insufficient_quota"},
		{"claude", "rate_limit_error"},
		{"gemini", "RESOURCE_EXHAUSTED"},
		{"unknown", "insufficient_quota"},
	} {
		t.Run(test.format, func(t *testing.T) {
			resp := callIntercept(t, app, test.format)
			var payload any
			if err := json.Unmarshal(resp.ResponseBody, &payload); err != nil {
				t.Fatalf("invalid JSON error body: %v", err)
			}
			if !strings.Contains(string(resp.ResponseBody), test.marker) {
				t.Fatalf("body %s does not contain %q", resp.ResponseBody, test.marker)
			}
		})
	}
}

package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
	if _, errCreate := app.store.CreatePlanWithBindings(billing.Plan{
		ID: "plan-5", Name: "Plan 5", Windows: []billing.QuotaWindow{{Name: "额度", Amount: 5, PeriodSeconds: int64(resetAfter / time.Second)}},
	}, []string{billing.CallerScope(testAPIKey)}); errCreate != nil {
		t.Fatalf("CreatePlanWithBindings error = %v", errCreate)
	}
	requestID := admit(t, app, "openai", "/v1/chat/completions")
	billUsage(t, app, 0, 0, 0, 2_500_000, 0)
	completeRequest(t, app, requestID)
	return app
}

func callIntercept(t *testing.T, app *App, sourceFormat string) RequestInterceptResponse {
	t.Helper()
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		SourceFormat: sourceFormat,
		Model:        "gpt-5.5",
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
	if !resp.Terminate || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("response = %+v, want quota rejection", resp)
	}
	retryAfter, err := strconv.Atoi(resp.ResponseHeaders.Get("Retry-After"))
	if err != nil || retryAfter != 1800 {
		t.Fatalf("Retry-After = %q, want 1800", resp.ResponseHeaders.Get("Retry-After"))
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

func TestInterceptEnforcesQuotaDimensionsAfterRestart(t *testing.T) {
	app, path := newAppWithPriceAndState(t, true)
	if _, err := app.store.SyncKeys([]string{testAPIKey}, false); err != nil {
		t.Fatal(err)
	}
	_, err := app.store.CreatePlanWithBindings(billing.Plan{ID: "p", Windows: []billing.QuotaWindow{
		{Name: "请求", RequestLimit: 1, PeriodSeconds: 3600},
		{Name: "Token", TokenLimit: 1000, PeriodSeconds: 7200},
	}}, []string{flowScope()})
	if err != nil {
		t.Fatal(err)
	}
	admit(t, app, "openai", "/v1/chat/completions")
	record := UsageRecord{APIKey: testAPIKey, Model: flowModel, Provider: "openai",
		RequestedAt: app.store.Now(), Failed: true, Failure: UsageFailure{StatusCode: 502}}
	publishUsageRecord(t, app, record)
	if response := callIntercept(t, app, "openai"); response.Terminate {
		t.Fatal("failed record consumed request quota")
	}
	record.Failed = false
	publishUsageRecord(t, app, record)
	response := callIntercept(t, app, "openai")
	if response.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(response.ResponseBody), "requests 1 / 1") {
		t.Fatalf("successful record did not exhaust request quota: %+v", response)
	}
	// This usage belongs to an already admitted concurrent execution.
	record.Provider = "unknown"
	record.Detail = UsageDetail{TotalTokens: 1200}
	publishUsageRecord(t, app, record)
	app.Shutdown()
	reopened := newTestApp(t)
	defer reopened.Shutdown()
	if _, err := reopened.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{
		ConfigYAML: []byte("enabled: true\nstate_file: " + path + "\n"),
	})); err != nil {
		t.Fatal(err)
	}
	response = callIntercept(t, reopened, "openai")
	if response.StatusCode != 429 || !strings.Contains(string(response.ResponseBody), "tokens 1200 / 1000") ||
		!strings.Contains(string(response.ResponseBody), "requests 2 / 1") {
		t.Fatalf("restart did not preserve exhaustion: %+v", response)
	}
}

func TestRequestOriginsRespectScopeAndCapacity(t *testing.T) {
	app := newApp(nil)
	var group sync.WaitGroup
	for worker := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := worker; i < maxRequestOrigins; i += 8 {
				trace, endpoint := fmt.Sprint(i), fmt.Sprintf("/endpoint/%d", i)
				app.rememberRequestOrigin(RequestInterceptRequest{TraceID: trace}, "scope", endpoint)
				if got := app.findRequestOrigin(trace, "scope"); got.Endpoint != endpoint {
					t.Errorf("concurrent origin crossed traces: %+v", got)
				}
			}
		}()
	}
	group.Wait()
	keep := app.requestOriginOrder.Back().Value.(*requestOrigin).key
	app.findRequestOrigin(keep.traceID, keep.scope)
	evicted := app.requestOriginOrder.Back().Value.(*requestOrigin).key
	app.rememberRequestOrigin(RequestInterceptRequest{TraceID: "new-trace"}, "scope", "/v1/messages")
	if len(app.requestOrigins) != maxRequestOrigins || app.findRequestOrigin(evicted.traceID, evicted.scope).Endpoint != "" ||
		app.findRequestOrigin(keep.traceID, keep.scope).Endpoint == "" {
		t.Fatal("origin retention did not evict the least recently used trace")
	}

	for _, test := range []struct {
		scope, path string
		headers     http.Header
	}{
		{"scope-a", "/v1/responses", http.Header{"User-Agent": {strings.Repeat("a", maxClientHeaderBytes+1)}}},
		{"scope-b", "/v1/messages?key=dummy-secret", http.Header{
			"User-Agent": {" dummy-client/1.0 "}, "X-Forwarded-For": {"192.0.2.10", "198.51.100.20"},
		}},
	} {
		app.rememberRequestOrigin(RequestInterceptRequest{TraceID: "shared", Headers: test.headers}, test.scope, test.path)
	}
	if got := app.findRequestOrigin("shared", "scope-b"); got != (billing.RequestOrigin{
		Endpoint: "/v1/messages", Transport: "http", UserAgent: "dummy-client/1.0", XForwardedFor: "192.0.2.10, 198.51.100.20",
	}) {
		t.Fatalf("incorrect scoped origin: %+v", got)
	}
	if got := app.findRequestOrigin("shared", "scope-a"); got != (billing.RequestOrigin{Endpoint: "/v1/responses", Transport: "http"}) {
		t.Fatalf("origin crossed caller scopes or kept an oversized header: %+v", got)
	}
	for _, endpoint := range []string{"/v1/messages", "/v1/responses"} {
		app.rememberRequestOrigin(RequestInterceptRequest{TraceID: "shared"}, "scope-a", endpoint)
		if got := app.findRequestOrigin("shared", "scope-a"); got != (billing.RequestOrigin{}) {
			t.Fatalf("conflicting trace ID was guessed: %+v", got)
		}
	}
	if got := app.findRequestOrigin("missing", "scope-a"); got != (billing.RequestOrigin{}) {
		t.Fatalf("missing trace ID was guessed: %+v", got)
	}
}

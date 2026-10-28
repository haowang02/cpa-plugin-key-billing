package plugin

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/sqlite"
)

const flowModel = "gpt-5.5"

func flowScope() string { return billing.CallerScope(testAPIKey) }

func flowMetadata() map[string]any {
	return map[string]any{MetadataCallerScope: flowScope()}
}

func admit(t *testing.T, app *App, clientFormat, requestPath string) string {
	t.Helper()
	requestID := rand.Text()
	metadata := flowMetadata()
	metadata[MetadataRequestPath] = requestPath
	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		RequestID: requestID, SourceFormat: clientFormat, Model: flowModel, RequestedModel: flowModel, Metadata: metadata,
	}))
	if errHandle != nil {
		t.Fatalf("request.intercept_before error = %v", errHandle)
	}
	var response RequestInterceptResponse
	decodeResult(t, raw, &response)
	if response.Terminate {
		t.Fatalf("request was terminated: %s", response.ResponseBody)
	}
	return requestID
}

func publishUsageRecord(t *testing.T, app *App, record UsageRecord) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodUsageHandle, mustMarshal(t, record))
	if errHandle != nil {
		t.Fatalf("usage.handle error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
}

func billUsage(t *testing.T, app *App, uncached, cacheRead, cacheWrite, output, reasoning int64) {
	t.Helper()
	publishUsageRecord(t, app, UsageRecord{
		Provider:     "openai",
		ExecutorType: "OpenAICompatExecutor",
		Model:        flowModel,
		Alias:        flowModel,
		APIKey:       testAPIKey,
		Generate:     true,
		RequestedAt:  app.store.Now(),
		Detail: UsageDetail{
			InputTokens:         uncached + cacheRead + cacheWrite,
			OutputTokens:        output,
			ReasoningTokens:     reasoning,
			CacheReadTokens:     cacheRead,
			CacheCreationTokens: cacheWrite,
			TotalTokens:         uncached + cacheRead + cacheWrite + output,
		},
	})
}

func requestEventCost(t *testing.T, app *App) (float64, int64) {
	t.Helper()
	var cost float64
	var requests int64
	for _, entry := range requestEventEntries(t, app) {
		if entry.Scope == flowScope() && (!entry.Failed || entry.AccountingQuality != "") {
			cost += entry.Cost.Total
			requests++
		}
	}
	return cost, requests
}

func assertCostClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("cost = %.12f, want %.12f", got, want)
	}
}

func TestUsageHandleBillsWithoutResponseOrCompletionHooks(t *testing.T) {
	app := newAppWithPrice(t, true)
	billUsage(t, app, 500, 400, 100, 500, 200)
	cost, requests := requestEventCost(t, app)
	assertCostClose(t, cost, 0.0005+0.00004+0.000125+0.001)
	if requests != 1 {
		t.Fatalf("Requests = %d, want 1", requests)
	}
	if entry := requestEventEntries(t, app)[0]; entry.RequestOrigin != (billing.RequestOrigin{}) || entry.Stream != nil {
		t.Fatalf("missing host metadata was reconstructed: %+v", entry)
	}
}

func TestUsageHandleUsesClientKeyModelAliasAndCredential(t *testing.T) {
	app := newAppWithPrice(t, true)
	if response := interceptWithID(t, app, "metadata-trace", true); response.Terminate {
		t.Fatalf("request rejected: %+v", response)
	}
	app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
		if method != hostAuthGetRuntime || payload.(map[string]string)["auth_index"] != "auth-7" {
			t.Fatalf("unexpected host call: %s %+v", method, payload)
		}
		return json.RawMessage(`{"auth":{"name":"codex-team.json","path":"/auths/codex-team.json"}}`), nil
	})
	stream := true
	// The host reports the caller's own key for an auth file without an email.
	publishUsageRecord(t, app, UsageRecord{
		TraceID: "metadata-trace", Stream: &stream,
		Provider: "codex", ExecutorType: "CodexExecutor", Model: flowModel, Alias: "route/gpt-5.5",
		ResponseModel: "gpt-5.6-luna", APIKey: testAPIKey, AuthIndex: "auth-7", AuthType: "oauth",
		Source: testAPIKey, ReasoningEffort: "high", ServiceTier: "priority", ResponseServiceTier: "default",
		Generate: true, RequestedAt: app.store.Now(), Latency: 1500 * time.Millisecond, TTFT: 250 * time.Millisecond,
		Detail: UsageDetail{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500},
	})

	entries := requestEventEntries(t, app)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	entry := entries[0]
	if entry.AuthIndex != "auth-7" || entry.ExecutorType != "CodexExecutor" ||
		entry.ReasoningEffort != "high" || entry.ServiceTier != "priority" ||
		entry.ResponseServiceTier != "default" || entry.ResponseModel != "gpt-5.6-luna" ||
		entry.UpstreamModel != flowModel || entry.BillingModel != "route/gpt-5.5" || entry.Failed ||
		entry.Source != "codex · codex-team" || entry.AccountingQuality != billing.TokenAccountingComplete ||
		entry.LatencyMS != 1500 || entry.TTFTMS != 250 ||
		entry.Endpoint != "/v1/responses" || entry.Transport != "http" || entry.Stream == nil || !*entry.Stream {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestUsageHandleReportsZeroUsageFailureAndBillsReportedFailureUsage(t *testing.T) {
	app := newAppWithPrice(t, true)
	if _, errSync := app.store.SyncKeys([]string{testAPIKey}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	if errLabel := app.store.SetLabel(flowScope(), "Alice"); errLabel != nil {
		t.Fatalf("SetLabel error = %v", errLabel)
	}
	publishUsageRecord(t, app, UsageRecord{
		Provider: "codex", Model: flowModel, Alias: flowModel, APIKey: testAPIKey,
		AuthIndex: "auth-failed", AuthType: "oauth", Source: "billing@example.com",
		Generate: true, Failed: true, Failure: UsageFailure{
			StatusCode: 502,
			Body:       `{"error":{"message":"service overloaded","type":"service_unavailable_error"}}`,
		},
	})
	if cost, requests := requestEventCost(t, app); cost != 0 || requests != 0 {
		t.Fatalf("zero failure cost = %v, requests = %d", cost, requests)
	}
	errors, errErrors := app.store.RequestErrors(billing.RequestErrorQuery{})
	if errErrors != nil || len(errors.Entries) != 1 {
		t.Fatalf("RequestErrors = %+v, %v", errors, errErrors)
	}
	requestError := errors.Entries[0]
	if requestError.Label != "Alice" || requestError.Provider != "codex" ||
		requestError.Source != "codex · billing@example.com" || requestError.StatusCode != 502 ||
		requestError.ErrorType != "service_unavailable_error" {
		t.Fatalf("request error = %+v", requestError)
	}

	publishUsageRecord(t, app, UsageRecord{
		Provider: "openai", Model: flowModel, Alias: flowModel, APIKey: testAPIKey,
		Generate: true, Failed: true, RequestedAt: app.store.Now(),
		Detail: UsageDetail{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500},
	})
	if cost, requests := requestEventCost(t, app); cost <= 0 || requests != 1 {
		t.Fatalf("reported failure cost = %v, requests = %d", cost, requests)
	}
	if entries := requestEventEntries(t, app); len(entries) != 2 || !entries[0].Failed {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestUsageHandleBillsTheSuccessfulRetryAttemptOnce(t *testing.T) {
	app := newAppWithPrice(t, true)
	req := RequestInterceptRequest{
		RequestID: "retry-request", TraceID: "retry-trace", Model: flowModel,
		Headers:  http.Header{"Connection": {"keep-alive, Upgrade"}, "Upgrade": {"WebSocket"}},
		Metadata: map[string]any{MetadataCallerScope: flowScope(), MetadataRequestPath: "/v1/responses"},
	}
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, req))
	if err != nil {
		t.Fatal(err)
	}
	var response RequestInterceptResponse
	decodeResult(t, raw, &response)
	if response.Terminate {
		t.Fatalf("request rejected: %+v", response)
	}
	completeRequest(t, app, req.RequestID)
	// Async usage survives completion; nested helpers must not replace its origin.
	req.Headers = nil
	req.Metadata[MetadataSource] = SourcePluginHostModelCallback
	req.Metadata[MetadataRequestPath] = "/helper"
	if _, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, req)); err != nil {
		t.Fatal(err)
	}
	stream := true
	publishUsageRecord(t, app, UsageRecord{
		TraceID: req.TraceID, Stream: &stream,
		Provider: "codex", Model: flowModel, Alias: flowModel, APIKey: testAPIKey,
		AuthIndex: "auth-failed", AuthType: "oauth", Source: "failed@example.com",
		Generate: true, Failed: true,
	})
	publishUsageRecord(t, app, UsageRecord{
		TraceID: req.TraceID, Stream: &stream,
		Provider: "codex", Model: flowModel, Alias: flowModel, APIKey: testAPIKey,
		AuthIndex: "auth-success", AuthType: "oauth", Source: "success@example.com",
		Generate: true, RequestedAt: app.store.Now(),
		Detail: UsageDetail{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500},
	})

	if cost, requests := requestEventCost(t, app); cost <= 0 || requests != 1 {
		t.Fatalf("cost = %v, requests = %d", cost, requests)
	}
	if entries := requestEventEntries(t, app); len(entries) != 2 || entries[0].AuthIndex != "auth-success" ||
		entries[0].Source != "codex · success@example.com" {
		t.Fatalf("entries = %+v", entries)
	}
	for _, entry := range requestEventEntries(t, app) {
		if entry.Endpoint != "/v1/responses" || entry.Transport != "websocket" || entry.Stream == nil || !*entry.Stream {
			t.Fatalf("retry lost its request origin: %+v", entry)
		}
	}
}

func TestUsageHandleBillsNonGeneratingRequest(t *testing.T) {
	app := newAppWithPrice(t, true)
	publishUsageRecord(t, app, UsageRecord{
		Provider: "codex", ExecutorType: "CodexWebsocketsExecutor",
		Model: flowModel, Alias: flowModel, APIKey: testAPIKey, Generate: false,
		Stream: new(bool), TTFT: 12 * time.Millisecond, Latency: 12 * time.Millisecond,
		Detail: UsageDetail{InputTokens: 1000, CacheReadTokens: 400, TotalTokens: 1000},
	})
	cost, requests := requestEventCost(t, app)
	assertCostClose(t, cost, 0.0006+0.00004)
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	entries := requestEventEntries(t, app)
	if len(entries) != 1 || entries[0].Cost.CacheReadTokens != 400 || entries[0].Cost.BilledOutputTokens != 0 {
		t.Fatalf("entries = %+v", entries)
	}
	if entry := entries[0]; entry.RequestOrigin != (billing.RequestOrigin{}) || entry.Stream == nil || *entry.Stream || entry.TTFTMS != 12 {
		t.Fatalf("invented request metadata or changed host timing: %+v", entry)
	}
}

func TestUsageHandleRecordsUnscopedUsageWithoutCreatingKey(t *testing.T) {
	app := newAppWithPrice(t, true)
	now := app.store.Now()
	publishUsageRecord(t, app, UsageRecord{
		Provider: "codex", ExecutorType: "CodexWebsocketsExecutor",
		Model: flowModel, Alias: flowModel, AuthIndex: "unscoped-auth",
		AuthType: "oauth", Source: "upstream@example.com", Generate: true,
		RequestedAt: now.Add(-time.Minute),
		Detail:      UsageDetail{InputTokens: 1000, TotalTokens: 1000},
	})

	entries := requestEventEntries(t, app)
	if len(entries) != 1 || entries[0].Scope != "" || entries[0].Source != "codex · upstream@example.com" {
		t.Fatalf("entries = %+v", entries)
	}
	if keys := app.store.KeyViews(); len(keys) != 0 {
		t.Fatalf("keys = %+v, want no synthetic API Key", keys)
	}
	analysis, err := app.store.Analysis(billing.RequestEventQuery{From: now.Add(-time.Hour), To: now})
	if err != nil || analysis.Summary.Requests != 1 || len(analysis.UsageDistribution.APIKeys) != 1 ||
		analysis.UsageDistribution.APIKeys[0].Label != "Unassigned" {
		t.Fatalf("analysis = %+v, err = %v", analysis, err)
	}
	assertCostClose(t, analysis.Summary.Cost.TotalUSD, 0.001)
}

func TestUsageHandleDoesNotGuessAmbiguousUnknownAccounting(t *testing.T) {
	app := newAppWithPrice(t, true)
	publishUsageRecord(t, app, UsageRecord{
		Provider: "future-provider", Model: flowModel, Alias: flowModel, APIKey: testAPIKey,
		Generate: true, Detail: UsageDetail{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5},
	})
	cost, requests := requestEventCost(t, app)
	if requests != 1 {
		t.Fatalf("unknown cost = %v, requests = %d", cost, requests)
	}
	assertCostClose(t, cost, 0)
	if entries := requestEventEntries(t, app); len(entries) != 1 || entries[0].AccountingQuality != billing.TokenAccountingUnclassified {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestUsageHandlePersistsBillWithoutPlaintextKeys(t *testing.T) {
	app, statePath := newAppWithPriceAndState(t, true)
	app.rememberRequestOrigin(RequestInterceptRequest{
		RequestID: "intercept-request", TraceID: "privacy-trace",
		Headers: http.Header{"User-Agent": {"dummy-client/1.0"}, "X-Forwarded-For": {"192.0.2.10"}},
	}, flowScope(), "/v1/messages?key=dummy-secret")
	publishUsageRecord(t, app, UsageRecord{
		RequestID: "privacy-execution", TraceID: "privacy-trace",
		SessionID: "session-1", ParentSessionID: "session-parent", AccessProvider: "config-inline", IsNativeKey: new(bool),
		AuthID: "dummy-auth-secret", BaseURL: "https://dummy:secret@upstream.invalid/dummy-token?key=dummy-query#dummy-fragment", Stream: new(bool),
		Provider: "openai-compatible-deepseek", ExecutorType: "OpenAICompatExecutor",
		Model: flowModel, Alias: flowModel, APIKey: testAPIKey,
		AuthIndex: "auth-3", AuthType: "apikey", Source: "sk-upstream-key-0001",
		Generate: true, RequestedAt: app.store.Now(),
		Detail: UsageDetail{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500},
	})
	response := app.listRequestEvents(ManagementRequest{}, viewAccess{APIKey: true, Scope: flowScope(), Tracked: true})
	if bytes.Contains(response.Body, []byte("upstream.invalid")) || bytes.Contains(response.Body, []byte("credential_ref")) {
		t.Fatalf("private metadata exposed: %s", response.Body)
	}
	app.Shutdown()

	database, errOpen := sqlite.Open(statePath, nil)
	if errOpen != nil {
		t.Fatalf("reopen persisted state: %v", errOpen)
	}
	defer database.Close()
	snapshot, errLoad := database.Load(time.Time{}, time.Time{})
	if errLoad != nil {
		t.Fatalf("load persisted state: %v", errLoad)
	}
	if key := snapshot.State.Keys[flowScope()]; key == nil {
		t.Fatalf("persisted key = %+v", key)
	}
	requests, errRequests := database.RequestEvents(billing.RequestEventQuery{Scope: flowScope(), Limit: 10}, time.Time{})
	if errRequests != nil || len(requests.Entries) != 1 || requests.Entries[0].Cost.Total <= 0 {
		t.Fatalf("persisted request events = %+v, err = %v", requests.Entries, errRequests)
	}
	if entry := requests.Entries[0]; entry.Account != "sk-ups…0001" || entry.Source != "deepseek · sk-ups…0001" ||
		entry.RequestOrigin != (billing.RequestOrigin{Endpoint: "/v1/messages", Transport: "http", UserAgent: "dummy-client/1.0", XForwardedFor: "192.0.2.10"}) ||
		entry.Stream == nil || *entry.Stream {
		t.Fatalf("persisted event identity = %+v", entry)
	}

	execution := requests.Entries[0].Execution
	if execution == nil || execution.RequestID != "privacy-execution" || execution.TraceID != "privacy-trace" || execution.BaseURL != "https://upstream.invalid" || execution.CredentialRef != billing.CredentialFingerprint("dummy-auth-secret") || execution.IsNativeKey == nil || execution.SessionID != "session-1" || execution.ParentSessionID != "session-parent" || execution.AccessProvider != "config-inline" {
		t.Fatalf("execution metadata lost: %+v", execution)
	}
	raw, errRead := os.ReadFile(statePath)
	if errRead != nil {
		t.Fatalf("read persisted state: %v", errRead)
	}
	if bytes.Contains(raw, []byte(testAPIKey)) {
		t.Fatal("database contains the plaintext downstream API key")
	}
	if bytes.Contains(raw, []byte("sk-upstream-key-0001")) {
		t.Fatal("database contains the plaintext upstream API key")
	}
	for _, secret := range []string{"dummy-auth-secret", "dummy-token", "dummy-query", "dummy-fragment", "dummy:secret"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("persisted secret %q", secret)
		}
	}
	if bytes.Contains(raw, []byte("dummy-secret")) {
		t.Fatal("database contains endpoint query parameters")
	}
}

func TestUsageHandleSpendDrivesQuotaEnforcement(t *testing.T) {
	app := newAppWithPrice(t, true)
	if _, errSync := app.store.SyncKeys([]string{testAPIKey}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	if _, errCreate := app.store.CreatePlanWithBindings(billing.Plan{
		ID: "p", Name: "Tiny", Windows: []billing.QuotaWindow{{Name: "额度", Amount: 0.0015, PeriodSeconds: 86400}},
	}, []string{flowScope()}); errCreate != nil {
		t.Fatalf("CreatePlanWithBindings error = %v", errCreate)
	}
	admit(t, app, "openai", "/v1/chat/completions")
	billUsage(t, app, 1000, 0, 0, 500, 0)

	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		Model: "gpt-5.5", SourceFormat: "openai", Metadata: flowMetadata(),
	}))
	if errHandle != nil {
		t.Fatalf("request.intercept_before error = %v", errHandle)
	}
	var response RequestInterceptResponse
	decodeResult(t, raw, &response)
	if !response.Terminate || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("response = %+v", response)
	}
}

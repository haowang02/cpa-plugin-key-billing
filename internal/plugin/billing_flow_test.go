package plugin

import (
	"bytes"
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

func admit(t *testing.T, app *App, clientFormat, requestPath string) {
	t.Helper()
	metadata := flowMetadata()
	metadata[MetadataRequestPath] = requestPath
	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		SourceFormat: clientFormat, Model: flowModel, RequestedModel: flowModel, Metadata: metadata,
	}))
	if errHandle != nil {
		t.Fatalf("request.intercept_before error = %v", errHandle)
	}
	var response RequestInterceptResponse
	decodeResult(t, raw, &response)
	if response.Terminate {
		t.Fatalf("request was terminated: %s", response.ResponseBody)
	}
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
			cost += entry.Cost.TotalUSD
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
}

func TestUsageHandleUsesClientKeyModelAliasAndCredential(t *testing.T) {
	app := newAppWithPrice(t, true)
	publishUsageRecord(t, app, UsageRecord{
		Provider: "codex", ExecutorType: "CodexExecutor", Model: flowModel, Alias: "route/gpt-5.5",
		APIKey: testAPIKey, AuthIndex: "auth-7", AuthType: "oauth", Source: "billing@example.com",
		ReasoningEffort: "high", ServiceTier: "priority",
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
		entry.UpstreamModel != flowModel || entry.BillingModel != "route/gpt-5.5" || entry.Failed ||
		entry.Source != "codex · billing@example.com" || entry.AccountingQuality != billing.TokenAccountingComplete ||
		entry.LatencyMS != 1500 || entry.TTFTMS != 250 {
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
	publishUsageRecord(t, app, UsageRecord{
		Provider: "codex", Model: flowModel, Alias: flowModel, APIKey: testAPIKey,
		AuthIndex: "auth-failed", AuthType: "oauth", Source: "failed@example.com",
		Generate: true, Failed: true,
	})
	publishUsageRecord(t, app, UsageRecord{
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
}

func TestUsageHandleSkipsCountOnlyAndDoesNotGuessUnknownProviderAccounting(t *testing.T) {
	app := newAppWithPrice(t, true)
	publishUsageRecord(t, app, UsageRecord{
		Provider: "openai", Model: flowModel, Alias: flowModel, APIKey: testAPIKey,
		Generate: false, Detail: UsageDetail{InputTokens: 1000, TotalTokens: 1000},
	})
	if cost, requests := requestEventCost(t, app); cost != 0 || requests != 0 {
		t.Fatalf("count-only cost = %v, requests = %d", cost, requests)
	}

	publishUsageRecord(t, app, UsageRecord{
		Provider: "future-provider", Model: flowModel, Alias: flowModel, APIKey: testAPIKey,
		Generate: true, Detail: UsageDetail{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
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
	publishUsageRecord(t, app, UsageRecord{
		Provider: "openai-compatible-deepseek", ExecutorType: "OpenAICompatExecutor",
		Model: flowModel, Alias: flowModel, APIKey: testAPIKey,
		AuthIndex: "auth-3", AuthType: "apikey", Source: "sk-upstream-key-0001",
		Generate: true, RequestedAt: app.store.Now(),
		Detail: UsageDetail{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500},
	})
	app.Shutdown()

	database, errOpen := sqlite.Open(statePath)
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
	if errRequests != nil || len(requests.Entries) != 1 || requests.Entries[0].Cost.TotalUSD <= 0 {
		t.Fatalf("persisted request events = %+v, err = %v", requests.Entries, errRequests)
	}
	if credential := snapshot.State.Credentials["auth-3"]; credential.Name() != "deepseek · sk-ups…0001" {
		t.Fatalf("persisted credential = %+v", credential)
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
}

func TestUsageHandleSpendDrivesQuotaEnforcement(t *testing.T) {
	app := newAppWithPrice(t, true)
	if _, errSync := app.store.SyncKeys([]string{testAPIKey}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	if _, errCreate := app.store.CreatePlanWithBindings(billing.Plan{
		ID: "p", Name: "Tiny", AmountUSD: 0.0015, PeriodSeconds: 86400,
	}, []string{flowScope()}); errCreate != nil {
		t.Fatalf("CreatePlanWithBindings error = %v", errCreate)
	}
	admit(t, app, "openai", "/v1/chat/completions")
	billUsage(t, app, 1000, 0, 0, 500, 0)

	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		SourceFormat: "openai", Metadata: flowMetadata(),
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

package plugin

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"testing"

	"cpa-key-billing/internal/billing"
)

const flowRequestID = "req-flow-1"

func flowScope() string { return billing.CallerScope(testAPIKey) }

func flowMetadata() map[string]any {
	return map[string]any{MetadataCallerScope: flowScope()}
}

// admit runs the before-auth interceptor for one downstream call, which is
// where a request is either rejected or registered as billable.
func admit(t *testing.T, app *App, protocol, requestPath string) {
	t.Helper()
	metadata := flowMetadata()
	metadata[MetadataRequestPath] = requestPath
	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		RequestID: flowRequestID, SourceFormat: protocol, Metadata: metadata,
	}))
	if errHandle != nil {
		t.Fatalf("request.intercept_before error = %v", errHandle)
	}
	var resp RequestInterceptResponse
	decodeResult(t, raw, &resp)
	if resp.Terminate {
		t.Fatalf("request was terminated: %s", resp.ResponseBody)
	}
}

// selectCredential runs the after-auth interceptor, which is where the host
// reports the credential it picked for the next upstream attempt.
func selectCredential(t *testing.T, app *App, authIndex string) {
	t.Helper()
	metadata := flowMetadata()
	metadata[MetadataSelectedAuthIndex] = authIndex
	raw, errHandle := app.HandleMethod(MethodRequestInterceptAfter, mustMarshal(t, RequestInterceptRequest{
		RequestID: flowRequestID, ToFormat: "codex", Metadata: metadata,
	}))
	if errHandle != nil {
		t.Fatalf("request.intercept_after error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
}

// publishUsage delivers the usage record CLIProxyAPI emits for a served
// request, which is where the plugin learns what a credential is.
func publishUsage(t *testing.T, app *App, authIndex, provider, authType, source string) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodUsageHandle, mustMarshal(t, UsageRecord{
		Provider: provider, AuthIndex: authIndex, AuthType: authType, Source: source,
	}))
	if errHandle != nil {
		t.Fatalf("usage.handle error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
}

func canonicalRecord(model string, uncached, cacheRead, cacheWrite, output, reasoning int64) RequestUsageRecord {
	return RequestUsageRecord{
		Model: model, RequestedModel: model, Generate: true,
		Breakdown: RequestTokenBreakdown{
			SchemaVersion: billing.TokenAccountingSchemaVersion,
			Quality:       billing.TokenAccountingComplete,
			TotalTokens:   uncached + cacheRead + cacheWrite + output,
			Input: RequestTokenInputBreakdown{
				TotalTokens:      uncached + cacheRead + cacheWrite,
				UncachedTokens:   uncached,
				CacheReadTokens:  cacheRead,
				CacheWriteTokens: cacheWrite,
			},
			Output: RequestTokenOutputBreakdown{
				TotalTokens:        output,
				NonReasoningTokens: output - reasoning,
				ReasoningTokens:    reasoning,
			},
		},
	}
}

func complete(t *testing.T, app *App, outcome RequestCompletionOutcome, records ...RequestUsageRecord) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodRequestComplete, mustMarshal(t, RequestCompletion{
		RequestID: flowRequestID, Outcome: outcome, UsageRecords: records,
	}))
	if errHandle != nil {
		t.Fatalf("request.complete error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
}

func lifetimeCost(t *testing.T, app *App) (float64, int64) {
	t.Helper()
	var cost float64
	var requests int64
	app.store.Read(func(state *billing.State) {
		if key := state.Keys[flowScope()]; key != nil {
			cost = key.Lifetime.CostUSD
			requests = key.Lifetime.Requests
		}
	})
	return cost, requests
}

func assertCostClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("cost = %.12f, want %.12f", got, want)
	}
}

func TestFlowBillsCanonicalCompletionWithoutResponseInterceptors(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "claude", "/v1/messages")
	if cost, _ := lifetimeCost(t, app); cost != 0 {
		t.Fatalf("cost before completion = %v", cost)
	}
	complete(t, app, RequestCompletionSucceeded, canonicalRecord("gpt-5.5", 500, 400, 100, 500, 200))
	cost, requests := lifetimeCost(t, app)
	assertCostClose(t, cost, 0.0005+0.00004+0.000125+0.001)
	if requests != 1 {
		t.Fatalf("Requests = %d, want 1", requests)
	}
}

func TestFlowBillsEveryUpstreamRetryRecord(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", "/v1/chat/completions")
	record := canonicalRecord("gpt-5.5", 1000, 0, 0, 500, 0)
	complete(t, app, RequestCompletionSucceeded, record, record)
	cost, requests := lifetimeCost(t, app)
	assertCostClose(t, cost, 2*(0.001+0.001))
	if requests != 2 {
		t.Fatalf("Requests = %d, want two provider usage records", requests)
	}
}

func TestFlowRecordsEndpointSourceAndCanonicalQuality(t *testing.T) {
	app := newAppWithPrice(t, true)
	publishUsage(t, app, "auth-7", "codex", "oauth", "billing@example.com")
	admit(t, app, "claude", "/v1/messages")
	selectCredential(t, app, "auth-7")
	complete(t, app, RequestCompletionSucceeded, canonicalRecord("gpt-5.5", 500, 400, 100, 500, 200))
	var view billing.LogView
	callOK(t, app, http.MethodGet, routeLogs, nil, nil, http.StatusOK, &view)
	if len(view.Entries) != 1 {
		t.Fatalf("Entries = %+v", view.Entries)
	}
	entry := view.Entries[0]
	if entry.RequestID != flowRequestID || entry.Endpoint != "/v1/messages" || entry.Source != "codex · billing@example.com" ||
		entry.AccountingQuality != billing.TokenAccountingComplete {
		t.Fatalf("entry = %+v", entry)
	}
}

// A retried request is attributed to the credential that answered it, which is
// the last one the host selected before the terminal event.
func TestFlowAttributesRetriedRequestToTheServingCredential(t *testing.T) {
	app := newAppWithPrice(t, true)
	publishUsage(t, app, "auth-exhausted", "codex", "oauth", "spent@example.com")
	publishUsage(t, app, "auth-healthy", "codex", "oauth", "live@example.com")
	admit(t, app, "claude", "/v1/messages")
	selectCredential(t, app, "auth-exhausted")
	selectCredential(t, app, "auth-healthy")
	complete(t, app, RequestCompletionSucceeded, canonicalRecord("gpt-5.5", 1000, 0, 0, 500, 0))
	if entries := app.store.Logs(0).Entries; len(entries) != 1 || entries[0].Source != "codex · live@example.com" {
		t.Fatalf("entries = %+v", entries)
	}
}

// Usage records arrive on the host's own schedule, so a credential can be
// learned after the request it served was billed. The log resolves its name on
// read, which is what lets that entry catch up.
func TestFlowNamesACredentialLearnedAfterTheBill(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "claude", "/v1/messages")
	selectCredential(t, app, "auth-7")
	complete(t, app, RequestCompletionSucceeded, canonicalRecord("gpt-5.5", 1000, 0, 0, 500, 0))
	if entries := app.store.Logs(0).Entries; len(entries) != 1 || entries[0].Source != "" {
		t.Fatalf("entries = %+v, want an unnamed credential", entries)
	}

	publishUsage(t, app, "auth-7", "openai-compatible-deepseek", "apikey", "sk-upstream-key-0001")
	if entries := app.store.Logs(0).Entries; entries[0].Source != "deepseek · sk-ups…0001" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestFlowRejectsOrMissingCanonicalUsageDoesNotBill(t *testing.T) {
	for _, outcome := range []RequestCompletionOutcome{RequestCompletionRejected, RequestCompletionSucceeded} {
		t.Run(string(outcome), func(t *testing.T) {
			app := newAppWithPrice(t, true)
			admit(t, app, "openai", "/v1/chat/completions")
			complete(t, app, outcome)
			if cost, requests := lifetimeCost(t, app); cost != 0 || requests != 0 {
				t.Fatalf("cost = %v, requests = %d", cost, requests)
			}
		})
	}
}

func TestFlowCanceledRequestBillsReportedUsageAndMarksFailure(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", "/v1/chat/completions")
	complete(t, app, RequestCompletionOutcome("canceled"), canonicalRecord("gpt-5.5", 1000, 0, 0, 250, 0))
	cost, _ := lifetimeCost(t, app)
	assertCostClose(t, cost, 0.001+0.0005)
	app.store.Read(func(state *billing.State) {
		if state.Keys[flowScope()].Lifetime.FailedRequests != 1 {
			t.Fatal("canceled usage was not marked failed")
		}
	})
}

func TestFlowUnclassifiedUsageIsVisibleButCostsZero(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", "/v1/chat/completions")
	record := canonicalRecord("gpt-5.5", 0, 0, 0, 0, 0)
	record.Breakdown = RequestTokenBreakdown{
		SchemaVersion:      billing.TokenAccountingSchemaVersion,
		Quality:            billing.TokenAccountingUnclassified,
		TotalTokens:        100,
		UnclassifiedTokens: 100,
	}
	complete(t, app, RequestCompletionSucceeded, record)
	if cost, requests := lifetimeCost(t, app); cost != 0 || requests != 1 {
		t.Fatalf("cost = %v, requests = %d", cost, requests)
	}
	status := app.store.Status(PluginName, Version)
	if status.Counters.UsageUnclassified != 1 {
		t.Fatalf("counters = %+v", status.Counters)
	}
}

func TestFlowTerminalEventPersistsCanonicalBillWithoutPlaintextKey(t *testing.T) {
	app := newAppWithPrice(t, true)
	publishUsage(t, app, "auth-3", "codex", "apikey", "sk-upstream-key-0001")
	admit(t, app, "openai", "/v1/chat/completions")
	selectCredential(t, app, "auth-3")
	complete(t, app, RequestCompletionSucceeded, canonicalRecord("gpt-5.5", 1000, 0, 0, 500, 0))
	// Writes are debounced, so ask for the one this test reads back.
	if errFlush := app.store.Flush(); errFlush != nil {
		t.Fatalf("Flush error = %v", errFlush)
	}
	raw, errRead := os.ReadFile(app.store.Status(PluginName, Version).StateFile)
	if errRead != nil {
		t.Fatalf("read persisted state: %v", errRead)
	}
	var persisted billing.State
	if errUnmarshal := json.Unmarshal(raw, &persisted); errUnmarshal != nil {
		t.Fatalf("decode state: %v", errUnmarshal)
	}
	if key := persisted.Keys[flowScope()]; key == nil || key.Lifetime.CostUSD <= 0 {
		t.Fatalf("persisted key = %+v", key)
	}
	if bytes.Contains(raw, []byte(testAPIKey)) {
		t.Fatalf("state contains plaintext API key: %s", raw)
	}
	if bytes.Contains(raw, []byte("sk-upstream-key-0001")) {
		t.Fatalf("state contains the plaintext upstream key: %s", raw)
	}
	if credential := persisted.Credentials["auth-3"]; credential.Name() != "codex · sk-ups…0001" {
		t.Fatalf("persisted credential = %+v", credential)
	}
}

func TestFlowEnforcementUsesCanonicalSpend(t *testing.T) {
	app := newAppWithPrice(t, true)
	app.store.Update(func(state *billing.State) {
		state.Plans = []billing.Plan{{ID: "p", Name: "Tiny", AmountUSD: 0.0015, Period: billing.Period{Kind: billing.PeriodDaily}}}
		state.Keys[flowScope()] = &billing.KeyState{PlanID: "p"}
	})
	admit(t, app, "openai", "/v1/chat/completions")
	complete(t, app, RequestCompletionSucceeded, canonicalRecord("gpt-5.5", 1000, 0, 0, 500, 0))
	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		RequestID: "req-flow-2", SourceFormat: "openai", Metadata: flowMetadata(),
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

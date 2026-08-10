package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"testing"

	"cpa-key-billing/internal/billing"
)

const (
	flowRequestID  = "req-flow-1"
	flowResponseID = "resp-flow-1"
	flowModel      = "gpt-5.5"
)

var flowRequestBody = []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`)

func flowScope() string { return billing.CallerScope(testAPIKey) }

func flowMetadata() map[string]any {
	return map[string]any{MetadataCallerScope: flowScope()}
}

func admit(t *testing.T, app *App, clientFormat, requestPath string) {
	t.Helper()
	metadata := flowMetadata()
	metadata[MetadataRequestPath] = requestPath
	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		RequestID: flowRequestID, SourceFormat: clientFormat, Model: flowModel, RequestedModel: flowModel,
		Body: flowRequestBody, Metadata: metadata,
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

func selectCredential(t *testing.T, app *App, authIndex string) {
	t.Helper()
	metadata := flowMetadata()
	metadata[MetadataSelectedAuthIndex] = authIndex
	raw, errHandle := app.HandleMethod(MethodRequestInterceptAfter, mustMarshal(t, RequestInterceptRequest{
		RequestID: flowRequestID, ToFormat: "openai", Model: flowModel, RequestedModel: flowModel,
		Body: flowRequestBody, Metadata: metadata,
	}))
	if errHandle != nil {
		t.Fatalf("request.intercept_after error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
}

func observeUpstream(t *testing.T, app *App, upstreamFormat, model string, stream bool, requestBody, responseBody []byte) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodResponseNormalizeBefore, mustMarshal(t, ResponseTransformRequest{
		FromFormat: upstreamFormat, Model: model, Stream: stream, OriginalRequest: requestBody, Body: responseBody,
	}))
	if errHandle != nil {
		t.Fatalf("response.normalize_before error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
}

func respond(t *testing.T, app *App, requestID string, body []byte) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodResponseInterceptAfter, mustMarshal(t, ResponseInterceptRequest{
		RequestID: requestID, Body: body,
	}))
	if errHandle != nil {
		t.Fatalf("response.intercept_after error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
}

func streamChunk(t *testing.T, app *App, requestID string, index int, body []byte) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodResponseStreamChunk, mustMarshal(t, ResponseInterceptRequest{
		RequestID: requestID, ChunkIndex: index, Body: body,
	}))
	if errHandle != nil {
		t.Fatalf("response.intercept_stream_chunk error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
}

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

func billUsage(t *testing.T, app *App, uncached, cacheRead, cacheWrite, output, reasoning int64) {
	t.Helper()
	usage := fmt.Sprintf(
		`{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d,"cache_creation_tokens":%d},"completion_tokens_details":{"reasoning_tokens":%d}}`,
		uncached+cacheRead+cacheWrite, output, uncached+cacheRead+cacheWrite+output, cacheRead, cacheWrite, reasoning)
	observeUpstream(t, app, "openai", flowModel, false, flowRequestBody,
		[]byte(`{"id":"`+flowResponseID+`","usage":`+usage+`}`))
	respond(t, app, flowRequestID, []byte(`{"id":"`+flowResponseID+`"}`))
}

func complete(t *testing.T, app *App, requestID string, outcome RequestCompletionOutcome) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodRequestComplete, mustMarshal(t, RequestCompletion{
		RequestID: requestID, Outcome: outcome,
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

func TestFlowBillsUpstreamUsageAtTheTerminalEvent(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "claude", "/v1/messages")
	billUsage(t, app, 500, 400, 100, 500, 200)
	if cost, _ := lifetimeCost(t, app); cost != 0 {
		t.Fatalf("cost before completion = %v", cost)
	}
	complete(t, app, flowRequestID, RequestCompletionSucceeded)
	cost, requests := lifetimeCost(t, app)
	assertCostClose(t, cost, 0.0005+0.00004+0.000125+0.001)
	if requests != 1 {
		t.Fatalf("Requests = %d, want 1", requests)
	}
}

func TestFlowRecordsEndpointSourceAndAccountingQuality(t *testing.T) {
	app := newAppWithPrice(t, true)
	publishUsage(t, app, "auth-7", "codex", "oauth", "billing@example.com")
	admit(t, app, "claude", "/v1/messages")
	selectCredential(t, app, "auth-7")
	billUsage(t, app, 500, 400, 100, 500, 200)
	complete(t, app, flowRequestID, RequestCompletionSucceeded)
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

func TestFlowBillsARetriedRequestOnceAgainstTheServingCredential(t *testing.T) {
	app := newAppWithPrice(t, true)
	publishUsage(t, app, "auth-exhausted", "codex", "oauth", "spent@example.com")
	publishUsage(t, app, "auth-healthy", "codex", "oauth", "live@example.com")
	admit(t, app, "claude", "/v1/messages")
	selectCredential(t, app, "auth-exhausted")
	selectCredential(t, app, "auth-healthy")
	billUsage(t, app, 1000, 0, 0, 500, 0)
	complete(t, app, flowRequestID, RequestCompletionSucceeded)
	cost, requests := lifetimeCost(t, app)
	assertCostClose(t, cost, 0.001+0.001)
	if requests != 1 {
		t.Fatalf("Requests = %d, want a single bill for the retried request", requests)
	}
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
	billUsage(t, app, 1000, 0, 0, 500, 0)
	complete(t, app, flowRequestID, RequestCompletionSucceeded)
	if entries := app.store.Logs(0).Entries; len(entries) != 1 || entries[0].Source != "" {
		t.Fatalf("entries = %+v, want an unnamed credential", entries)
	}

	publishUsage(t, app, "auth-7", "openai-compatible-deepseek", "apikey", "sk-upstream-key-0001")
	if entries := app.store.Logs(0).Entries; entries[0].Source != "deepseek · sk-ups…0001" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestFlowRejectedRequestLeavesNoTrace(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", "/v1/chat/completions")
	complete(t, app, flowRequestID, RequestCompletionRejected)
	if cost, requests := lifetimeCost(t, app); cost != 0 || requests != 0 {
		t.Fatalf("cost = %v, requests = %d", cost, requests)
	}
	if entries := app.store.Logs(0).Entries; len(entries) != 0 {
		t.Fatalf("entries = %+v, want none", entries)
	}
}

func TestFlowUnmeasuredRequestIsLoggedAtZeroCost(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", "/v1/chat/completions")
	complete(t, app, flowRequestID, RequestCompletionSucceeded)
	if cost, _ := lifetimeCost(t, app); cost != 0 {
		t.Fatalf("cost = %v, want nothing charged", cost)
	}
	entries := app.store.Logs(0).Entries
	if len(entries) != 1 || entries[0].AccountingQuality != "" || entries[0].Cost.TotalUSD != 0 {
		t.Fatalf("entries = %+v, want one unmeasured zero-cost row", entries)
	}
	status := app.store.Status()
	if status.Counters.UsageNoTokens != 1 || status.Counters.UsageUnclassified != 0 {
		t.Fatalf("counters = %+v", status.Counters)
	}
}

func TestFlowCanceledRequestBillsReportedUsageAndSaysSo(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", "/v1/chat/completions")
	billUsage(t, app, 1000, 0, 0, 250, 0)
	complete(t, app, flowRequestID, RequestCompletionCanceled)
	cost, _ := lifetimeCost(t, app)
	assertCostClose(t, cost, 0.001+0.0005)
	if entries := app.store.Logs(0).Entries; len(entries) != 1 || entries[0].Outcome != billing.OutcomeCanceled {
		t.Fatalf("entries = %+v, want one canceled row", entries)
	}
}

func TestFlowCanceledRequestWithoutUsageIsStillLogged(t *testing.T) {
	app := newAppWithPrice(t, true)
	publishUsage(t, app, "auth-7", "codex", "oauth", "billing@example.com")
	admit(t, app, "claude", "/v1/messages")
	selectCredential(t, app, "auth-7")
	complete(t, app, flowRequestID, RequestCompletionCanceled)

	entries := app.store.Logs(0).Entries
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want the canceled request logged", entries)
	}
	entry := entries[0]
	if entry.Outcome != billing.OutcomeCanceled || entry.Cost.TotalUSD != 0 || entry.AccountingQuality != "" {
		t.Fatalf("entry = %+v, want a canceled zero-cost row with no token detail", entry)
	}
	if entry.Endpoint != "/v1/messages" || entry.Source != "codex · billing@example.com" || entry.BillingModel != flowModel {
		t.Fatalf("entry = %+v", entry)
	}
	app.store.Read(func(state *billing.State) {
		if key := state.Keys[flowScope()]; key.Lifetime.Requests != 1 || key.Lifetime.CostUSD != 0 {
			t.Fatalf("lifetime = %+v", key.Lifetime)
		}
	})
}

func TestFlowUnclassifiedUsageIsVisibleButCostsZero(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", "/v1/chat/completions")
	observeUpstream(t, app, "acme", flowModel, false, flowRequestBody,
		[]byte(`{"id":"`+flowResponseID+`","usage":{"total_tokens":100}}`))
	respond(t, app, flowRequestID, []byte(`{"id":"`+flowResponseID+`"}`))
	complete(t, app, flowRequestID, RequestCompletionSucceeded)
	if cost, requests := lifetimeCost(t, app); cost != 0 || requests != 1 {
		t.Fatalf("cost = %v, requests = %d", cost, requests)
	}
	status := app.store.Status()
	if status.Counters.UsageUnclassified != 1 {
		t.Fatalf("counters = %+v", status.Counters)
	}
}

func TestFlowTerminalEventPersistsTheBillWithoutPlaintextKeys(t *testing.T) {
	app, statePath := newAppWithPriceAndState(t, true)
	publishUsage(t, app, "auth-3", "codex", "apikey", "sk-upstream-key-0001")
	admit(t, app, "openai", "/v1/chat/completions")
	selectCredential(t, app, "auth-3")
	billUsage(t, app, 1000, 0, 0, 500, 0)
	complete(t, app, flowRequestID, RequestCompletionSucceeded)
	if errFlush := app.store.Flush(); errFlush != nil {
		t.Fatalf("Flush error = %v", errFlush)
	}
	raw, errRead := os.ReadFile(statePath)
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

func TestFlowEnforcementUsesRecordedSpend(t *testing.T) {
	app := newAppWithPrice(t, true)
	app.store.Update(func(state *billing.State) {
		state.Plans = []billing.Plan{{ID: "p", Name: "Tiny", AmountUSD: 0.0015, Period: billing.Period{Kind: billing.PeriodDaily}}}
		state.Keys[flowScope()] = &billing.KeyState{PlanID: "p"}
	})
	admit(t, app, "openai", "/v1/chat/completions")
	billUsage(t, app, 1000, 0, 0, 500, 0)
	complete(t, app, flowRequestID, RequestCompletionSucceeded)
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

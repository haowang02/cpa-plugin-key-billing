package plugin

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"testing"

	"cpa-key-billing/internal/billing"
)

// The billing flow spans four host calls that share one RequestID:
// intercept_before admits and attributes, the response or stream interceptors
// observe tokens, and request.complete commits. These tests drive that whole
// sequence through the RPC boundary.

const flowRequestID = "req-flow-1"

func flowScope() string { return billing.CallerScope(testAPIKey) }

func flowMetadata() map[string]any {
	return map[string]any{MetadataCallerScope: flowScope()}
}

func admit(t *testing.T, app *App, _ string, _ bool) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		RequestID:      flowRequestID,
		SourceFormat:   "openai",
		Model:          "gpt-5.5",
		RequestedModel: "gpt-5.5",
		Metadata:       flowMetadata(),
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

func observeResponse(t *testing.T, app *App, _ string, body []byte) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodResponseInterceptAfter, mustMarshal(t, ResponseInterceptRequest{
		RequestID: flowRequestID,
		Body:      body,
	}))
	if errHandle != nil {
		t.Fatalf("response.intercept_after error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
}

func observeChunk(t *testing.T, app *App, _ string, index int, body []byte) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodResponseInterceptStreamChunk, mustMarshal(t, StreamChunkInterceptRequest{
		RequestID:  flowRequestID,
		Body:       body,
		ChunkIndex: index,
	}))
	if errHandle != nil {
		t.Fatalf("response.intercept_stream_chunk error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
}

func complete(t *testing.T, app *App, outcome RequestCompletionOutcome) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodRequestComplete, mustMarshal(t, RequestCompletion{
		RequestID: flowRequestID,
		Outcome:   outcome,
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
		key := state.Keys[flowScope()]
		if key == nil {
			return
		}
		cost = key.Lifetime.CostUSD
		requests = key.Lifetime.Requests
	})
	return cost, requests
}

func assertCostClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("cost = %.12f, want %.12f", got, want)
	}
}

func TestFlowBillsNonStreamingResponse(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", false)
	observeResponse(t, app, "openai", []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500,"prompt_tokens_details":{"cached_tokens":400,"cached_creation_tokens":100},"completion_tokens_details":{"reasoning_tokens":200}}}`))

	// Nothing may be billed before the terminal event.
	if cost, _ := lifetimeCost(t, app); cost != 0 {
		t.Fatalf("cost = %v before request.complete, want 0", cost)
	}

	complete(t, app, RequestCompletionSucceeded)

	cost, requests := lifetimeCost(t, app)
	// 500 uncached input, 400 cache read, 100 cache write, 500 output.
	assertCostClose(t, cost, 0.0005+0.00004+0.000125+0.001)
	if requests != 1 {
		t.Fatalf("Requests = %d, want 1", requests)
	}
}

// TestFlowBillsOnceDespiteUpstreamRetries is a property the host's own usage
// records do not have: they fire per upstream attempt, while the terminal event
// fires once per client request.
func TestFlowBillsOnceDespiteUpstreamRetries(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", false)
	// Two observations for the same request, as a retried execution would produce.
	observeResponse(t, app, "openai", []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	observeResponse(t, app, "openai", []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	complete(t, app, RequestCompletionSucceeded)

	cost, requests := lifetimeCost(t, app)
	assertCostClose(t, cost, 0.001+0.001)
	if requests != 1 {
		t.Fatalf("Requests = %d, want a single billed request", requests)
	}
}

// TestFlowRecordsTheRequestInTheBillingLog drives the log the way the admin page
// does: bill through the five host calls, then read it back over the management
// route. It is the only view in which a single request survives, so it is worth
// checking end to end rather than only in the store.
func TestFlowRecordsTheRequestInTheBillingLog(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", false)
	observeResponse(t, app, "openai", []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":500,"prompt_tokens_details":{"cached_tokens":400,"cached_creation_tokens":100},"completion_tokens_details":{"reasoning_tokens":200}}}`))

	// Nothing is logged until the terminal event commits, same as the bill.
	var before billing.LogView
	callOK(t, app, http.MethodGet, routeLogs, nil, nil, http.StatusOK, &before)
	if len(before.Entries) != 0 {
		t.Fatalf("Entries = %+v before request.complete, want none", before.Entries)
	}

	complete(t, app, RequestCompletionSucceeded)

	var view billing.LogView
	callOK(t, app, http.MethodGet, routeLogs, nil, nil, http.StatusOK, &view)
	if len(view.Entries) != 1 {
		t.Fatalf("Entries = %d, want 1", len(view.Entries))
	}
	entry := view.Entries[0]
	if entry.Scope != flowScope() || entry.Model != "gpt-5.5" {
		t.Fatalf("entry = %+v", entry)
	}
	// The buckets the bill was computed from: 500 uncached input, 400 cache
	// read, 100 cache write, 500 output, with 200 of the output being reasoning.
	if entry.Cost.UncachedInputTokens != 500 || entry.Cost.CacheReadTokens != 400 ||
		entry.Cost.CacheWriteTokens != 100 || entry.Cost.BilledOutputTokens != 500 ||
		entry.ReasoningTokens != 200 {
		t.Fatalf("Cost = %+v, reasoning = %d", entry.Cost, entry.ReasoningTokens)
	}
	if diff := entry.Cost.TotalUSD - (0.0005 + 0.00004 + 0.000125 + 0.001); diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("TotalUSD = %.12f", entry.Cost.TotalUSD)
	}
	if view.Limit != billing.DefaultLogEntries {
		t.Fatalf("Limit = %d, want the default retention %d", view.Limit, billing.DefaultLogEntries)
	}
}

// TestFlowRejectedRequestIsNotBilled covers this plugin's own quota rejection:
// the request never reached an upstream, so it must not be counted.
func TestFlowRejectedRequestIsNotBilled(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", false)
	observeResponse(t, app, "openai", []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	complete(t, app, RequestCompletionRejected)

	app.store.Read(func(state *billing.State) {
		if len(state.Keys) != 0 {
			t.Fatalf("Keys = %+v, want a rejected request to leave no trace", state.Keys)
		}
	})
}

func TestFlowCanceledRequestBillsWhatWasServed(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", true)
	observeChunk(t, app, "openai", 0, []byte(`data: {"usage":{"prompt_tokens":1000,"completion_tokens":250}}`+"\n"))
	complete(t, app, RequestCompletionOutcome("canceled"))

	cost, _ := lifetimeCost(t, app)
	// A disconnect still consumed upstream tokens, so it is billed and marked failed.
	assertCostClose(t, cost, 0.001+0.0005)
	app.store.Read(func(state *billing.State) {
		if state.Keys[flowScope()].Lifetime.FailedRequests != 1 {
			t.Fatal("a canceled request was not marked failed")
		}
	})
}

// TestFlowBillsACodexUpstreamServedAsMessages is the regression test for a
// production overcharge. A client on /v1/messages served by a Codex (OpenAI
// Responses) upstream was billed 3x, because the plugin chose how to read the
// counters from the protocol the body was announced as rather than from the
// body. Read as Anthropic, a Responses block charges its cache-inclusive
// input_tokens at the full input rate and never applies the cache discount.
//
// Both shapes are exercised under the same "claude" label, and both must
// produce the one true bill: 2772 uncached + 20000 cache read + 500 output.
func TestFlowBillsACodexUpstreamServedAsMessages(t *testing.T) {
	// Prices are the shipped catalog's for gpt-5.3-codex.
	const (
		priceIn     = 1.75
		priceOut    = 14.0
		priceCacheR = 0.175
	)
	want := (2772*priceIn + 20000*priceCacheR + 500*priceOut) / 1e6

	bodies := map[string][]byte{
		// What CPA's codex->claude translator emits downstream: Anthropic shape,
		// input_tokens already cache-exclusive.
		"translated to the anthropic shape": []byte("event: message_delta\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":2772,"output_tokens":500,"cache_read_input_tokens":20000}}` + "\n\n"),
		// The upstream Responses shape, where input_tokens includes the cache.
		"raw openai responses shape": []byte("data: " +
			`{"type":"response.completed","response":{"usage":{"input_tokens":22772,"input_tokens_details":{"cached_tokens":20000},"output_tokens":500,"output_tokens_details":{"reasoning_tokens":200}}}}` + "\n\n"),
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			app := newConfiguredApp(t)
			cacheRead := priceCacheR
			app.store.Update(func(state *billing.State) {
				state.Prices = []billing.PriceRule{{
					Pattern:        "gpt-5.3-codex",
					InputPer1M:     priceIn,
					OutputPer1M:    priceOut,
					CacheReadPer1M: &cacheRead,
				}}
			})

			// The client speaks Claude; the upstream model is a Codex one.
			if _, errAdmit := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
				RequestID: flowRequestID, SourceFormat: "claude", Model: "gpt-5.3-codex",
				RequestedModel: "gpt-5.3-codex", Metadata: flowMetadata(),
			})); errAdmit != nil {
				t.Fatalf("request.intercept_before error = %v", errAdmit)
			}
			observeChunk(t, app, "claude", 0, body)
			complete(t, app, RequestCompletionSucceeded)

			got, _ := lifetimeCost(t, app)
			assertCostClose(t, got, want)
		})
	}
}

// TestFlowIgnoresTheEstimatedInputFrameOnACodexUpstream is the regression test
// for a production overcharge on /v1/messages served by Codex.
//
// When CLIProxyAPI serves a Claude client from a non-Claude upstream it fills
// the message_start frame in with an input count of its own, tokenized locally
// from the request body, because the upstream reports nothing until the stream
// ends and a client with an empty context meter looks broken. That estimate
// counts the whole prompt, cache included, and names no cache bucket. The real
// counters arrive in message_delta in Anthropic's layout, where input_tokens
// excludes the cache.
//
// Merging the two by taking the larger input therefore charged the cached
// tokens twice: once inside the estimate, at the full input rate, and once as a
// cache read. The estimate here deliberately does not match the true total, so
// the assertion only passes if the frame that splits the input is the one that
// decides it.
func TestFlowIgnoresTheEstimatedInputFrameOnACodexUpstream(t *testing.T) {
	// Prices are the shipped catalog's for gpt-5.3-codex.
	const (
		priceIn     = 1.75
		priceOut    = 14.0
		priceCacheR = 0.175
	)
	// The upstream reported 22772 input tokens of which 20000 were cached.
	want := (2772*priceIn + 20000*priceCacheR + 500*priceOut) / 1e6

	app := newConfiguredApp(t)
	cacheRead := priceCacheR
	app.store.Update(func(state *billing.State) {
		state.Prices = []billing.PriceRule{{
			Pattern:        "gpt-5.3-codex",
			InputPer1M:     priceIn,
			OutputPer1M:    priceOut,
			CacheReadPer1M: &cacheRead,
		}}
	})

	if _, errAdmit := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		RequestID: flowRequestID, SourceFormat: "claude", Model: "gpt-5.3-codex",
		RequestedModel: "gpt-5.3-codex", Metadata: flowMetadata(),
	})); errAdmit != nil {
		t.Fatalf("request.intercept_before error = %v", errAdmit)
	}

	// message_start, with the proxy's own estimate of the whole prompt in place
	// of the zero the translator emits. 23500 overshoots the true 22772.
	observeChunk(t, app, "claude", 0, []byte("event: message_start\n"+
		`data: {"type":"message_start","message":{"id":"resp_1","type":"message","role":"assistant","model":"gpt-5.3-codex","usage":{"input_tokens":23500,"output_tokens":0},"content":[],"stop_reason":null}}`+"\n\n"))
	observeChunk(t, app, "claude", 1, []byte("event: content_block_delta\n"+
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`+"\n\n"))
	// message_delta, carrying what the upstream actually reported.
	observeChunk(t, app, "claude", 2, []byte("event: message_delta\n"+
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":2772,"output_tokens":500,"cache_read_input_tokens":20000}}`+"\n\n"+
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	complete(t, app, RequestCompletionSucceeded)

	got, _ := lifetimeCost(t, app)
	assertCostClose(t, got, want)

	app.store.Read(func(state *billing.State) {
		key := state.Keys[flowScope()]
		if key == nil {
			t.Fatal("no entry for the billed request")
		}
		if key.Lifetime.UncachedInputTokens != 2772 || key.Lifetime.CacheReadTokens != 20000 {
			t.Fatalf("Lifetime = %+v, want 2772 uncached input and 20000 cache reads", key.Lifetime)
		}
	})
}

// TestFlowKeepsTheIdentifiedLayoutAcrossChunks covers the merge that spans host
// calls. Anthropic splits its counters over two frames and only the first names
// the layout, so the bare closing frame must not be allowed to reinterpret what
// the opening one established — doing so would clamp the cache buckets into the
// cache-exclusive input total and undercharge the request.
func TestFlowKeepsTheIdentifiedLayoutAcrossChunks(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "claude", true)

	// message_start names the layout: input_tokens excludes the cache buckets.
	observeChunk(t, app, "claude", 0, []byte(
		`data: {"type":"message_start","message":{"usage":{"input_tokens":1000,"cache_read_input_tokens":400,"cache_creation_input_tokens":100,"output_tokens":1}}}`+"\n\n"))
	// message_delta carries nothing but the final output count.
	observeChunk(t, app, "claude", 1, []byte(
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":500}}`+"\n\n"))
	complete(t, app, RequestCompletionSucceeded)

	// 1000 uncached input on top of the cache buckets, at the fixture's prices.
	cost, _ := lifetimeCost(t, app)
	assertCostClose(t, cost, 0.001+0.00004+0.000125+0.001)
}

// TestFlowTerminalEventPersistsTheBill covers the persistence schedule that
// replaced the background flusher. The host call that commits a bill is also
// what writes it out, so nothing needs a timer to reach the disk.
func TestFlowTerminalEventPersistsTheBill(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", false)
	observeResponse(t, app, "openai", []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	complete(t, app, RequestCompletionSucceeded)

	// Deliberately no explicit Flush: request.complete must have done it.
	raw, errRead := os.ReadFile(app.store.Status(PluginName, Version).StateFile)
	if errRead != nil {
		t.Fatalf("request.complete did not persist the state document: %v", errRead)
	}
	var persisted billing.State
	if errUnmarshal := json.Unmarshal(raw, &persisted); errUnmarshal != nil {
		t.Fatalf("decode: %v", errUnmarshal)
	}
	key := persisted.Keys[flowScope()]
	if key == nil || key.Lifetime.Requests != 1 || key.Lifetime.CostUSD <= 0 {
		t.Fatalf("persisted key = %+v, want the committed bill on disk", key)
	}
}

// TestFlowNeverPersistsPlaintextKeys is the privacy guarantee: only the scope
// hash and a masked preview may reach the state document.
func TestFlowNeverPersistsPlaintextKeys(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", false)
	observeResponse(t, app, "openai", []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	complete(t, app, RequestCompletionSucceeded)

	if errFlush := app.store.Flush(); errFlush != nil {
		t.Fatalf("Flush error = %v", errFlush)
	}
	raw, errRead := os.ReadFile(app.store.Status(PluginName, Version).StateFile)
	if errRead != nil {
		t.Fatalf("read state: %v", errRead)
	}
	if containsString(raw, testAPIKey) {
		t.Fatalf("state file contains the plaintext API key:\n%s", raw)
	}
}

// TestFlowEnforcementUsesBilledSpend closes the loop: what the response
// interceptors accumulate is what the next request is checked against.
func TestFlowEnforcementUsesBilledSpend(t *testing.T) {
	app := newAppWithPrice(t, true)
	app.store.Update(func(state *billing.State) {
		state.Plans = []billing.Plan{{ID: "p", Name: "Tiny", AmountUSD: 0.0015, Period: billing.Period{Kind: billing.PeriodDaily}}}
		state.Keys[flowScope()] = &billing.KeyState{Scope: flowScope(), PlanID: "p"}
	})

	admit(t, app, "openai", false)
	// 1000 input + 500 output at 1.00/2.00 per 1M is 0.002, over the 0.0015 limit.
	observeResponse(t, app, "openai", []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	complete(t, app, RequestCompletionSucceeded)

	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		RequestID:    "req-flow-2",
		SourceFormat: "openai",
		Model:        "gpt-5.5",
		Metadata:     flowMetadata(),
	}))
	if errHandle != nil {
		t.Fatalf("request.intercept_before error = %v", errHandle)
	}
	var resp RequestInterceptResponse
	decodeResult(t, raw, &resp)
	if !resp.Terminate {
		t.Fatal("the next request was admitted after the budget was spent")
	}
	if resp.StatusCode != QuotaExhaustedStatus {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, QuotaExhaustedStatus)
	}
}

// TestFlowPricesByUpstreamModelName covers the aliasing case: a client asks for
// "gpt-5.5" while the credential routes to "fake-model". Both names must be
// usable in a price rule, because an operator may think in either.
func TestFlowPricesByUpstreamModelName(t *testing.T) {
	app := newAppWithPrice(t, true)
	app.store.Update(func(state *billing.State) {
		// Price only the upstream name; the client-facing alias is unpriced.
		state.Prices = []billing.PriceRule{{Pattern: "fake-model", InputPer1M: 1, OutputPer1M: 2}}
	})

	admit(t, app, "openai", false)
	if _, errHandle := app.HandleMethod(MethodRequestInterceptAfter, mustMarshal(t, RequestInterceptRequest{
		RequestID: flowRequestID,
		Model:     "fake-model",
	})); errHandle != nil {
		t.Fatalf("request.intercept_after error = %v", errHandle)
	}
	observeResponse(t, app, "openai", []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	complete(t, app, RequestCompletionSucceeded)

	cost, _ := lifetimeCost(t, app)
	assertCostClose(t, cost, 0.001+0.001)

	app.store.Read(func(state *billing.State) {
		if state.Keys[flowScope()].ByModel["fake-model"] == nil {
			t.Fatalf("ByModel = %+v, want the upstream model recorded", state.Keys[flowScope()].ByModel)
		}
	})
}

// TestFlowFallsBackToClientModelNameWhenAuthNeverRuns keeps pricing working for
// requests that fail before credential selection.
func TestFlowFallsBackToClientModelNameWhenAuthNeverRuns(t *testing.T) {
	app := newAppWithPrice(t, true)
	admit(t, app, "openai", false)
	observeResponse(t, app, "openai", []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	complete(t, app, RequestCompletionSucceeded)

	// newAppWithPrice prices "gpt-5.5", the name the client asked for.
	cost, _ := lifetimeCost(t, app)
	assertCostClose(t, cost, 0.001+0.001)
}

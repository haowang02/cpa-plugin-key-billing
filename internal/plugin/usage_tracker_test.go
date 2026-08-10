package plugin

import (
	"testing"

	"cpa-key-billing/internal/billing"
)

func admitRoute(t *testing.T, app *App, requestID, clientFormat, upstreamFormat, model string, stream bool, body []byte) {
	t.Helper()
	for _, req := range []RequestInterceptRequest{
		{RequestID: requestID, SourceFormat: clientFormat, Model: model, RequestedModel: model, Stream: stream, Body: body, Metadata: flowMetadata()},
		{RequestID: requestID, SourceFormat: clientFormat, ToFormat: upstreamFormat, Model: model, RequestedModel: model, Stream: stream, Body: body, Metadata: flowMetadata()},
	} {
		method := MethodRequestInterceptBefore
		if req.ToFormat != "" {
			method = MethodRequestInterceptAfter
		}
		raw, errHandle := app.HandleMethod(method, mustMarshal(t, req))
		if errHandle != nil {
			t.Fatalf("%s error = %v", method, errHandle)
		}
		var response RequestInterceptResponse
		decodeResult(t, raw, &response)
		if response.Terminate {
			t.Fatalf("%s terminated request: %s", method, response.ResponseBody)
		}
	}
}

func TestUsageCodexStreamRecordsUsage(t *testing.T) {
	app := newAppWithPrice(t, true)
	requestBody := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	admitRoute(t, app, flowRequestID, "claude", "codex", "gpt-5.5", true, requestBody)

	observeUpstream(t, app, "codex", "gpt-5.5", true, requestBody,
		[]byte(`data: {"type":"response.created","response":{"id":"resp-1","model":"gpt-5.5"}}`))
	observeUpstream(t, app, "codex", "gpt-5.5", true, requestBody,
		[]byte(`data: {"type":"response.completed","response":{"id":"resp-1","usage":{"input_tokens":19857,"output_tokens":11,"total_tokens":19868}}}`))
	complete(t, app, flowRequestID, RequestCompletionSucceeded)

	app.store.Read(func(state *billing.State) {
		key := state.Keys[flowScope()]
		if key == nil || key.Lifetime.UncachedInputTokens != 19857 || key.Lifetime.OutputTokens != 11 || key.Lifetime.Requests != 1 {
			t.Fatalf("lifetime = %+v, want input=19857 output=11", key)
		}
	})
}

func TestUsageNonStreamCodexResponseRecordsUsage(t *testing.T) {
	app := newAppWithPrice(t, true)
	requestBody := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`)
	admitRoute(t, app, flowRequestID, "claude", "codex", "gpt-5.5", false, requestBody)
	observeUpstream(t, app, "codex", "gpt-5.5", false, requestBody,
		[]byte(`{"type":"response.completed","response":{"id":"resp-nonstream","usage":{"input_tokens":642,"output_tokens":11,"total_tokens":653}}}`))
	respond(t, app, flowRequestID, []byte(`{"id":"resp-nonstream","type":"message"}`))
	complete(t, app, flowRequestID, RequestCompletionSucceeded)

	app.store.Read(func(state *billing.State) {
		key := state.Keys[flowScope()]
		if key == nil || key.Lifetime.UncachedInputTokens != 642 || key.Lifetime.OutputTokens != 11 {
			t.Fatalf("lifetime = %+v, want input=642 output=11", key)
		}
	})
}

func TestUsageAnthropicStreamCombinesSplitRealUsage(t *testing.T) {
	app := newAppWithPrice(t, true)
	requestBody := []byte(`{"model":"gpt-5.5","input":"hello","stream":true}`)
	admitRoute(t, app, flowRequestID, "openai-response", "claude", "gpt-5.5", true, requestBody)
	observeUpstream(t, app, "claude", "gpt-5.5", true, requestBody,
		[]byte(`data: {"type":"message_start","message":{"id":"msg-real-1","usage":{"input_tokens":500,"cache_read_input_tokens":400,"cache_creation_input_tokens":100,"output_tokens":0}}}`))
	streamChunk(t, app, flowRequestID, 0,
		[]byte(`data: {"type":"response.created","response":{"id":"msg-real-1"}}`))
	observeUpstream(t, app, "claude", "gpt-5.5", true, requestBody,
		[]byte(`data: {"type":"message_delta","usage":{"output_tokens":500,"output_tokens_details":{"thinking_tokens":200}}}`))
	complete(t, app, flowRequestID, RequestCompletionSucceeded)

	app.store.Read(func(state *billing.State) {
		key := state.Keys[flowScope()]
		if key == nil || key.Lifetime.UncachedInputTokens != 500 || key.Lifetime.CacheReadTokens != 400 ||
			key.Lifetime.CacheCreationTokens != 100 || key.Lifetime.OutputTokens != 500 || key.Lifetime.ReasoningTokens != 200 {
			t.Fatalf("lifetime = %+v", key)
		}
	})
}

func TestUsageAnthropicSplitUsageBeforeResponseBinding(t *testing.T) {
	app := newAppWithPrice(t, true)
	requestBody := []byte(`{"model":"gpt-5.5","messages":[]}`)
	admitRoute(t, app, flowRequestID, "openai", "claude", "gpt-5.5", false, requestBody)
	observeUpstream(t, app, "claude", "gpt-5.5", false, requestBody,
		[]byte(`data: {"type":"message_start","message":{"id":"msg-aggregated","usage":{"input_tokens":500,"output_tokens":0}}}`))
	observeUpstream(t, app, "claude", "gpt-5.5", false, requestBody,
		[]byte(`data: {"type":"message_delta","usage":{"output_tokens":25}}`))
	respond(t, app, flowRequestID, []byte(`{"id":"msg-aggregated","usage":{"prompt_tokens":999,"completion_tokens":999}}`))
	complete(t, app, flowRequestID, RequestCompletionSucceeded)

	app.store.Read(func(state *billing.State) {
		key := state.Keys[flowScope()]
		if key == nil || key.Lifetime.UncachedInputTokens != 500 || key.Lifetime.OutputTokens != 25 {
			t.Fatalf("lifetime = %+v", key)
		}
	})
}

// A translated stream chunk is not authoritative, so its tokens are ignored and
// the request is logged as unmeasured rather than billed from them.
func TestUsageIgnoresTranslatedTokens(t *testing.T) {
	app := newAppWithPrice(t, true)
	requestBody := []byte(`{"model":"gpt-5.5","messages":[],"stream":true}`)
	admitRoute(t, app, flowRequestID, "claude", "codex", "gpt-5.5", true, requestBody)
	streamChunk(t, app, flowRequestID, 0,
		[]byte(`data: {"type":"message_start","message":{"id":"resp-1","usage":{"input_tokens":100,"output_tokens":0}}}`))
	complete(t, app, flowRequestID, RequestCompletionSucceeded)
	if cost, _ := lifetimeCost(t, app); cost != 0 {
		t.Fatalf("cost=%v, want nothing charged from translated tokens", cost)
	}
	if entries := app.store.Logs(0).Entries; len(entries) != 1 || entries[0].AccountingQuality != "" {
		t.Fatalf("entries = %+v, want one unmeasured row", entries)
	}
}

func TestUsageAmbiguousRawUsageWithoutResponseIDFailsClosed(t *testing.T) {
	app := newAppWithPrice(t, true)
	requestBody := []byte(`{"model":"gpt-5.5","messages":[]}`)
	admitRoute(t, app, "ambiguous-1", "claude", "codex", "gpt-5.5", false, requestBody)
	admitRoute(t, app, "ambiguous-2", "claude", "codex", "gpt-5.5", false, requestBody)
	observeUpstream(t, app, "codex", "gpt-5.5", false, requestBody,
		[]byte(`{"usage":{"input_tokens":1000,"output_tokens":500,"total_tokens":1500}}`))
	complete(t, app, "ambiguous-1", RequestCompletionSucceeded)
	complete(t, app, "ambiguous-2", RequestCompletionSucceeded)
	if cost, _ := lifetimeCost(t, app); cost != 0 {
		t.Fatalf("ambiguous usage was attributed: cost=%v", cost)
	}
	for _, entry := range app.store.Logs(0).Entries {
		if entry.AccountingQuality != "" || entry.Cost.TotalUSD != 0 {
			t.Fatalf("entry = %+v, want an unmeasured zero-cost row", entry)
		}
	}
}

func TestUsageOpenAIUsageKeepsCacheAndReasoningBuckets(t *testing.T) {
	app := newAppWithPrice(t, true)
	requestBody := []byte(`{"model":"gpt-5.5","messages":[]}`)
	admitRoute(t, app, flowRequestID, "openai", "openai", "gpt-5.5", false, requestBody)
	observeUpstream(t, app, "openai", "gpt-5.5", false, requestBody,
		[]byte(`{"id":"chatcmpl-1","usage":{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500,"prompt_tokens_details":{"cached_tokens":400,"cache_creation_tokens":100},"completion_tokens_details":{"reasoning_tokens":200}}}`))
	respond(t, app, flowRequestID,
		[]byte(`{"id":"chatcmpl-1","usage":{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500,"prompt_tokens_details":{"cached_tokens":400,"cache_creation_tokens":100},"completion_tokens_details":{"reasoning_tokens":200}}}`))
	complete(t, app, flowRequestID, RequestCompletionSucceeded)

	app.store.Read(func(state *billing.State) {
		key := state.Keys[flowScope()]
		if key == nil || key.Lifetime.UncachedInputTokens != 500 || key.Lifetime.CacheReadTokens != 400 ||
			key.Lifetime.CacheCreationTokens != 100 || key.Lifetime.OutputTokens != 500 || key.Lifetime.ReasoningTokens != 200 {
			t.Fatalf("lifetime = %+v", key)
		}
	})
}

func TestUsageNativeClaudeStreamReadsUntranslatedUpstreamChunk(t *testing.T) {
	app := newAppWithPrice(t, true)
	requestBody := []byte(`{"model":"gpt-5.5","messages":[],"stream":true}`)
	admitRoute(t, app, flowRequestID, "claude", "claude", "gpt-5.5", true, requestBody)
	// ClaudeExecutor directly forwards native Claude streams without invoking
	// response.normalize_before. With matching formats this chunk is still the
	// original upstream event, so its usage is authoritative.
	streamChunk(t, app, flowRequestID, 0,
		[]byte(`data: {"type":"message_start","message":{"id":"msg-native","usage":{"input_tokens":500,"cache_read_input_tokens":400,"cache_creation_input_tokens":100,"output_tokens":0}}}`))
	streamChunk(t, app, flowRequestID, 1,
		[]byte(`data: {"type":"message_delta","usage":{"output_tokens":500}}`))
	complete(t, app, flowRequestID, RequestCompletionSucceeded)

	app.store.Read(func(state *billing.State) {
		key := state.Keys[flowScope()]
		if key == nil || key.Lifetime.UncachedInputTokens != 500 || key.Lifetime.CacheReadTokens != 400 ||
			key.Lifetime.CacheCreationTokens != 100 || key.Lifetime.OutputTokens != 500 {
			t.Fatalf("lifetime = %+v", key)
		}
	})
}

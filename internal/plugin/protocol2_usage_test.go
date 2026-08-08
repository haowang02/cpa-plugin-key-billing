package plugin

import (
	"testing"

	"cpa-key-billing/internal/billing"
)

func protocol2Admit(t *testing.T, app *App, requestID, source, provider, model string, stream bool, body []byte) {
	t.Helper()
	for _, req := range []RequestInterceptRequest{
		{RequestID: requestID, SourceFormat: source, Model: model, RequestedModel: model, Stream: stream, Body: body, Metadata: flowMetadata()},
		{RequestID: requestID, SourceFormat: source, ToFormat: provider, Model: model, RequestedModel: model, Stream: stream, Body: body, Metadata: flowMetadata()},
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

func protocol2Observe(t *testing.T, app *App, provider, model string, stream bool, requestBody, responseBody []byte) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodResponseNormalizeBefore, mustMarshal(t, ResponseTransformRequest{
		FromFormat: provider, Model: model, Stream: stream, OriginalRequest: requestBody, Body: responseBody,
	}))
	if errHandle != nil {
		t.Fatalf("response.normalize_before error = %v", errHandle)
	}
	var response PayloadResponse
	decodeResult(t, raw, &response)
	if string(response.Body) != string(responseBody) {
		t.Fatalf("response body changed: got %q, want %q", response.Body, responseBody)
	}
}

func protocol2StreamChunk(t *testing.T, app *App, requestID string, index int, body []byte) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodResponseStreamChunk, mustMarshal(t, ResponseInterceptRequest{
		RequestID: requestID, ChunkIndex: index, Body: body,
	}))
	if errHandle != nil {
		t.Fatalf("response.intercept_stream_chunk error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
}

func protocol2Response(t *testing.T, app *App, requestID string, body []byte) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodResponseInterceptAfter, mustMarshal(t, ResponseInterceptRequest{
		RequestID: requestID, Body: body,
	}))
	if errHandle != nil {
		t.Fatalf("response.intercept_after error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
}

func protocol2Complete(t *testing.T, app *App, requestID string) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodRequestComplete, mustMarshal(t, RequestCompletion{
		RequestID: requestID, Outcome: RequestCompletionSucceeded,
	}))
	if errHandle != nil {
		t.Fatalf("request.complete error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
}

func TestProtocol2CodexStreamUsesRawUpstreamUsageNotClaudeEstimate(t *testing.T) {
	app := newAppWithPriceSchema(t, true, MinHostSchemaVersion)
	requestBody := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	protocol2Admit(t, app, flowRequestID, "claude", "codex", "gpt-5.5", true, requestBody)

	protocol2Observe(t, app, "codex", "gpt-5.5", true, requestBody,
		[]byte(`data: {"type":"response.created","response":{"id":"resp-real-1","model":"gpt-5.5"}}`))
	// This translated Claude message_start contains CLIProxyAPI's synthesized
	// 20.7K input count. It is used only to correlate the response ID.
	protocol2StreamChunk(t, app, flowRequestID, 0,
		[]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"resp-real-1\",\"usage\":{\"input_tokens\":20700,\"output_tokens\":0}}}"))
	protocol2Observe(t, app, "codex", "gpt-5.5", true, requestBody,
		[]byte(`data: {"type":"response.completed","response":{"id":"resp-real-1","usage":{"input_tokens":19857,"output_tokens":11,"total_tokens":19868}}}`))
	protocol2Complete(t, app, flowRequestID)

	app.store.Read(func(state *billing.State) {
		key := state.Keys[flowScope()]
		if key == nil || key.Lifetime.UncachedInputTokens != 19857 || key.Lifetime.OutputTokens != 11 || key.Lifetime.Requests != 1 {
			t.Fatalf("lifetime = %+v, want raw upstream input=19857 output=11", key)
		}
	})
}

func TestProtocol2NonStreamCodexResponseUsesRawUsage(t *testing.T) {
	app := newAppWithPriceSchema(t, true, MinHostSchemaVersion)
	requestBody := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`)
	protocol2Admit(t, app, flowRequestID, "claude", "codex", "gpt-5.5", false, requestBody)
	protocol2Observe(t, app, "codex", "gpt-5.5", false, requestBody,
		[]byte(`{"type":"response.completed","response":{"id":"resp-nonstream","usage":{"input_tokens":642,"output_tokens":11,"total_tokens":653}}}`))
	protocol2Response(t, app, flowRequestID,
		[]byte(`{"id":"resp-nonstream","type":"message","usage":{"input_tokens":99999,"output_tokens":11}}`))
	protocol2Complete(t, app, flowRequestID)

	app.store.Read(func(state *billing.State) {
		key := state.Keys[flowScope()]
		if key == nil || key.Lifetime.UncachedInputTokens != 642 || key.Lifetime.OutputTokens != 11 {
			t.Fatalf("lifetime = %+v, want raw upstream input=642 output=11", key)
		}
	})
}

func TestProtocol2AnthropicStreamCombinesSplitRealUsage(t *testing.T) {
	app := newAppWithPriceSchema(t, true, MinHostSchemaVersion)
	requestBody := []byte(`{"model":"gpt-5.5","input":"hello","stream":true}`)
	protocol2Admit(t, app, flowRequestID, "openai-response", "claude", "gpt-5.5", true, requestBody)
	protocol2Observe(t, app, "claude", "gpt-5.5", true, requestBody,
		[]byte(`data: {"type":"message_start","message":{"id":"msg-real-1","usage":{"input_tokens":500,"cache_read_input_tokens":400,"cache_creation_input_tokens":100,"output_tokens":0}}}`))
	protocol2StreamChunk(t, app, flowRequestID, 0,
		[]byte(`data: {"type":"response.created","response":{"id":"msg-real-1"}}`))
	protocol2Observe(t, app, "claude", "gpt-5.5", true, requestBody,
		[]byte(`data: {"type":"message_delta","usage":{"output_tokens":500,"output_tokens_details":{"thinking_tokens":200}}}`))
	protocol2Complete(t, app, flowRequestID)

	app.store.Read(func(state *billing.State) {
		key := state.Keys[flowScope()]
		if key == nil || key.Lifetime.UncachedInputTokens != 500 || key.Lifetime.CacheReadTokens != 400 ||
			key.Lifetime.CacheCreationTokens != 100 || key.Lifetime.OutputTokens != 500 || key.Lifetime.ReasoningTokens != 200 {
			t.Fatalf("lifetime = %+v", key)
		}
	})
}

func TestProtocol2AnthropicSplitUsageBeforeResponseBinding(t *testing.T) {
	app := newAppWithPriceSchema(t, true, MinHostSchemaVersion)
	requestBody := []byte(`{"model":"gpt-5.5","messages":[]}`)
	protocol2Admit(t, app, flowRequestID, "openai", "claude", "gpt-5.5", false, requestBody)
	protocol2Observe(t, app, "claude", "gpt-5.5", false, requestBody,
		[]byte(`data: {"type":"message_start","message":{"id":"msg-aggregated","usage":{"input_tokens":500,"output_tokens":0}}}`))
	protocol2Observe(t, app, "claude", "gpt-5.5", false, requestBody,
		[]byte(`data: {"type":"message_delta","usage":{"output_tokens":25}}`))
	protocol2Response(t, app, flowRequestID, []byte(`{"id":"msg-aggregated","usage":{"prompt_tokens":999,"completion_tokens":999}}`))
	protocol2Complete(t, app, flowRequestID)

	app.store.Read(func(state *billing.State) {
		key := state.Keys[flowScope()]
		if key == nil || key.Lifetime.UncachedInputTokens != 500 || key.Lifetime.OutputTokens != 25 {
			t.Fatalf("lifetime = %+v", key)
		}
	})
}

func TestProtocol2TranslatedUsageAloneNeverBills(t *testing.T) {
	app := newAppWithPriceSchema(t, true, MinHostSchemaVersion)
	requestBody := []byte(`{"model":"gpt-5.5","messages":[],"stream":true}`)
	protocol2Admit(t, app, flowRequestID, "claude", "codex", "gpt-5.5", true, requestBody)
	protocol2StreamChunk(t, app, flowRequestID, 0,
		[]byte(`data: {"type":"message_start","message":{"id":"resp-estimated-only","usage":{"input_tokens":20700,"output_tokens":0}}}`))
	protocol2Complete(t, app, flowRequestID)
	if cost, requests := lifetimeCost(t, app); cost != 0 || requests != 0 {
		t.Fatalf("translated-only usage billed: cost=%v requests=%d", cost, requests)
	}
}

func TestProtocol2AmbiguousRawUsageWithoutResponseIDFailsClosed(t *testing.T) {
	app := newAppWithPriceSchema(t, true, MinHostSchemaVersion)
	requestBody := []byte(`{"model":"gpt-5.5","messages":[]}`)
	protocol2Admit(t, app, "ambiguous-1", "claude", "codex", "gpt-5.5", false, requestBody)
	protocol2Admit(t, app, "ambiguous-2", "claude", "codex", "gpt-5.5", false, requestBody)
	protocol2Observe(t, app, "codex", "gpt-5.5", false, requestBody,
		[]byte(`{"usage":{"input_tokens":1000,"output_tokens":500,"total_tokens":1500}}`))
	protocol2Complete(t, app, "ambiguous-1")
	protocol2Complete(t, app, "ambiguous-2")
	if cost, requests := lifetimeCost(t, app); cost != 0 || requests != 0 {
		t.Fatalf("ambiguous usage was attributed: cost=%v requests=%d", cost, requests)
	}
}

func TestProtocol2OpenAIUsageKeepsCacheAndReasoningBuckets(t *testing.T) {
	app := newAppWithPriceSchema(t, true, MinHostSchemaVersion)
	requestBody := []byte(`{"model":"gpt-5.5","messages":[]}`)
	protocol2Admit(t, app, flowRequestID, "openai", "openai", "gpt-5.5", false, requestBody)
	protocol2Observe(t, app, "openai", "gpt-5.5", false, requestBody,
		[]byte(`{"id":"chatcmpl-1","usage":{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500,"prompt_tokens_details":{"cached_tokens":400,"cache_creation_tokens":100},"completion_tokens_details":{"reasoning_tokens":200}}}`))
	protocol2Response(t, app, flowRequestID,
		[]byte(`{"id":"chatcmpl-1","usage":{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500,"prompt_tokens_details":{"cached_tokens":400,"cache_creation_tokens":100},"completion_tokens_details":{"reasoning_tokens":200}}}`))
	protocol2Complete(t, app, flowRequestID)

	app.store.Read(func(state *billing.State) {
		key := state.Keys[flowScope()]
		if key == nil || key.Lifetime.UncachedInputTokens != 500 || key.Lifetime.CacheReadTokens != 400 ||
			key.Lifetime.CacheCreationTokens != 100 || key.Lifetime.OutputTokens != 500 || key.Lifetime.ReasoningTokens != 200 {
			t.Fatalf("lifetime = %+v", key)
		}
	})
}

func TestProtocol2NativeClaudeStreamReadsUntranslatedUpstreamChunk(t *testing.T) {
	app := newAppWithPriceSchema(t, true, MinHostSchemaVersion)
	requestBody := []byte(`{"model":"gpt-5.5","messages":[],"stream":true}`)
	protocol2Admit(t, app, flowRequestID, "claude", "claude", "gpt-5.5", true, requestBody)
	// ClaudeExecutor directly forwards native Claude streams without invoking
	// response.normalize_before. With matching formats this chunk is still the
	// original upstream event, so its usage is authoritative.
	protocol2StreamChunk(t, app, flowRequestID, 0,
		[]byte(`data: {"type":"message_start","message":{"id":"msg-native","usage":{"input_tokens":500,"cache_read_input_tokens":400,"cache_creation_input_tokens":100,"output_tokens":0}}}`))
	protocol2StreamChunk(t, app, flowRequestID, 1,
		[]byte(`data: {"type":"message_delta","usage":{"output_tokens":500}}`))
	protocol2Complete(t, app, flowRequestID)

	app.store.Read(func(state *billing.State) {
		key := state.Keys[flowScope()]
		if key == nil || key.Lifetime.UncachedInputTokens != 500 || key.Lifetime.CacheReadTokens != 400 ||
			key.Lifetime.CacheCreationTokens != 100 || key.Lifetime.OutputTokens != 500 {
			t.Fatalf("lifetime = %+v", key)
		}
	})
}

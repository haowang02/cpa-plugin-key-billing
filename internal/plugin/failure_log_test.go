package plugin

import (
	"strings"
	"testing"

	"cpa-key-billing/internal/billing"
)

func pluginLog(t *testing.T, app *App) []billing.Event {
	t.Helper()
	return app.store.Events()
}

func onlyRequestEvent(t *testing.T, app *App) billing.Event {
	t.Helper()
	var found []billing.Event
	for _, event := range pluginLog(t, app) {
		if strings.HasPrefix(event.Message, "请求失败：") || strings.HasPrefix(event.Message, "额度拦截：") ||
			strings.HasPrefix(event.Message, "模型拦截：") {
			found = append(found, event)
		}
	}
	if len(found) != 1 {
		t.Fatalf("events = %+v, want exactly one entry about the request", found)
	}
	return found[0]
}

// A stream that dies partway through is the shape a failing upstream usually
// takes: the client is handed an error event, and that error is what the plugin
// log has to name.
func TestFailedStreamIsReportedWithTheErrorTheClientSaw(t *testing.T) {
	app := newAppWithPrice(t, true)
	if _, errSync := app.store.SyncKeys([]string{testAPIKey}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	if errLabel := app.store.SetLabel(flowScope(), "Alice"); errLabel != nil {
		t.Fatalf("SetLabel error = %v", errLabel)
	}
	publishUsage(t, app, "auth-3", "codex", "apikey", "sk-upstream-key-0001")

	admit(t, app, "openai", "/v1/chat/completions")
	selectCredential(t, app, "auth-3")
	streamChunk(t, app, flowRequestID, 0, []byte(`data: {"id":"`+flowResponseID+`"}`))
	streamChunk(t, app, flowRequestID, 1, []byte(
		`data: {"type":"error","code":"internal_server_error","message":"upstream stream closed before a terminal event"}`))
	complete(t, app, flowRequestID, RequestCompletionFailed)

	event := onlyRequestEvent(t, app)
	if event.Level != billing.EventError {
		t.Fatalf("level = %q, want an error", event.Level)
	}
	for _, want := range []string{
		"Alice", "sk-tes…", "/v1/chat/completions", "gpt-5.5", "codex · sk-ups…0001", flowRequestID,
		"upstream stream closed before a terminal event（internal_server_error）",
	} {
		if !strings.Contains(event.Message, want) {
			t.Fatalf("message = %q, want it to name %q", event.Message, want)
		}
	}
}

// The plaintext key is not in the plugin's hands by the time a request is
// reported, and the log must not reconstruct one either.
func TestFailureReportNamesNoPlaintextKey(t *testing.T) {
	app := newAppWithPrice(t, true)
	if _, errSync := app.store.SyncKeys([]string{testAPIKey}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}

	admit(t, app, "openai", "/v1/messages")
	respond(t, app, flowRequestID, []byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`))
	complete(t, app, flowRequestID, RequestCompletionFailed)

	message := onlyRequestEvent(t, app).Message
	if strings.Contains(message, testAPIKey) {
		t.Fatalf("message = %q, want no plaintext key in it", message)
	}
	if !strings.Contains(message, "bad request（invalid_request_error）") {
		t.Fatalf("message = %q, want the upstream error named", message)
	}
}

// Enforcement runs before the request reaches an upstream, so a blocked request
// is billed nothing and logged nowhere else.
func TestQuotaBlockedRequestIsReported(t *testing.T) {
	app := newAppWithPrice(t, true)
	if _, errSync := app.store.SyncKeys([]string{testAPIKey}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	app.store.ReplaceAll(func(state *billing.State) {
		state.Plans = []billing.Plan{{ID: "daily", Name: "Daily 1", AmountUSD: 1, Period: billing.Period{Kind: billing.PeriodDaily}}}
	})
	if errBind := app.store.BindKey(flowScope(), "daily"); errBind != nil {
		t.Fatalf("BindKey error = %v", errBind)
	}
	// Spend the window, so the next request is the one that gets turned away.
	billOneRequest(t, app, testAPIKey, 1_000_000)

	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		RequestID: "req-blocked", SourceFormat: "openai", Model: flowModel, RequestedModel: flowModel,
		Body: flowRequestBody, Metadata: map[string]any{
			MetadataCallerScope: flowScope(),
			MetadataRequestPath: "/v1/chat/completions",
		},
	}))
	if errHandle != nil {
		t.Fatalf("request.intercept_before error = %v", errHandle)
	}
	var blocked RequestInterceptResponse
	decodeResult(t, raw, &blocked)
	if !blocked.Terminate {
		t.Fatal("the exhausted key was admitted")
	}

	event := onlyRequestEvent(t, app)
	if event.Level != billing.EventInfo {
		t.Fatalf("level = %q, want information: enforcement working is not a fault", event.Level)
	}
	for _, want := range []string{"额度拦截：", "/v1/chat/completions", "Daily 1", "$1.0000"} {
		if !strings.Contains(event.Message, want) {
			t.Fatalf("message = %q, want it to name %q", event.Message, want)
		}
	}
}

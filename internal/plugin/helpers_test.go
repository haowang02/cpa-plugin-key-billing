package plugin

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"cpa-key-billing/internal/billing"
)

func billOneRequest(t *testing.T, app *App, apiKey string, outputTokens int64) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		SourceFormat: "openai", Model: "gpt-5.5", RequestedModel: "gpt-5.5",
		Metadata: map[string]any{
			MetadataCallerScope: billing.CallerScope(apiKey),
			MetadataRequestPath: "/v1/chat/completions",
		},
	}))
	if errHandle != nil {
		t.Fatalf("request.intercept_before error = %v", errHandle)
	}
	var admitted RequestInterceptResponse
	decodeResult(t, raw, &admitted)
	if admitted.Terminate {
		t.Fatalf("request was terminated: %s", admitted.ResponseBody)
	}
	publishUsageRecord(t, app, UsageRecord{
		Provider: "openai", ExecutorType: "OpenAICompatExecutor", Model: "gpt-5.5", Alias: "gpt-5.5",
		APIKey: apiKey, Generate: true, RequestedAt: app.store.Now(),
		Detail: UsageDetail{OutputTokens: outputTokens, TotalTokens: outputTokens},
	})
}

func logEntries(t *testing.T, app *App) []billing.LogRow {
	t.Helper()
	view, errLogs := app.store.Logs(billing.LogQuery{})
	if errLogs != nil {
		t.Fatalf("Logs error = %v", errLogs)
	}
	return view.Entries
}

func decodeResult(t *testing.T, raw []byte, v any) {
	t.Helper()
	var envelope Envelope
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("decode envelope: %v (raw=%s)", errUnmarshal, raw)
	}
	if !envelope.OK {
		t.Fatalf("envelope reports failure: %+v", envelope.Error)
	}
	if v != nil {
		if errUnmarshal := json.Unmarshal(envelope.Result, v); errUnmarshal != nil {
			t.Fatalf("decode result: %v (raw=%s)", errUnmarshal, envelope.Result)
		}
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	return raw
}

// testConfigYAML keeps every app under test on its own database: the default
// path is relative to the working directory, which is the source tree here.
func testConfigYAML(t *testing.T, enabled bool) []byte {
	t.Helper()
	return []byte("enabled: " + strconv.FormatBool(enabled) +
		"\nstate_file: \"" + filepath.Join(t.TempDir(), "state.db") + "\"\n")
}

func newConfiguredApp(t *testing.T) *App {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.Shutdown)
	raw, errHandle := app.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{
		ConfigYAML: testConfigYAML(t, true),
	}))
	if errHandle != nil {
		t.Fatalf("plugin.register error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
	return app
}

func newAppWithPrice(t *testing.T, enabled bool) *App {
	app, _ := newAppWithPriceAndState(t, enabled)
	return app
}

func newAppWithPriceAndState(t *testing.T, enabled bool) (*App, string) {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.Shutdown)
	statePath := filepath.Join(t.TempDir(), "state.db")
	configYAML := "enabled: " + strconv.FormatBool(enabled) + "\nstate_file: \"" + statePath + "\"\n"
	if _, errHandle := app.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{
		ConfigYAML: []byte(configYAML),
	})); errHandle != nil {
		t.Fatalf("plugin.register error = %v", errHandle)
	}
	cacheRead := 0.1
	cacheWrite := 1.25
	if _, errPrice := app.store.UpsertPrice(billing.PriceRule{
		Pattern:         "gpt-5.5",
		InputPer1M:      1,
		OutputPer1M:     2,
		CacheReadPer1M:  &cacheRead,
		CacheWritePer1M: &cacheWrite,
	}); errPrice != nil {
		t.Fatalf("UpsertPrice error = %v", errPrice)
	}
	return app, statePath
}

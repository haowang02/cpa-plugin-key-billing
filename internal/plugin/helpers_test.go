package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		APIKey: apiKey, RequestedAt: app.store.Now(),
		Detail: UsageDetail{OutputTokens: outputTokens, TotalTokens: outputTokens},
	})
}

func requestEventEntries(t *testing.T, app *App) []billing.RequestEventRow {
	t.Helper()
	view, err := app.store.RequestEvents(billing.RequestEventQuery{})
	if err != nil {
		t.Fatalf("RequestEvents error = %v", err)
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

// The default database path is relative to the working directory, which is the source tree here.
func testConfigYAML(t *testing.T, enabled bool) []byte {
	t.Helper()
	return []byte("enabled: " + strconv.FormatBool(enabled) +
		"\ndebug: true" +
		"\nstate_file: \"" + filepath.Join(t.TempDir(), "state.db") + "\"\n")
}

func newConfiguredApp(t *testing.T) *App {
	t.Helper()
	app := newTestApp(t)
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
	app := newTestApp(t)
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
	if _, errPrice := app.store.UpsertPrice(billing.CustomPrice{
		ModelID: "gpt-5.5", PriceRates: billing.PriceRates{InputPer1M: 1,
			OutputPer1M:     2,
			CacheReadPer1M:  &cacheRead,
			CacheWritePer1M: &cacheWrite},
	}); errPrice != nil {
		t.Fatalf("UpsertPrice error = %v", errPrice)
	}
	return app, statePath
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "billing", "testdata", "models_dev_prices.json"))
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err = json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	models := response["providers"].(map[string]any)["openai"].(map[string]any)["models"].(map[string]any)
	for _, id := range []string{"gpt-5.5", "gpt-5.6"} {
		models[id] = map[string]any{"id": id, "cost": map[string]any{"input": 1.0, "output": 2.0}}
	}
	raw, err = json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(billing.NewStore(openRepository, func(context.Context) ([]byte, error) { return raw, nil }))
	app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
		return nil, errors.New("unsupported host callback " + method)
	})
	return app
}

// authFileHost answers host.auth.list and host.auth.get_runtime from files, a JSON array of host auth file entries.
func authFileHost(files string) HostCaller {
	return func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case hostAuthList:
			return json.RawMessage(`{"files":` + files + `}`), nil
		case hostAuthGetRuntime:
			var listed []json.RawMessage
			if err := json.Unmarshal([]byte(files), &listed); err != nil {
				return nil, err
			}
			index := payload.(map[string]string)["auth_index"]
			for _, file := range listed {
				var entry struct {
					AuthIndex string `json:"auth_index"`
				}
				if json.Unmarshal(file, &entry) == nil && entry.AuthIndex == index {
					return json.RawMessage(`{"auth":` + string(file) + `}`), nil
				}
			}
			return nil, errors.New("auth not found for auth_index " + index)
		}
		return nil, errors.New("unsupported host callback " + method)
	}
}

// hostDirectoryCaller reports credentials as host.auth.models and answers auth file calls from files.
func hostDirectoryCaller(credentials, files string) HostCaller {
	return func(method string, payload any) (json.RawMessage, error) {
		if method == hostAuthModels {
			return json.RawMessage(`{"credentials":` + credentials + `}`), nil
		}
		return authFileHost(files)(method, payload)
	}
}

// The credentials routing tests name: config API keys when the ID starts with "config-", codex auth files
// otherwise, all serving gpt-5.6.
var routingTestCredentials = []string{"a", "b", "outside", "auth-a", "auth-b", "codex-a", "codex-b", "file-codex", "config-codex"}

func routingTestHost(ids ...string) HostCaller {
	credentials, files := []map[string]any{}, []map[string]any{}
	for _, id := range ids {
		credentials = append(credentials, map[string]any{"id": id, "auth_index": id, "provider": "codex", "models": []string{"gpt-5.6"}})
		if !strings.HasPrefix(id, "config-") {
			files = append(files, map[string]any{"id": id, "auth_index": id, "provider": "codex", "path": "/auth/" + id + ".json"})
		}
	}
	rawCredentials, _ := json.Marshal(credentials)
	rawFiles, _ := json.Marshal(files)
	return hostDirectoryCaller(string(rawCredentials), string(rawFiles))
}

func mustJSONRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	return raw
}

func configuredRoutingApp(t *testing.T, rule billing.RouteRule) (*App, string) {
	app := newConfiguredApp(t)
	app.SetHostCaller(routingTestHost(routingTestCredentials...))
	const key = "sk-route-test-00000001"
	scope := billing.CallerScope(key)
	if _, err := app.store.SyncKeys([]string{key}, false); err != nil {
		t.Fatal(err)
	}
	_, err := app.store.CreateRoute(billing.Route{ID: "route-test", Name: "Route", Rule: rule}, []string{scope})
	if err != nil {
		t.Fatal(err)
	}
	return app, scope
}

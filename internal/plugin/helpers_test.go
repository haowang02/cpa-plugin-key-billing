package plugin

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"cpa-key-billing/internal/billing"
)

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

func newConfiguredApp(t *testing.T) *App {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.Shutdown)
	configYAML := "enabled: true\nstate_file: \"" + filepath.Join(t.TempDir(), "state.json") + "\"\n"
	raw, errHandle := app.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{
		ConfigYAML:    []byte(configYAML),
		SchemaVersion: SchemaVersion,
	}))
	if errHandle != nil {
		t.Fatalf("plugin.register error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
	return app
}

func newAppWithPrice(t *testing.T, enabled bool) *App {
	return newAppWithPriceSchema(t, enabled, SchemaVersion)
}

func newAppWithPriceSchema(t *testing.T, enabled bool, schema uint32) *App {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.Shutdown)
	configYAML := "enabled: " + strconv.FormatBool(enabled) + "\nstate_file: \"" + filepath.Join(t.TempDir(), "state.json") + "\"\n"
	if _, errHandle := app.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{
		ConfigYAML:    []byte(configYAML),
		SchemaVersion: schema,
	})); errHandle != nil {
		t.Fatalf("plugin.register error = %v", errHandle)
	}
	cacheRead := 0.1
	cacheWrite := 1.25
	app.store.Update(func(state *billing.State) {
		state.Prices = []billing.PriceRule{{
			Pattern:         "gpt-5.5",
			InputPer1M:      1,
			OutputPer1M:     2,
			CacheReadPer1M:  &cacheRead,
			CacheWritePer1M: &cacheWrite,
		}}
	})
	return app
}

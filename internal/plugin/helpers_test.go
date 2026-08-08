package plugin

import (
	"path/filepath"
	"strconv"
	"testing"

	"cpa-key-billing/internal/billing"
)

func newAppWithPrice(t *testing.T, enabled bool) *App {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.Shutdown)
	configYAML := "enabled: " + strconv.FormatBool(enabled) + "\nstate_file: \"" + filepath.Join(t.TempDir(), "state.json") + "\"\n"
	if _, errHandle := app.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{
		ConfigYAML:    []byte(configYAML),
		SchemaVersion: SchemaVersion,
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

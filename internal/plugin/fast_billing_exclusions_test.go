package plugin

import (
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func fastExclusionUsage(app *App, model, alias string) UsageRecord {
	return UsageRecord{
		Model: model, Alias: alias, APIKey: testAPIKey,
		Provider: "codex", AuthType: "oauth", ServiceTier: "priority",
		RequestedAt: app.store.Now().Add(-time.Second),
		Detail:      UsageDetail{InputTokens: 200, CacheReadTokens: 100, OutputTokens: 100, TotalTokens: 300},
	}
}

func TestCodexFastBillingExclusions(t *testing.T) {
	for _, test := range []struct {
		name, excluded, alias, tier, provider, authType string
		enabled                                         bool
		factor                                          float64
	}{
		{"empty-default", "", "fast-alias", "priority", "codex", "oauth", true, 2.5},
		{"excluded", "fast-alias", "fast-alias", "priority", "codex", "oauth", true, 1},
		{"list-and-case", "other, FAST-ALIAS ,", "fast-alias", "priority", "codex", "oauth", true, 1},
		{"thinking-suffix", "fast-alias", "fast-alias(high)", "priority", "codex", "oauth", true, 1},
		{"prefixed-exclusion", "route/fast-alias", "route/fast-alias", "priority", "codex", "oauth", true, 1},
		{"prefix-is-significant", "fast-alias", "route/fast-alias", "priority", "codex", "oauth", true, 2.5},
		{"different-alias", "fast-alias", "fast-alias-other", "priority", "codex", "oauth", true, 2.5},
		{"upstream-is-not-alias", flowModel, "fast-alias", "priority", "codex", "oauth", true, 2.5},
		{"no-wildcards", "*-alias", "fast-alias", "priority", "codex", "oauth", true, 2.5},
		{"standard", "", "fast-alias", "default", "codex", "oauth", true, 1},
		{"disabled", "", "fast-alias", "priority", "codex", "oauth", false, 1},
		{"other-provider", "", "fast-alias", "priority", "openai", "oauth", true, 1},
		{"api-key", "", "fast-alias", "priority", "codex", "api_key", true, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, statePath := newAppWithPriceAndState(t, true)
			cfg := billing.Config{Enabled: true, StateFile: statePath,
				CodexFastModeBilling: test.enabled, CodexFastModeBillingExcludedModels: test.excluded}
			if err := app.store.Configure(cfg); err != nil {
				t.Fatal(err)
			}
			cacheRead, cacheWrite := 0.1, 1.25
			if _, err := app.store.UpsertPrice(billing.CustomPrice{
				ModelID:    billing.ModelWithoutThinkingSuffix(test.alias),
				PriceRates: billing.PriceRates{InputPer1M: 1, OutputPer1M: 2, CacheReadPer1M: &cacheRead, CacheWritePer1M: &cacheWrite},
			}); err != nil {
				t.Fatal(err)
			}
			record := fastExclusionUsage(app, flowModel, test.alias)
			record.ServiceTier, record.Provider, record.AuthType = test.tier, test.provider, test.authType
			publishUsageRecord(t, app, record)
			entries := requestEventEntries(t, app)
			if len(entries) != 1 {
				t.Fatalf("events = %+v", entries)
			}
			entry := entries[0]
			wantMultiplier := test.factor
			if wantMultiplier == 1 {
				wantMultiplier = 0
			}
			if entry.Cost.Multiplier != wantMultiplier || entry.ServiceTier != test.tier || entry.PriceSource != billing.PriceSourceCustom {
				t.Fatalf("unexpected billing metadata: %+v", entry)
			}
			assertCostClose(t, entry.Cost.TotalUSD, 0.00031*test.factor)
			assertCostClose(t, entry.Cost.AppliedInputPer1M, test.factor)
			assertCostClose(t, entry.Cost.AppliedOutputPer1M, 2*test.factor)
			assertCostClose(t, entry.Cost.AppliedCacheReadPer1M, 0.1*test.factor)
			assertCostClose(t, entry.Cost.AppliedCacheWritePer1M, 1.25*test.factor)
			if entry.Cost.UncachedInputTokens != 100 || entry.Cost.CacheReadTokens != 100 || entry.Cost.BilledOutputTokens != 100 {
				t.Fatalf("exclusion changed token usage: %+v", entry.Cost)
			}
		})
	}
}

func TestCodexFastExclusionPriceSourcesAndHistory(t *testing.T) {
	for _, test := range []struct {
		model  string
		source billing.PriceSource
	}{
		{flowModel, billing.PriceSourceCustom},
		{"gpt-4o", billing.PriceSourceReference},
		{"gpt-image-1.5", billing.PriceSourceBuiltin},
	} {
		t.Run(string(test.source), func(t *testing.T) {
			app, statePath := newAppWithPriceAndState(t, true)
			cfg := billing.Config{Enabled: true, StateFile: statePath, CodexFastModeBilling: true}
			if err := app.store.Configure(cfg); err != nil {
				t.Fatal(err)
			}
			record := fastExclusionUsage(app, test.model, test.model)
			record.RequestedAt = app.store.Now().Add(-2 * time.Second)
			publishUsageRecord(t, app, record)
			cfg.CodexFastModeBillingExcludedModels = test.model
			if err := app.store.Configure(cfg); err != nil {
				t.Fatal(err)
			}
			record.RequestedAt = record.RequestedAt.Add(time.Second)
			publishUsageRecord(t, app, record)
			app.Shutdown()
			reopened := newTestApp(t)
			t.Cleanup(reopened.Shutdown)
			if err := reopened.store.Configure(cfg); err != nil {
				t.Fatal(err)
			}
			entries := requestEventEntries(t, reopened)
			if len(entries) != 2 {
				t.Fatalf("persisted events = %+v", entries)
			}
			current, historical := entries[0], entries[1]
			if current.PriceSource != test.source || historical.PriceSource != test.source || current.Cost.Multiplier != 0 || historical.Cost.Multiplier != 2.5 {
				t.Fatalf("exclusion changed source or historical multiplier: %+v", entries)
			}
			if current.Cost.TotalUSD <= 0 {
				t.Fatal("exclusion made a priced model free")
			}
			assertCostClose(t, historical.Cost.TotalUSD, current.Cost.TotalUSD*2.5)
			if current.ServiceTier != "priority" || historical.ServiceTier != "priority" {
				t.Fatal("exclusion changed service tier")
			}
		})
	}
}

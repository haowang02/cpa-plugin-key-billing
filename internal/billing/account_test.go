package billing

import (
	"testing"
	"time"
)

func newAccountStore(t *testing.T, now time.Time) *Store {
	t.Helper()
	store := NewStore()
	store.SetNow(func() time.Time { return now })
	if err := store.Configure(testConfig(t)); err != nil {
		t.Fatalf("Configure error = %v", err)
	}
	t.Cleanup(store.Close)
	store.Update(func(state *State) {
		state.Prices = []PriceRule{{
			Pattern:         "gpt-5.5",
			InputPer1M:      1,
			OutputPer1M:     2,
			CacheReadPer1M:  floatPtr(0.1),
			CacheWritePer1M: floatPtr(1.25),
		}}
	})
	return store
}

func subsetEvent(scope string, at time.Time) UsageEvent {
	return UsageEvent{
		Scope: scope, Preview: "sk-tes…0001", Model: "gpt-5.5", Alias: "gpt-5.5",
		Semantics: SemanticsSubset,
		Tokens: TokenUsage{
			InputTokens: 1000, CacheReadTokens: 400, CacheCreationTokens: 100,
			OutputTokens: 500, ReasoningTokens: 200,
		},
		At: at,
	}
}

const wantSubsetCost = 0.0005 + 0.00004 + 0.000125 + 0.001

func TestRecordUsageCreatesAndAccumulatesAKey(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	store.RecordUsage(subsetEvent("scope-a", now))
	store.RecordUsage(subsetEvent("scope-a", now.Add(time.Hour)))

	store.Read(func(state *State) {
		key := state.Keys["scope-a"]
		if key == nil || key.Lifetime.Requests != 2 || key.Preview != "sk-tes…0001" {
			t.Fatalf("key = %+v", key)
		}
		assertClose(t, "CostUSD", key.Lifetime.CostUSD, 2*wantSubsetCost)
		if key.Lifetime.UncachedInputTokens != 1000 || key.Lifetime.CacheReadTokens != 800 ||
			key.Lifetime.CacheCreationTokens != 200 || key.Lifetime.OutputTokens != 1000 {
			t.Fatalf("Lifetime tokens = %+v", key.Lifetime)
		}
	})
}

func TestRecordUsageAccumulatesAndRollsPlanCycle(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	store.Update(func(state *State) {
		state.Plans = []Plan{{ID: "daily-5", AmountUSD: 5, Period: Period{Kind: PeriodDaily}}}
		state.Keys["scope-a"] = &KeyState{Scope: "scope-a", PlanID: "daily-5"}
	})

	store.RecordUsage(subsetEvent("scope-a", now))
	store.RecordUsage(subsetEvent("scope-a", now.Add(24*time.Hour)))

	store.Read(func(state *State) {
		key := state.Keys["scope-a"]
		if key.Cycle.Requests != 1 || len(key.RecentCycles) != 1 {
			t.Fatalf("key = %+v, want one current and one archived request", key)
		}
		assertClose(t, "CycleSpent", key.Cycle.SpentUSD, wantSubsetCost)
		assertClose(t, "Lifetime.CostUSD", key.Lifetime.CostUSD, 2*wantSubsetCost)
	})
}

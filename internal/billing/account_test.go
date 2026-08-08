package billing

import (
	"testing"
	"time"
)

func newAccountStore(t *testing.T, now time.Time) *Store {
	t.Helper()
	store := NewStore()
	store.now = func() time.Time { return now }
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
		Scope: scope, Preview: "sk-tes…0001", RequestID: "req-1", ClientProtocol: "claude", At: at,
		Records: []UsageRecord{{
			Provider: "codex", ExecutorType: "CodexExecutor", Model: "gpt-5.5", Alias: "gpt-5.5", Generate: true,
			Breakdown: completeBreakdown(500, 400, 100, 500, 200),
		}},
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

func TestRecordUsageIgnoresNonGenerationRecords(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	event := subsetEvent("scope-a", now)
	event.Records[0].Generate = false
	store.RecordUsage(event)
	store.Read(func(state *State) {
		if state.Keys["scope-a"] != nil || len(state.Log) != 0 {
			t.Fatalf("non-generation record was billed: %+v", state)
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

func TestLateCompletionStaysInItsAdmissionCycle(t *testing.T) {
	start := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, start)
	store.Update(func(state *State) {
		state.Plans = []Plan{{ID: "daily", AmountUSD: 5, Period: Period{Kind: PeriodDaily}}}
		state.Keys["scope-a"] = &KeyState{Scope: "scope-a", PlanID: "daily"}
	})
	decision := store.Authorize("scope-a", start)
	event := subsetEvent("scope-a", start.Add(25*time.Hour))
	event.AttributionKnown = true
	event.CyclePlanID = decision.PlanID
	event.CycleStartAt = decision.CycleStartAt
	event.CycleEndAt = decision.ResetAt
	event.CycleLimitUSD = decision.LimitUSD
	store.RecordUsage(event)

	store.Read(func(state *State) {
		key := state.Keys["scope-a"]
		if key.Cycle != (Cycle{}) || len(key.RecentCycles) != 1 || key.RecentCycles[0].Requests != 1 {
			t.Fatalf("key = %+v, want one charged archived cycle", key)
		}
		assertClose(t, "archived cost", key.RecentCycles[0].SpentUSD, wantSubsetCost)
	})
}

func TestConcurrentLateCompletionDoesNotChargeNewCycle(t *testing.T) {
	start := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, start)
	store.Update(func(state *State) {
		state.Plans = []Plan{{ID: "daily", AmountUSD: 5, Period: Period{Kind: PeriodDaily}}}
		state.Keys["scope-a"] = &KeyState{Scope: "scope-a", PlanID: "daily"}
	})
	oldCycle := store.Authorize("scope-a", start)
	newCycle := store.Authorize("scope-a", start.Add(25*time.Hour))
	if newCycle.CycleStartAt.Equal(oldCycle.CycleStartAt) {
		t.Fatal("new request did not start a new cycle")
	}

	event := subsetEvent("scope-a", start.Add(26*time.Hour))
	event.AttributionKnown = true
	event.CyclePlanID = oldCycle.PlanID
	event.CycleStartAt = oldCycle.CycleStartAt
	event.CycleEndAt = oldCycle.ResetAt
	event.CycleLimitUSD = oldCycle.LimitUSD
	store.RecordUsage(event)

	store.Read(func(state *State) {
		key := state.Keys["scope-a"]
		if !key.Cycle.StartAt.Equal(newCycle.CycleStartAt) || key.Cycle.Requests != 0 || key.Cycle.SpentUSD != 0 {
			t.Fatalf("new cycle was charged: %+v", key.Cycle)
		}
		if len(key.RecentCycles) != 1 || !key.RecentCycles[0].StartAt.Equal(oldCycle.CycleStartAt) || key.RecentCycles[0].Requests != 1 {
			t.Fatalf("old cycle history = %+v", key.RecentCycles)
		}
	})
}

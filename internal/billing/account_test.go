package billing

import (
	"testing"
	"time"
)

func newAccountStore(t *testing.T, now time.Time) *Store {
	store, _ := newAccountStoreWithRepository(t, now)
	return store
}

func newAccountStoreWithRepository(t *testing.T, now time.Time) (*Store, *memoryRepository) {
	t.Helper()
	store, repo := newStoreWithRepository(t)
	store.now = func() time.Time { return now }
	store.Update(func(state *State) {
		state.Prices = []PriceRule{{
			Pattern:         "gpt-5.5",
			InputPer1M:      1,
			OutputPer1M:     2,
			CacheReadPer1M:  floatPtr(0.1),
			CacheWritePer1M: floatPtr(1.25),
		}}
	})
	return store, repo
}

func subsetEvent(scope string, at time.Time) UsageEvent {
	return UsageEvent{
		Scope: scope, RequestID: "req-1", Endpoint: "/v1/messages", AuthIndex: "auth-codex", At: at,
		Record: &UsageRecord{
			BillingModel: "gpt-5.5", UpstreamModel: "gpt-5.5", Generate: true,
			Breakdown: completeBreakdown(500, 400, 100, 500, 200),
		},
	}
}

func admittedEvent(store *Store, scope string, at time.Time) UsageEvent {
	decision := store.Authorize(scope, at)
	event := subsetEvent(scope, at)
	event.CyclePlanID = decision.PlanID
	event.CycleStartAt = decision.CycleStartAt
	return event
}

const wantSubsetCost = 0.0005 + 0.00004 + 0.000125 + 0.001

func TestRecordUsageCreatesAndAccumulatesAKey(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	store.RecordUsage(subsetEvent("scope-a", now))
	store.RecordUsage(subsetEvent("scope-a", now.Add(time.Hour)))

	store.Read(func(state *State) {
		key := state.Keys["scope-a"]
		if key == nil || key.Lifetime.Requests != 2 {
			t.Fatalf("key = %+v", key)
		}
		assertClose(t, "CostUSD", key.Lifetime.CostUSD, 2*wantSubsetCost)
		if key.Lifetime.UncachedInputTokens != 1000 || key.Lifetime.CacheReadTokens != 800 ||
			key.Lifetime.CacheCreationTokens != 200 || key.Lifetime.OutputTokens != 1000 {
			t.Fatalf("Lifetime tokens = %+v", key.Lifetime)
		}
	})
}

func TestRecordUsageGroupsAndPricesByBillingModel(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	store.Update(func(state *State) {
		state.Prices = append(state.Prices, PriceRule{
			Pattern: "claude/gpt-latest", InputPer1M: 3, OutputPer1M: 4,
		})
	})
	event := subsetEvent("scope-a", now)
	event.Record.BillingModel = "claude/gpt-latest"
	store.RecordUsage(event)

	store.Read(func(state *State) {
		key := state.Keys["scope-a"]
		if len(key.ByModel) != 1 || key.ByModel["claude/gpt-latest"] == nil {
			t.Fatalf("ByModel = %+v", key.ByModel)
		}
		assertClose(t, "CostUSD", key.Lifetime.CostUSD, 0.0015+0.0012+0.0003+0.002)
	})
	entries := mustLogs(t, store, LogQuery{}).Entries
	if len(entries) != 1 || entries[0].UpstreamModel != "gpt-5.5" || entries[0].BillingModel != "claude/gpt-latest" {
		t.Fatalf("log = %+v", entries)
	}
}

func TestRecordUsageIgnoresANonGenerationRecord(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	event := subsetEvent("scope-a", now)
	event.Record.Generate = false
	store.RecordUsage(event)
	store.Read(func(state *State) {
		if state.Keys["scope-a"] != nil {
			t.Fatalf("non-generation record was billed: %+v", state.Keys)
		}
	})
	if entries := mustLogs(t, store, LogQuery{}).Entries; len(entries) != 0 {
		t.Fatalf("non-generation record reached the log: %+v", entries)
	}
}

func TestConcurrentLateCompletionDoesNotChargeNewCycle(t *testing.T) {
	start := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, start)
	store.Update(func(state *State) {
		state.Plans = []Plan{{ID: "daily", AmountUSD: 5, Period: Period{Kind: PeriodDaily}}}
		state.Keys["scope-a"] = &KeyState{PlanID: "daily"}
	})
	firstCycle := store.Authorize("scope-a", start)
	newCycle := store.Authorize("scope-a", start.Add(25*time.Hour))
	if newCycle.CycleStartAt.Equal(firstCycle.CycleStartAt) {
		t.Fatal("new request did not start a new cycle")
	}

	event := subsetEvent("scope-a", start.Add(26*time.Hour))
	event.CyclePlanID = firstCycle.PlanID
	event.CycleStartAt = firstCycle.CycleStartAt
	store.RecordUsage(event)

	store.Read(func(state *State) {
		key := state.Keys["scope-a"]
		if !key.Cycle.StartAt.Equal(newCycle.CycleStartAt) || key.Cycle.SpentUSD != 0 {
			t.Fatalf("new cycle was charged: %+v", key.Cycle)
		}
		assertClose(t, "Lifetime.CostUSD", key.Lifetime.CostUSD, wantSubsetCost)
	})
}

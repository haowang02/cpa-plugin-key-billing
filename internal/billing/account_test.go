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
	store.ReplaceAll(func(state *State) {
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
		Scope: scope, AuthIndex: "auth-codex", ExecutorType: "CodexExecutor",
		ReasoningEffort: "high", ServiceTier: "auto", At: at,
		UpstreamModel: "gpt-5.5", RouteModel: "gpt-5.5",
		Breakdown: completeBreakdown(500, 400, 100, 500, 200),
	}
}

func admittedEvent(store *Store, scope string, at time.Time) UsageEvent {
	store.Authorize(scope, at)
	return subsetEvent(scope, at)
}

const wantSubsetCost = 0.0005 + 0.00004 + 0.000125 + 0.001

func TestRecordUsageSeparatesNormalAndErrorEvents(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store, repo := newAccountStoreWithRepository(t, now)
	if _, err := store.ClearPluginLogs(); err != nil {
		t.Fatal(err)
	}
	store.RecordUsage(subsetEvent("scope-a", now))
	store.RecordUsageError(subsetEvent("scope-a", now.Add(time.Hour)), RequestError{StatusCode: 502})

	entries := mustRequestEvents(t, store, RequestEventQuery{}).Entries
	if len(entries) != 2 || len(repo.requestEvents) != 1 || len(repo.requestErrors) != 1 {
		t.Fatalf("request events = %d, normal writes = %d, error writes = %d", len(entries), len(repo.requestEvents), len(repo.requestErrors))
	}
	if !entries[0].Failed || entries[1].Failed {
		t.Fatalf("request events = %+v", entries)
	}
	errors, err := store.RequestErrors(RequestErrorQuery{})
	if err != nil || len(errors.Entries) != 1 || errors.Entries[0].StatusCode != 502 {
		t.Fatalf("request errors = %+v, err = %v", errors.Entries, err)
	}
	if logs := mustPluginLogs(t, store); len(logs) != 0 {
		t.Fatalf("usage events leaked into plugin logs: %+v", logs)
	}
}

func TestRecordUsageGroupsAndPricesByBillingModel(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	store.ReplaceAll(func(state *State) {
		state.Prices = append(state.Prices, PriceRule{
			Pattern: "claude/gpt-latest", InputPer1M: 3, OutputPer1M: 4,
		})
	})
	event := subsetEvent("scope-a", now)
	event.RouteModel = "claude/gpt-latest"
	store.RecordUsage(event)

	entries := mustRequestEvents(t, store, RequestEventQuery{}).Entries
	if len(entries) != 1 || entries[0].UpstreamModel != "gpt-5.5" || entries[0].BillingModel != "claude/gpt-latest" {
		t.Fatalf("request events = %+v", entries)
	}
	assertClose(t, "CostUSD", entries[0].Cost.TotalUSD, 0.0015+0.0012+0.0003+0.002)
}

func TestRecordUsagePreservesHostModelAndDurationValues(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	event := subsetEvent("scope-a", now)
	event.UpstreamModel = "gpt-5.5(high)"
	event.RouteModel = "gpt-5.5(high)"
	event.Latency = 999 * time.Microsecond
	event.TTFT = 1500 * time.Microsecond
	store.RecordUsage(event)

	entries := mustRequestEvents(t, store, RequestEventQuery{}).Entries
	if len(entries) != 1 {
		t.Fatalf("request events = %+v", entries)
	}
	entry := entries[0]
	if entry.UpstreamModel != "gpt-5.5(high)" || entry.BillingModel != "gpt-5.5" ||
		entry.LatencyMS != 0 || entry.TTFTMS != 1 {
		t.Fatalf("request event = %+v", entry)
	}
}

func TestConcurrentLateCompletionDoesNotChargeNewCycle(t *testing.T) {
	start := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, start)
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{{ID: "daily", AmountUSD: 5, PeriodSeconds: 86400}}
		state.Keys["scope-a"] = &KeyState{PlanID: "daily"}
	})
	firstCycle := store.Authorize("scope-a", start)
	newCycle := store.Authorize("scope-a", start.Add(25*time.Hour))
	if newCycle.CycleStartAt.Equal(firstCycle.CycleStartAt) {
		t.Fatal("new request did not start a new cycle")
	}

	event := subsetEvent("scope-a", start.Add(26*time.Hour))
	event.RequestedAt = start
	store.RecordUsage(event)

	store.Read(func(state *State) {
		key := state.Keys["scope-a"]
		if !key.Cycle.StartAt.Equal(newCycle.CycleStartAt) || key.Cycle.SpentUSD != 0 {
			t.Fatalf("new cycle was charged: %+v", key.Cycle)
		}
	})
	if len(mustRequestEvents(t, store, RequestEventQuery{}).Entries) != 1 {
		t.Fatal("late completion request event was not preserved")
	}
}

func TestFutureRequestedAtDoesNotClearCurrentCycle(t *testing.T) {
	start := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, start.Add(time.Hour))
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{{ID: "daily", AmountUSD: 5, PeriodSeconds: 86400}}
		state.Keys["scope-a"] = &KeyState{PlanID: "daily"}
	})
	cycle := store.Authorize("scope-a", start)
	store.ReplaceAll(func(state *State) {
		state.Keys["scope-a"].Cycle.SpentUSD = 4
	})

	event := subsetEvent("scope-a", start.Add(time.Hour))
	event.RequestedAt = start.Add(25 * time.Hour)
	store.RecordUsage(event)

	store.Read(func(state *State) {
		key := state.Keys["scope-a"]
		if !key.Cycle.StartAt.Equal(cycle.CycleStartAt) || key.Cycle.SpentUSD != 4 {
			t.Fatalf("cycle changed by request timestamp: %+v", key.Cycle)
		}
	})
}

func TestCompletionDoesNotOpenCycleAfterAdministrativeChange(t *testing.T) {
	start := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		change func(*Store) error
		planID string
	}{
		{"reset", func(store *Store) error {
			store.ResetCycles([]string{"scope-a"})
			return nil
		}, "daily"},
		{"rebind", func(store *Store) error { return store.BindKey("scope-a", "weekly") }, "weekly"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newAccountStore(t, start)
			store.ReplaceAll(func(state *State) {
				state.Plans = []Plan{
					{ID: "daily", AmountUSD: 5, PeriodSeconds: 86400},
					{ID: "weekly", AmountUSD: 5, PeriodSeconds: 604800},
				}
				state.Keys["scope-a"] = &KeyState{PlanID: "daily"}
			})
			store.Authorize("scope-a", start)
			if errChange := test.change(store); errChange != nil {
				t.Fatal(errChange)
			}

			event := subsetEvent("scope-a", start.Add(time.Hour))
			event.RequestedAt = start
			store.RecordUsage(event)

			store.Read(func(state *State) {
				key := state.Keys["scope-a"]
				if key.PlanID != test.planID || key.Cycle != (Cycle{}) {
					t.Fatalf("key = %+v, want plan %q with no active cycle", key, test.planID)
				}
			})
			if len(mustRequestEvents(t, store, RequestEventQuery{}).Entries) != 1 {
				t.Fatal("completion request event was not preserved")
			}
		})
	}
}

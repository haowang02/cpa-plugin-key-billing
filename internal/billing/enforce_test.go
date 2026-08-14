package billing

import (
	"testing"
	"time"
)

func newEnforceStore(t *testing.T, now time.Time) *Store {
	t.Helper()
	store := newStore(t)
	store.now = func() time.Time { return now }
	return store
}

// TestAuthorizeFailsOpen documents the deliberate bias: anything the plugin
// cannot resolve is allowed. A billing plugin that starts rejecting live
// traffic because of its own missing state is worse than one that under-charges.
func TestAuthorizeFailsOpen(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	t.Run("empty scope", func(t *testing.T) {
		store := newEnforceStore(t, now)
		if !store.Authorize("", now).Allowed {
			t.Fatal("an unattributable request was blocked")
		}
	})

	t.Run("unknown key", func(t *testing.T) {
		store := newEnforceStore(t, now)
		if !store.Authorize("never-seen", now).Allowed {
			t.Fatal("a key with no record was blocked")
		}
	})

	t.Run("key without a plan", func(t *testing.T) {
		store := newEnforceStore(t, now)
		store.Update(func(state *State) { state.Keys["s"] = &KeyState{} })
		if !store.Authorize("s", now).Allowed {
			t.Fatal("an unsubscribed key was blocked, it should be unlimited")
		}
	})

	t.Run("invalid plan with a zero amount", func(t *testing.T) {
		store := newEnforceStore(t, now)
		store.Update(func(state *State) {
			state.Plans = []Plan{{ID: "p", AmountUSD: 0, Period: Period{Kind: PeriodDaily}}}
			state.Keys["s"] = &KeyState{PlanID: "p", Cycle: Cycle{SpentUSD: 999}}
		})
		if decision := store.Authorize("s", now); decision.Allowed {
			t.Fatalf("decision = %+v, want the invalid plan treated as exhausted", decision)
		}
	})
}

func TestAuthorizeBlocksWhenCycleBudgetIsSpent(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newEnforceStore(t, now)
	store.Update(func(state *State) {
		state.Plans = []Plan{{ID: "daily-5", Name: "Daily 5", AmountUSD: 5, Period: Period{Kind: PeriodDaily}}}
		state.Keys["s"] = &KeyState{
			PlanID: "daily-5",
			Cycle: Cycle{
				PlanID:   "daily-5",
				StartAt:  time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC),
				EndAt:    time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC),
				SpentUSD: 5,
			},
		}
	})

	decision := store.Authorize("s", now)
	if decision.Allowed {
		t.Fatal("Allowed = true with the budget fully spent")
	}
	if decision.LimitUSD != 5 || decision.SpentUSD != 5 || decision.PlanName != "Daily 5" {
		t.Fatalf("decision = %+v", decision)
	}
	if !decision.ResetAt.Equal(time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("ResetAt = %s", decision.ResetAt)
	}
	store.Update(func(state *State) { state.Keys["s"].Cycle.SpentUSD = 4.999999 })
	if !store.Authorize("s", now).Allowed {
		t.Fatal("a key just under its limit was blocked")
	}
	store.Update(func(state *State) { state.Keys["s"].Cycle.SpentUSD = 7 })
	if store.Authorize("s", now).Allowed {
		t.Fatal("an over-spent key was allowed")
	}
}

func TestAuthorizeNeverResetPlanHasNoAutomaticReset(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newEnforceStore(t, now)
	store.Update(func(state *State) {
		state.Plans = []Plan{{ID: "once", Name: "One-time", AmountUSD: 5, Period: Period{Kind: PeriodNever}}}
		state.Keys["s"] = &KeyState{PlanID: "once", Cycle: Cycle{
			PlanID: "once", StartAt: now.Add(-365 * 24 * time.Hour), SpentUSD: 5,
		}}
	})

	decision := store.Authorize("s", now.Add(10*365*24*time.Hour))
	if decision.Allowed || !decision.ResetAt.IsZero() {
		t.Fatalf("decision = %+v, want blocked forever with no reset time", decision)
	}
	if errReset := store.ResetCycle("s"); errReset != nil {
		t.Fatalf("ResetCycle error = %v", errReset)
	}
	if !store.Authorize("s", now.Add(10*365*24*time.Hour)).Allowed {
		t.Fatal("manual reset did not restore the one-time budget")
	}
}

// TestAuthorizeRollsIdleCycle covers a key that stopped sending traffic before
// its window closed: the reset must happen on the next check, not on the next
// billed request.
func TestAuthorizeRollsIdleCycle(t *testing.T) {
	store := newEnforceStore(t, time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	store.Update(func(state *State) {
		state.Plans = []Plan{{ID: "p", AmountUSD: 5, Period: Period{Kind: PeriodDaily}}}
		state.Keys["s"] = &KeyState{
			PlanID: "p",
			Cycle: Cycle{
				PlanID:   "p",
				StartAt:  time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
				EndAt:    time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
				SpentUSD: 5,
			},
		}
	})

	decision := store.Authorize("s", time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	if !decision.Allowed {
		t.Fatal("an expired exhausted window still blocked the key")
	}
	store.Read(func(state *State) {
		key := state.Keys["s"]
		if !key.Cycle.StartAt.Equal(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)) {
			t.Fatalf("Cycle.StartAt = %s, want next use time", key.Cycle.StartAt)
		}
	})
}

func TestAuthorizeUnbindsDeletedPlan(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newEnforceStore(t, now)
	store.Update(func(state *State) {
		state.Keys["s"] = &KeyState{
			PlanID: "gone",
			Cycle:  Cycle{PlanID: "gone", StartAt: now, EndAt: now.Add(time.Hour), SpentUSD: 99},
		}
	})

	if !store.Authorize("s", now).Allowed {
		t.Fatal("a key bound to a deleted plan was blocked")
	}
	store.Read(func(state *State) {
		key := state.Keys["s"]
		if key.PlanID != "" || key.Cycle.SpentUSD != 0 {
			t.Fatalf("key = %+v, want the dangling binding cleared", key)
		}
	})
}

// TestAuthorizeAndRecordUsageAgreeOnTheCycle checks the two halves of the
// feature against each other: spend accumulated by billing is what enforcement
// reads, in the same window.
func TestAuthorizeAndRecordUsageAgreeOnTheCycle(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	const limitUSD = 0.004
	store.Update(func(state *State) {
		state.Plans = []Plan{{ID: "p", AmountUSD: limitUSD, Period: Period{Kind: PeriodDaily}}}
		state.Keys["scope-a"] = &KeyState{PlanID: "p"}
	})

	// Each request costs wantSubsetCost (~0.001665). The third is admitted
	// while spend is still 0.00333 and only pushes the total past the limit
	// once it completes. That last-request overshoot is inherent to checking
	// before a request and billing after it; it is bounded by the number of
	// requests in flight.
	for index := 0; index < 3; index++ {
		decision := store.Authorize("scope-a", now)
		if !decision.Allowed {
			t.Fatalf("request %d was blocked below the limit", index)
		}
		event := subsetEvent("scope-a", now)
		event.CyclePlanID = decision.PlanID
		event.CycleStartAt = decision.CycleStartAt
		store.RecordUsage(event)
	}
	if store.Authorize("scope-a", now).Allowed {
		t.Fatalf("the key was still allowed after spending %.6f of %.6f", 3*wantSubsetCost, limitUSD)
	}
	store.Read(func(state *State) {
		if spent := state.Keys["scope-a"].Cycle.SpentUSD; spent <= limitUSD {
			t.Fatalf("cycle spend = %.6f, want it past the %.6f limit", spent, limitUSD)
		}
	})
}

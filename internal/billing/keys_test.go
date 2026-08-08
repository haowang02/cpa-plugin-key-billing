package billing

import (
	"testing"
	"time"
)

func TestBindingAndResetLeaveCycleInactive(t *testing.T) {
	now := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	store := newEnforceStore(t, now)
	store.Update(func(state *State) {
		state.Plans = []Plan{{ID: "p", AmountUSD: 10, Period: Period{Kind: PeriodDaily}}}
		state.Keys["a"] = &KeyState{Scope: "a"}
	})

	if changed, err := store.BindKeys([]string{"a"}, "p"); err != nil || changed != 1 {
		t.Fatalf("BindKeys = %d, %v", changed, err)
	}
	store.Read(func(state *State) {
		if cycle := state.Keys["a"].Cycle; cycle != (Cycle{}) {
			t.Fatalf("cycle after bind = %+v, want inactive", cycle)
		}
	})

	if !store.Authorize("a", now).Allowed {
		t.Fatal("first use was blocked")
	}
	if changed, err := store.ResetCycles([]string{"a"}); err != nil || changed != 1 {
		t.Fatalf("ResetCycles = %d, %v", changed, err)
	}
	store.Read(func(state *State) {
		if cycle := state.Keys["a"].Cycle; cycle != (Cycle{}) {
			t.Fatalf("cycle after reset = %+v, want inactive", cycle)
		}
	})
}

func TestKeyDirectorySettlesExpiredCycleWithoutRestartingIt(t *testing.T) {
	now := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	store := newEnforceStore(t, now)
	store.Update(func(state *State) {
		state.Plans = []Plan{{ID: "p", AmountUSD: 10, Period: Period{Kind: PeriodDaily}}}
		state.Keys["a"] = &KeyState{Scope: "a", PlanID: "p", Cycle: Cycle{
			PlanID: "p", StartAt: now.Add(-48 * time.Hour), EndAt: now.Add(-24 * time.Hour), SpentUSD: 2, Requests: 3,
		}}
	})

	directory := store.KeyDirectory()
	if len(directory.Keys) != 1 || !directory.Keys[0].CycleStartAt.IsZero() || !directory.Keys[0].CycleEndAt.IsZero() {
		t.Fatalf("directory = %+v, want inactive cycle", directory)
	}
	store.Read(func(state *State) {
		key := state.Keys["a"]
		if key.Cycle != (Cycle{}) || len(key.RecentCycles) != 1 || key.RecentCycles[0].SpentUSD != 2 {
			t.Fatalf("key = %+v", key)
		}
	})
}

func TestPlanBindingTransactions(t *testing.T) {
	now := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	store := newEnforceStore(t, now)
	store.Update(func(state *State) {
		state.Keys["a"] = &KeyState{Scope: "a"}
		state.Keys["b"] = &KeyState{Scope: "b"}
		state.Keys["owned"] = &KeyState{Scope: "owned", PlanID: "other"}
		state.Plans = []Plan{{ID: "other", AmountUSD: 1, Period: Period{Kind: PeriodNever}}}
	})

	created, err := store.CreatePlanWithBindings(Plan{ID: "p", AmountUSD: 5, Period: Period{Kind: PeriodDaily}}, []string{"a"})
	if err != nil || created.ID != "p" {
		t.Fatalf("CreatePlanWithBindings = %+v, %v", created, err)
	}
	selected := []string{"b"}
	if _, err = store.UpdatePlanWithBindings(PlanPatch{ID: "p"}, &selected); err != nil {
		t.Fatalf("UpdatePlanWithBindings error = %v", err)
	}
	store.Read(func(state *State) {
		if state.Keys["a"].PlanID != "" || state.Keys["b"].PlanID != "p" || state.Keys["b"].Cycle != (Cycle{}) {
			t.Fatalf("keys = %+v", state.Keys)
		}
	})

	rejected := []string{"b", "owned"}
	if _, err = store.UpdatePlanWithBindings(PlanPatch{ID: "p"}, &rejected); err == nil {
		t.Fatal("stealing a key from another plan was accepted")
	}
	store.Read(func(state *State) {
		if state.Keys["b"].PlanID != "p" || state.Keys["owned"].PlanID != "other" {
			t.Fatalf("rejected update was not atomic: %+v", state.Keys)
		}
	})
}

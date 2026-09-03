package billing

import (
	"testing"
	"time"
)

func TestBindingAndResetLeaveCycleInactive(t *testing.T) {
	now := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	store := newEnforceStore(t, now)
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{{ID: "p", AmountUSD: 10, Period: Period{Kind: PeriodDaily}}}
		state.Keys["a"] = &KeyState{}
	})

	if errBind := store.BindKey("a", "p"); errBind != nil {
		t.Fatalf("BindKey error = %v", errBind)
	}
	store.Read(func(state *State) {
		if cycle := state.Keys["a"].Cycle; cycle != (Cycle{}) {
			t.Fatalf("cycle after bind = %+v, want inactive", cycle)
		}
	})

	if !store.Authorize("a", now).Allowed {
		t.Fatal("first use was blocked")
	}
	if errReset := store.ResetCycle("a"); errReset != nil {
		t.Fatalf("ResetCycle error = %v", errReset)
	}
	store.Read(func(state *State) {
		if cycle := state.Keys["a"].Cycle; cycle != (Cycle{}) {
			t.Fatalf("cycle after reset = %+v, want inactive", cycle)
		}
	})
}

func TestResetAllCyclesSparesPlansThatNeverReset(t *testing.T) {
	now := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	store := newEnforceStore(t, now)
	spent := Cycle{PlanID: "weekly", StartAt: now, EndAt: now.Add(time.Hour), SpentUSD: 3}
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{
			{ID: "weekly", AmountUSD: 10, Period: Period{Kind: PeriodWeekly}},
			{ID: "once", AmountUSD: 10, Period: Period{Kind: PeriodNever}},
		}
		state.Keys["periodic"] = &KeyState{PlanID: "weekly", Cycle: spent}
		state.Keys["one-time"] = &KeyState{PlanID: "once", Cycle: Cycle{PlanID: "once", StartAt: now, SpentUSD: 3}}
		state.Keys["unbound"] = &KeyState{Cycle: spent}
	})

	if reset := store.ResetAllCycles(); reset != 1 {
		t.Fatalf("reset = %d, want only the periodic key", reset)
	}
	store.Read(func(state *State) {
		if state.Keys["periodic"].Cycle != (Cycle{}) {
			t.Fatalf("periodic = %+v, want an inactive cycle", state.Keys["periodic"].Cycle)
		}
		if state.Keys["one-time"].Cycle.SpentUSD != 3 || state.Keys["unbound"].Cycle.SpentUSD != 3 {
			t.Fatalf("keys = %+v, want the one-time and unbound keys untouched", state.Keys)
		}
	})
}

func TestKeyViewsSettleExpiredCycleWithoutRestartingIt(t *testing.T) {
	now := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	store := newEnforceStore(t, now)
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{{ID: "p", AmountUSD: 10, Period: Period{Kind: PeriodDaily}}}
		state.Keys["a"] = &KeyState{PlanID: "p", Cycle: Cycle{
			PlanID: "p", StartAt: now.Add(-48 * time.Hour), EndAt: now.Add(-24 * time.Hour), SpentUSD: 2,
		}}
	})

	views := store.KeyViews()
	if len(views) != 1 || !views[0].CycleEndAt.IsZero() {
		t.Fatalf("views = %+v, want inactive cycle", views)
	}
	store.Read(func(state *State) {
		key := state.Keys["a"]
		if key.Cycle != (Cycle{}) {
			t.Fatalf("key = %+v", key)
		}
	})
}

func TestPlanBindingTransactions(t *testing.T) {
	now := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	store := newEnforceStore(t, now)
	store.ReplaceAll(func(state *State) {
		state.Keys["a"] = &KeyState{}
		state.Keys["b"] = &KeyState{}
		state.Keys["owned"] = &KeyState{PlanID: "other"}
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

const (
	keptKeyPlaintext    = "sk-live-0123456789"
	deletedKeyPlaintext = "sk-retired-0123456789"
)

// newSyncStore returns a store holding one plan and both keys above, already
// synchronized and bound, with a clock the caller can move.
func newSyncStore(t *testing.T, clock *time.Time) *Store {
	t.Helper()
	store := newAccountStore(t, *clock)
	store.now = func() time.Time { return *clock }
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{{ID: "p", Name: "Weekly", AmountUSD: 10, Period: Period{Kind: PeriodWeekly}}}
	})
	if _, errSync := store.SyncKeys([]string{keptKeyPlaintext, deletedKeyPlaintext}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	if errBind := store.BindKey(CallerScope(deletedKeyPlaintext), "p"); errBind != nil {
		t.Fatalf("BindKey error = %v", errBind)
	}
	return store
}

// Deleting a key in CPA must not delete what it spent. The record is what gives
// every request event its masked key and remark, so dropping it would leave
// the history unreadable even where the entries survived.
func TestSyncKeysRetiresAMissingKeyWithoutLosingItsHistory(t *testing.T) {
	now := time.Date(2026, 8, 12, 15, 57, 0, 0, time.UTC)
	clock := now
	store := newSyncStore(t, &clock)
	scope := CallerScope(deletedKeyPlaintext)
	store.RecordUsage(admittedEvent(store, scope, now))

	result, errSync := store.SyncKeys([]string{keptKeyPlaintext}, false)
	if errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	if result.Removed != 1 || result.Added != 0 {
		t.Fatalf("SyncResult = %+v, want one retirement and no addition", result)
	}

	store.Read(func(state *State) {
		key := state.Keys[scope]
		if key == nil {
			t.Fatal("the retired key was dropped, taking its history with it")
		}
		if key.DeletedAt != now || key.InConfig {
			t.Fatalf("key = %+v, want marked deleted and out of config", key)
		}
		if key.PlanID != "p" {
			t.Fatalf("PlanID = %q, want the binding kept for a later re-add", key.PlanID)
		}
		if key.Preview != PreviewKey(deletedKeyPlaintext) {
			t.Fatalf("key = %+v, want identity kept", key)
		}
	})

	if rows := mustRequestEvents(t, store, RequestEventQuery{}).Entries; len(rows) != 1 || rows[0].Preview != PreviewKey(deletedKeyPlaintext) {
		t.Fatalf("request events = %+v, want the retired key still named", rows)
	}

}

// The race this whole design exists for: a request admitted before the deletion
// commits after it. It must land on the record it was admitted under instead of
// recreating the key as a scope nobody can identify.
func TestUsageCommittedAfterRetirementDoesNotResurrectTheKey(t *testing.T) {
	now := time.Date(2026, 8, 12, 15, 57, 0, 0, time.UTC)
	clock := now
	store := newSyncStore(t, &clock)
	scope := CallerScope(deletedKeyPlaintext)
	event := admittedEvent(store, scope, now)

	clock = now.Add(43 * time.Minute)
	if _, errSync := store.SyncKeys([]string{keptKeyPlaintext}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	clock = now.Add(time.Hour)
	store.RecordUsage(event)

	store.Read(func(state *State) {
		key := state.Keys[scope]
		if key.Preview != PreviewKey(deletedKeyPlaintext) {
			t.Fatalf("key = %+v, want the retired record reused, not a bare scope", key)
		}
		if key.DeletedAt.IsZero() || key.InConfig {
			t.Fatalf("key = %+v, want it to stay retired", key)
		}
		// The window was live at admission, so the spend belongs to it.
		if key.Cycle.SpentUSD == 0 {
			t.Fatalf("Cycle = %+v, want the admitted window charged", key.Cycle)
		}
	})
}

func TestSyncKeysRestoresAReaddedKeyWithAFreshPeriod(t *testing.T) {
	now := time.Date(2026, 8, 12, 15, 57, 0, 0, time.UTC)
	clock := now
	store := newSyncStore(t, &clock)
	scope := CallerScope(deletedKeyPlaintext)
	store.RecordUsage(admittedEvent(store, scope, now))
	if _, errSync := store.SyncKeys([]string{keptKeyPlaintext}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}

	clock = now.Add(24 * time.Hour)
	result, errSync := store.SyncKeys([]string{keptKeyPlaintext, deletedKeyPlaintext}, false)
	if errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	if result.Added != 1 || result.Removed != 0 {
		t.Fatalf("SyncResult = %+v, want the key counted as added back", result)
	}
	store.Read(func(state *State) {
		key := state.Keys[scope]
		if !key.DeletedAt.IsZero() || !key.InConfig {
			t.Fatalf("key = %+v, want it live again", key)
		}
		if key.PlanID != "p" {
			t.Fatalf("PlanID = %q, want the subscription restored", key.PlanID)
		}
		if key.Cycle != (Cycle{}) {
			t.Fatalf("Cycle = %+v, want a period that starts on next use", key.Cycle)
		}
	})
	if len(mustRequestEvents(t, store, RequestEventQuery{}).Entries) != 1 {
		t.Fatal("request history was not kept across the round trip")
	}
}

func TestSyncKeysRetiresOnlyOnceAndSparesTrafficOnlyPrincipals(t *testing.T) {
	now := time.Date(2026, 8, 12, 15, 57, 0, 0, time.UTC)
	clock := now
	store := newSyncStore(t, &clock)
	// A principal that no sync ever listed may belong to another access
	// provider, so a CPA key-list sync has no authority over it.
	store.RecordUsage(subsetEvent("foreign-principal", now))

	if _, errSync := store.SyncKeys([]string{keptKeyPlaintext}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	result, errSync := store.SyncKeys([]string{keptKeyPlaintext}, false)
	if errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	if result.Removed != 0 {
		t.Fatalf("SyncResult = %+v, want an already retired key counted once", result)
	}
	store.Read(func(state *State) {
		foreign := state.Keys["foreign-principal"]
		if foreign == nil || !foreign.DeletedAt.IsZero() || foreign.InConfig {
			t.Fatalf("foreign principal = %+v, want it untouched", foreign)
		}
	})
}

func TestRetiredKeysAreDroppedOnceTheirLogExpired(t *testing.T) {
	now := time.Date(2026, 8, 12, 15, 57, 0, 0, time.UTC)
	clock := now
	store := newSyncStore(t, &clock)
	scope := CallerScope(deletedKeyPlaintext)
	store.RecordUsage(admittedEvent(store, scope, now))
	if _, errSync := store.SyncKeys([]string{keptKeyPlaintext}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}

	clock = now.Add(RequestEventRetention - time.Hour)
	if _, errSync := store.SyncKeys([]string{keptKeyPlaintext}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	store.Read(func(state *State) {
		if state.Keys[scope] == nil {
			t.Fatal("a retired key was dropped while its request events were still readable")
		}
	})

	clock = now.Add(RequestEventRetention + time.Hour)
	if !store.blocked.onset(scope, now) {
		t.Fatal("failed to seed request-report suppression state")
	}
	if _, errSync := store.SyncKeys([]string{keptKeyPlaintext}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	store.Read(func(state *State) {
		if state.Keys[scope] != nil {
			t.Fatal("a retired key outlived every request event that could name it")
		}
	})
	if !store.blocked.onset(scope, now) {
		t.Fatal("purged key retained request-report suppression state")
	}
}

// A retired key is invisible in the plan editor, so its absence from a
// submitted selection carries no intent.
func TestPlanEditKeepsRetiredBindings(t *testing.T) {
	now := time.Date(2026, 8, 12, 15, 57, 0, 0, time.UTC)
	clock := now
	store := newSyncStore(t, &clock)
	scope := CallerScope(deletedKeyPlaintext)
	if _, errSync := store.SyncKeys([]string{keptKeyPlaintext}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}

	selected := []string{CallerScope(keptKeyPlaintext)}
	if _, errUpdate := store.UpdatePlanWithBindings(PlanPatch{ID: "p"}, &selected); errUpdate != nil {
		t.Fatalf("UpdatePlanWithBindings error = %v", errUpdate)
	}
	store.Read(func(state *State) {
		if state.Keys[scope].PlanID != "p" {
			t.Fatalf("retired key = %+v, want its binding kept", state.Keys[scope])
		}
	})

	if errBind := store.BindKey(scope, "p"); errBind == nil {
		t.Fatal("a retired key was bindable through the management API")
	}
	if errLabel := store.SetLabel(scope, "x"); errLabel == nil {
		t.Fatal("a retired key was renameable through the management API")
	}
}

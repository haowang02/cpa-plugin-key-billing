package billing

import (
	"fmt"
	"testing"
	"time"
)

func TestPlanBindingTransactions(t *testing.T) {
	now := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	store := newStore(t)
	store.now = func() time.Time { return now }
	store.ReplaceAll(func(state *State) {
		state.Keys["a"] = &KeyState{}
		state.Keys["b"] = &KeyState{}
		state.Keys["owned"] = &KeyState{PlanID: "other"}
		state.Plans = []Plan{{ID: "other", Windows: []QuotaWindow{{ID: "default", Name: "额度", AmountUSD: 1, PeriodSeconds: 3600}}}}
	})

	created, err := store.CreatePlanWithBindings(Plan{ID: "p", Windows: []QuotaWindow{{Name: "额度", AmountUSD: 5, PeriodSeconds: 86400}}}, []string{"a"})
	if err != nil || created.ID != "p" {
		t.Fatalf("CreatePlanWithBindings = %+v, %v", created, err)
	}
	selected := []string{"b"}
	if _, err = store.UpdatePlanWithBindings(PlanPatch{ID: "p"}, &selected); err != nil {
		t.Fatalf("UpdatePlanWithBindings error = %v", err)
	}
	store.Read(func(state *State) {
		if state.Keys["a"].PlanID != "" || state.Keys["b"].PlanID != "p" || len(state.Keys["b"].Cycles) != 0 {
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
	deletedKeyPlaintext = "sk-deleted-0123456789"
)

// newSyncStore returns a store holding one plan and both keys above, already
// synchronized and bound, with a clock the caller can move.
func newSyncStore(t *testing.T, clock *time.Time) *Store {
	t.Helper()
	store := newAccountStore(t, *clock)
	store.now = func() time.Time { return *clock }
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{{ID: "p", Name: "Weekly", Windows: []QuotaWindow{{ID: "default", Name: "额度", AmountUSD: 10, PeriodSeconds: 604800}}}}
	})
	if _, errSync := store.SyncKeys([]string{keptKeyPlaintext, deletedKeyPlaintext}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	if errBind := store.BindKey(CallerScope(deletedKeyPlaintext), "p"); errBind != nil {
		t.Fatalf("BindKey error = %v", errBind)
	}
	return store
}

func TestDeletedKeyRetainsUsageAndIdentity(t *testing.T) {
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
			t.Fatalf("key = %+v, want the deleted record reused, not a bare scope", key)
		}
		if key.DeletedAt.IsZero() || key.InConfig {
			t.Fatalf("key = %+v, want it to stay deleted", key)
		}
		// The window was live at admission, so the spend belongs to it.
		if key.Cycles["default"].SpentUSD == 0 {
			t.Fatalf("Cycle = %+v, want the admitted window charged", key.Cycles["default"])
		}
	})
	if rows := mustRequestEvents(t, store, RequestEventQuery{}).Entries; len(rows) != 1 || rows[0].Preview != PreviewKey(deletedKeyPlaintext) {
		t.Fatalf("deleted key lost its historical identity: %+v", rows)
	}
	clock = now.Add(RequestEventRetention + time.Hour)
	if _, err := store.SyncKeys([]string{keptKeyPlaintext}, false); err != nil {
		t.Fatal(err)
	}
	if rows := mustRequestEvents(t, store, RequestEventQuery{}).Entries; len(rows) != 0 {
		t.Fatal("expired events remained visible")
	}
	key := store.state.Keys[scope]
	if key == nil || key.PlanID != "p" || key.Preview != PreviewKey(deletedKeyPlaintext) || key.DeletedAt.IsZero() {
		t.Fatalf("history expiry changed the deleted key: %+v", key)
	}
}

func TestSyncKeysRestoresQuotaAndBindings(t *testing.T) {
	for _, unified := range []bool{false, true} {
		for _, period := range []int64{3600, 7200} {
			t.Run(fmt.Sprintf("%t/%s", unified, time.Duration(period*int64(time.Second))), func(t *testing.T) {
				now := time.Date(2026, 8, 12, 15, 57, 0, 0, time.UTC)
				store := newSyncStore(t, &now)
				scope := CallerScope(deletedKeyPlaintext)
				store.ReplaceAll(func(state *State) {
					w := &state.Plans[0].Windows[0]
					w.PeriodSeconds = period
					if unified {
						w.CycleAnchorAt = now.Add(time.Duration(period) * time.Second)
					}
				})
				store.RecordUsage(admittedEvent(store, scope, now))
				store.ReplaceAll(func(state *State) {
					cycle := state.Keys[scope].Cycles["default"]
					cycle.SpentUSD = 10
					state.Keys[scope].Cycles["default"] = cycle
				})
				cycle := store.state.Keys[scope].Cycles["default"]
				if _, err := store.SyncKeys([]string{keptKeyPlaintext}, false); err != nil {
					t.Fatal(err)
				}
				now = now.Add(time.Minute)
				if _, err := store.SyncKeys([]string{keptKeyPlaintext, deletedKeyPlaintext}, false); err != nil {
					t.Fatal(err)
				}
				key := store.state.Keys[scope]
				if !key.InConfig || !key.DeletedAt.IsZero() || key.PlanID != "p" || key.Cycles["default"] != cycle {
					t.Fatalf("restored key = %+v, want original binding and cycle %+v", key, cycle)
				}
				if store.Authorize(scope, now).Allowed {
					t.Fatal("restoring a key replenished its quota")
				}
				if len(mustRequestEvents(t, store, RequestEventQuery{}).Entries) != 1 {
					t.Fatal("request history was lost")
				}
				now = now.Add(time.Hour)
				if allowed := store.Authorize(scope, now).Allowed; allowed != (period == 3600) {
					t.Fatalf("allowed after an hour = %v for period %d", allowed, period)
				}
			})
		}
	}
}

func TestSyncKeysMarksDeletedOnlyOnceAndSparesTrafficOnlyPrincipals(t *testing.T) {
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
	if result.Deleted != 0 {
		t.Fatalf("SyncResult = %+v, want an already deleted key counted once", result)
	}
	store.Read(func(state *State) {
		foreign := state.Keys["foreign-principal"]
		if foreign == nil || !foreign.DeletedAt.IsZero() || foreign.InConfig {
			t.Fatalf("foreign principal = %+v, want it untouched", foreign)
		}
	})
}

func TestPlanEditCanKeepOrUnbindDeletedKeys(t *testing.T) {
	now := time.Date(2026, 8, 12, 15, 57, 0, 0, time.UTC)
	clock := now
	store := newSyncStore(t, &clock)
	scope := CallerScope(deletedKeyPlaintext)
	if _, errSync := store.SyncKeys([]string{keptKeyPlaintext}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}

	selected := []string{CallerScope(keptKeyPlaintext), scope}
	if _, errUpdate := store.UpdatePlanWithBindings(PlanPatch{ID: "p"}, &selected); errUpdate != nil {
		t.Fatalf("UpdatePlanWithBindings error = %v", errUpdate)
	}
	store.Read(func(state *State) {
		if state.Keys[scope].PlanID != "p" {
			t.Fatalf("deleted key = %+v, want its binding kept", state.Keys[scope])
		}
	})

	if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: "p"}, nil); err != nil {
		t.Fatal(err)
	}
	store.Read(func(state *State) {
		if state.Keys[scope].PlanID != "p" {
			t.Fatal("omitting scopes changed a binding")
		}
	})
	selected = []string{CallerScope(keptKeyPlaintext)}
	if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: "p"}, &selected); err != nil {
		t.Fatal(err)
	}
	store.Read(func(state *State) {
		key := state.Keys[scope]
		if key.PlanID != "" || key.Cycles["default"] != (QuotaCycle{}) || key.DeletedAt.IsZero() {
			t.Fatalf("deleted key was not unbound: %+v", key)
		}
	})
	selected = append(selected, scope)
	if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: "p"}, &selected); err == nil {
		t.Fatal("rebound a deleted key through plan editing")
	}

	if errBind := store.BindKey(scope, "p"); errBind == nil {
		t.Fatal("a deleted key was bindable through the management API")
	}
	if err := store.SetLabel(scope, "Historical key"); err != nil {
		t.Fatal(err)
	}
	views := store.KeyViews()
	if len(views) != 2 {
		t.Fatalf("missing retained identity: %+v", views)
	}
	for _, key := range views {
		if key.Scope == scope && (key.Label != "Historical key" || key.DeletedAt.IsZero()) {
			t.Fatalf("deleted key lost its display identity: %+v", key)
		}
	}
}

func TestWindowEditsAndQuotaReset(t *testing.T) {
	for _, unified := range []bool{false, true} {
		t.Run(fmt.Sprint(unified), func(t *testing.T) {
			now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
			store := newAccountStore(t, now)
			store.now = func() time.Time { return now }
			store.ReplaceAll(func(state *State) {
				state.Keys["live"] = &KeyState{}
				state.Keys["deleted"] = &KeyState{}
			})
			windows := []QuotaWindow{
				{Name: "Budget", AmountUSD: 10, PeriodSeconds: 86400},
				{Name: "Short", AmountUSD: 5, PeriodSeconds: 3600},
			}
			if unified {
				for i := range windows {
					windows[i].CycleAnchorAt = now.Add(time.Duration(windows[i].PeriodSeconds) * time.Second)
				}
			}
			plan, err := store.CreatePlanWithBindings(Plan{Name: "Team", Windows: windows}, []string{"live", "deleted"})
			if err != nil {
				t.Fatal(err)
			}
			for _, scope := range []string{"live", "deleted"} {
				store.RecordUsage(admittedEvent(store, scope, now))
			}
			store.ReplaceAll(func(state *State) { state.Keys["deleted"].DeletedAt = now })
			short, long := plan.Windows[0].ID, plan.Windows[1].ID
			if err := store.BindKey("live", plan.ID); err != nil {
				t.Fatal(err)
			}
			plan.Windows[0].Name = "Renamed"
			plan.Windows[0].AmountUSD = wantSubsetCost / 2
			if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: plan.ID, Windows: &plan.Windows}, nil); err != nil {
				t.Fatal(err)
			}
			if store.Authorize("live", now).Allowed {
				t.Fatal("lowered amount did not block")
			}
			plan.Windows[0].PeriodSeconds = 7200
			if unified {
				plan.Windows[0].CycleAnchorAt = now.Add(90 * time.Minute)
			}
			if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: plan.ID, Windows: &plan.Windows}, nil); err != nil {
				t.Fatal(err)
			}
			for _, key := range store.state.Keys {
				if _, exists := key.Cycles[short]; exists || key.Cycles[long].SpentUSD != wantSubsetCost {
					t.Fatalf("window edit reset other usage: %+v", key.Cycles)
				}
			}
			if _, err := store.ResetQuota(ResetRequest{Mode: "all", Scopes: []string{"live", "deleted"}}); err == nil {
				t.Fatal("deleted key accepted in reset")
			}
			if len(store.state.Keys["live"].Cycles) != 1 {
				t.Fatal("invalid batch partially applied")
			}
			before := store.state.Keys["live"].Cycles[long]
			now = now.Add(time.Minute)
			result, err := store.ResetQuota(ResetRequest{Mode: "global"})
			if err != nil || result.Keys != 1 || result.Windows != 1 || len(store.state.Keys["live"].Cycles) != 0 || len(store.state.Keys["deleted"].Cycles) != 1 {
				t.Fatalf("global reset: %+v, %v", result, err)
			}
			if unified {
				view, _ := store.KeyViewForScope("live")
				after := view.Windows[1]
				if !after.StartAt.Equal(before.StartAt) || !after.EndAt.Equal(before.EndAt) || after.Dimensions[0].Used != "0" {
					t.Fatalf("reset moved unified schedule: %+v", after)
				}
			}
			plan.Windows = plan.Windows[:1]
			plan.Windows = append(plan.Windows, QuotaWindow{Name: "New budget", AmountUSD: 20, PeriodSeconds: 86400})
			if unified {
				plan.Windows[1].CycleAnchorAt = now.Add(24 * time.Hour)
			}
			updated, err := store.UpdatePlanWithBindings(PlanPatch{ID: plan.ID, Windows: &plan.Windows}, nil)
			if err != nil || updated.Windows[1].ID == long || len(store.state.Keys["deleted"].Cycles) != 0 {
				t.Fatalf("window replacement: %+v, %v", updated, err)
			}

			if unified {
				store.Authorize("live", now)
				changed := append([]QuotaWindow(nil), updated.Windows...)
				changed[0].CycleAnchorAt = now.Add(45 * time.Minute)
				if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: plan.ID, Windows: &changed}, nil); err != nil {
					t.Fatal(err)
				}
				if _, exists := store.state.Keys["live"].Cycles[short]; exists {
					t.Fatal("schedule edit retained old consumption")
				}
				if _, exists := store.state.Keys["live"].Cycles[changed[1].ID]; !exists {
					t.Fatal("schedule edit reset an unrelated window")
				}
				for i := range changed {
					changed[i].CycleAnchorAt = time.Time{}
				}
				if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: plan.ID, Windows: &changed}, nil); err != nil {
					t.Fatal(err)
				}
				if len(store.state.Keys["live"].Cycles) != 0 {
					t.Fatal("mode change retained cycles")
				}
				if view, _ := store.KeyViewForScope("live"); view.Windows[0].Started {
					t.Fatal("independent mode did not return to first admission")
				}
			}
		})
	}
}

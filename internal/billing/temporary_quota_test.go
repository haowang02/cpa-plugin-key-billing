package billing

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func temporaryQuotaRequest(t *testing.T, store *Store, quota TemporaryQuota) TemporaryQuotaRequest {
	t.Helper()
	view, ok := store.KeyViewForScope("s")
	if !ok {
		t.Fatal("missing key")
	}
	return TemporaryQuotaRequest{Scope: "s", PlanID: "p", WindowID: "w", Revision: view.Windows[0].CreditRevision, Quota: &quota}
}

func setTemporaryQuota(t *testing.T, store *Store, quota TemporaryQuota) QuotaWindowView {
	t.Helper()
	view, err := store.SetTemporaryQuota(temporaryQuotaRequest(t, store, quota))
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func TestTemporaryQuotaRaisesOnlySelectedKeyAndWindow(t *testing.T) {
	store, _, now := quotaStore(t, QuotaWindow{AmountUSD: 600, TokenLimit: 100, RequestLimit: 10})
	store.ReplaceAll(func(state *State) {
		state.Plans[0].Windows = append(state.Plans[0].Windows, QuotaWindow{ID: "other", Name: "周限", AmountUSD: 800, PeriodSeconds: 604800})
		state.Keys["friend"] = &KeyState{PlanID: "p", Preview: "sk-tes…0002"}
		cycle := state.Keys["s"].Cycles["w"]
		cycle.SpentUSD, cycle.UsedTokens, cycle.UsedRequests = 107.18, 20, 2
		state.Keys["s"].Cycles["w"] = cycle
	})
	before := currentQuotaCycle(t, store)
	view := setTemporaryQuota(t, store, TemporaryQuota{AmountUSD: 100, TokenLimit: 50, RequestLimit: 5})
	for i, want := range []struct{ limit, remaining string }{{"700", "592.82"}, {"150", "130"}, {"15", "13"}} {
		gotRemaining, _ := view.Dimensions[i].Remaining.Float64()
		wantRemaining, _ := json.Number(want.remaining).Float64()
		if view.Dimensions[i].Limit != json.Number(want.limit) || math.Abs(gotRemaining-wantRemaining) > 1e-9 || view.Dimensions[i].TemporaryLimit == "" {
			t.Fatalf("balance %d = %+v", i, view.Dimensions[i])
		}
	}
	after := currentQuotaCycle(t, store)
	after.TemporaryQuota, after.CreditSequence = TemporaryQuota{}, before.CreditSequence
	if after != before {
		t.Fatalf("credit changed usage or schedule: %+v -> %+v", before, after)
	}
	key, _ := store.KeyViewForScope("s")
	friend := store.Authorize("friend", now)
	if key.Windows[1].Dimensions[0].Limit != "800" || friend.Windows[0].Dimensions[0].Limit != "600" || store.Plans()[0].Windows[0].AmountUSD != 600 {
		t.Fatal("credit escaped the selected key/window")
	}
	store.ReplaceAll(func(state *State) {
		state.Keys["s"].Cycles["other"] = QuotaCycle{PlanID: "p", StartAt: now, EndAt: now.Add(7 * 24 * time.Hour), SpentUSD: 800}
	})
	if store.Authorize("s", now).Allowed {
		t.Fatal("credit bypassed another exhausted window")
	}
}

func TestTemporaryQuotaReplacementAndRevocationKeepUsage(t *testing.T) {
	store, repo, now := quotaStore(t, QuotaWindow{AmountUSD: 600})
	store.RecordUsage(subsetEvent("s", now))
	setTemporaryQuota(t, store, TemporaryQuota{AmountUSD: 100})
	view := setTemporaryQuota(t, store, TemporaryQuota{AmountUSD: 150})
	if view.Dimensions[0].Limit != "750" {
		t.Fatal("replacement accumulated instead of setting")
	}
	store.ReplaceAll(func(state *State) {
		cycle := state.Keys["s"].Cycles["w"]
		cycle.SpentUSD = 650
		state.Keys["s"].Cycles["w"] = cycle
	})
	if !store.Authorize("s", now).Allowed {
		t.Fatal("temporary quota did not authorize over-base usage")
	}
	view = setTemporaryQuota(t, store, TemporaryQuota{})
	balance := view.Dimensions[0]
	if !view.Blocked || balance.Limit != "600" || balance.Used != "650" || balance.Remaining != "0" || balance.TemporaryLimit != "" ||
		len(repo.requestEvents) != 1 || store.Authorize("s", now).Allowed {
		t.Fatalf("revocation changed accounting: %+v", view)
	}
}

func TestTemporaryQuotaEndsWithItsCycle(t *testing.T) {
	for _, operation := range []string{"expire", "reset", "global", "unbind", "rebind", "delete-plan", "change-period", "change-anchor", "remove-window"} {
		t.Run(operation, func(t *testing.T) {
			store, _, now := quotaStore(t, QuotaWindow{AmountUSD: 600, CycleAnchorAt: time.Date(2026, 9, 8, 13, 0, 0, 0, time.UTC)})
			setTemporaryQuota(t, store, TemporaryQuota{AmountUSD: 100})
			var err error
			switch operation {
			case "expire":
				store.now = func() time.Time { return now.Add(time.Hour) }
				store.KeyViews()
			case "reset", "global":
				req := ResetRequest{Mode: "all", Scopes: []string{"s"}}
				if operation == "global" {
					req = ResetRequest{Mode: "global"}
				}
				_, err = store.ResetQuota(req)
			case "unbind":
				err = store.UnbindKey("s")
			case "rebind":
				store.ReplaceAll(func(state *State) {
					state.Plans = append(state.Plans, Plan{ID: "other", Windows: []QuotaWindow{{ID: "new", Name: "新计划", AmountUSD: 600, PeriodSeconds: 3600}}})
				})
				err = store.BindKey("s", "other")
			case "delete-plan":
				_, err = store.DeletePlan("p")
			default:
				windows := store.Plans()[0].Windows
				switch operation {
				case "change-period":
					windows[0].PeriodSeconds = 7200
				case "change-anchor":
					windows[0].CycleAnchorAt = now.Add(30 * time.Minute)
				case "remove-window":
					windows = []QuotaWindow{{Name: "新窗口", AmountUSD: 600, PeriodSeconds: 3600}}
				}
				_, err = store.UpdatePlanWithBindings(PlanPatch{ID: "p", Windows: &windows}, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if cycle := currentQuotaCycle(t, store); cycle.TemporaryQuota != (TemporaryQuota{}) {
				t.Fatalf("credit survived %s: %+v", operation, cycle)
			}
		})
	}
}

func TestTemporaryQuotaRespectsUnstartedWindowsAndFixedSchedule(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		store, _, now := quotaStore(t, QuotaWindow{AmountUSD: 600})
		store.ReplaceAll(func(state *State) {
			state.Keys["s"].Cycles = nil
			if fixed {
				state.Plans[0].Windows[0].CycleAnchorAt = now.Add(time.Hour)
			}
		})
		req := temporaryQuotaRequest(t, store, TemporaryQuota{AmountUSD: 100})
		if !fixed {
			if req.Revision != "" {
				t.Fatal("unstarted independent cycle is editable")
			}
			req.Revision = "stale"
			if _, err := store.SetTemporaryQuota(req); KindOf(err) != KindConflict || !currentQuotaCycle(t, store).StartAt.IsZero() {
				t.Fatal("credit started an independent window")
			}
			continue
		}
		store.now = func() time.Time { return now.Add(10 * time.Minute) }
		if refreshed := temporaryQuotaRequest(t, store, *req.Quota); refreshed.Revision != req.Revision {
			t.Fatal("unstarted fixed-window revision changes with wall time")
		}
		view, err := store.SetTemporaryQuota(req)
		if err != nil || !view.EndAt.Equal(now.Add(time.Hour)) || !view.StartAt.Equal(now) {
			t.Fatalf("credit moved the fixed schedule: %+v, %v", view, err)
		}
		store.now = func() time.Time { return now.Add(time.Hour) }
		if d := store.Authorize("s", store.Now()); !d.Allowed || d.Windows[0].Dimensions[0].Limit != "600" {
			t.Fatalf("credit leaked into next fixed cycle: %+v", d)
		}
		store.RecordUsage(subsetEvent("s", now.Add(20*time.Minute)))
		if currentQuotaCycle(t, store).SpentUSD != 0 {
			t.Fatal("late usage charged the replacement cycle")
		}
	}
}

func TestTemporaryQuotaRejectsStaleEditsButNotUsage(t *testing.T) {
	store, _, now := quotaStore(t, QuotaWindow{AmountUSD: 600, CycleAnchorAt: time.Date(2026, 9, 8, 13, 0, 0, 0, time.UTC)})
	req := temporaryQuotaRequest(t, store, TemporaryQuota{AmountUSD: 100})
	store.RecordUsage(subsetEvent("s", now))
	if _, err := store.SetTemporaryQuota(req); err != nil {
		t.Fatalf("ordinary usage invalidated the form: %v", err)
	}
	if _, err := store.SetTemporaryQuota(req); KindOf(err) != KindConflict {
		t.Fatal("stale concurrent credit edit was accepted")
	}
	req = temporaryQuotaRequest(t, store, TemporaryQuota{AmountUSD: 200})
	store.now = func() time.Time { return now.Add(time.Minute) }
	if _, err := store.ResetQuota(ResetRequest{Mode: "all", Scopes: []string{"s"}}); err != nil {
		t.Fatal(err)
	}
	store.Authorize("s", store.Now())
	if _, err := store.SetTemporaryQuota(req); KindOf(err) != KindConflict {
		t.Fatal("pre-reset form wrote into a new cycle with the same end time")
	}
	req = temporaryQuotaRequest(t, store, TemporaryQuota{AmountUSD: 200})
	store.now = func() time.Time { return now.Add(time.Hour) }
	if _, err := store.SetTemporaryQuota(req); KindOf(err) != KindConflict {
		t.Fatal("expired form wrote into the next fixed cycle")
	}
}

func TestTemporaryQuotaValidationAndWriteRollback(t *testing.T) {
	invalid := []TemporaryQuota{
		{AmountUSD: -1}, {AmountUSD: math.NaN()}, {AmountUSD: math.Inf(1)},
		{TokenLimit: -1}, {RequestLimit: -1}, {TokenLimit: maxQuotaCount}, {RequestLimit: maxQuotaCount},
	}
	for _, quota := range invalid {
		store, _, _ := quotaStore(t, QuotaWindow{AmountUSD: 600, TokenLimit: 1, RequestLimit: 1})
		before := currentQuotaCycle(t, store)
		if _, err := store.SetTemporaryQuota(temporaryQuotaRequest(t, store, quota)); KindOf(err) != KindInvalid || currentQuotaCycle(t, store) != before {
			t.Fatalf("invalid credit was accepted: %+v, %v", quota, err)
		}
	}
	store, repo, _ := quotaStore(t, QuotaWindow{AmountUSD: math.MaxFloat64})
	for _, quota := range []TemporaryQuota{{AmountUSD: math.MaxFloat64}, {TokenLimit: 1}, {RequestLimit: 1}} {
		if _, err := store.SetTemporaryQuota(temporaryQuotaRequest(t, store, quota)); KindOf(err) != KindInvalid {
			t.Fatalf("overflow/disabled dimension accepted: %+v", quota)
		}
	}
	req := temporaryQuotaRequest(t, store, TemporaryQuota{AmountUSD: 1})
	repo.fail = errors.New("dummy disk failure")
	if _, err := store.SetTemporaryQuota(req); err == nil || currentQuotaCycle(t, store).TemporaryQuota != (TemporaryQuota{}) {
		t.Fatal("failed write published a credit")
	}
	repo.fail = nil
	if _, err := store.SetTemporaryQuota(req); err != nil {
		t.Fatalf("failed write invalidated the retry revision: %v", err)
	}
	for _, modify := range []func(*TemporaryQuotaRequest){
		func(r *TemporaryQuotaRequest) { r.Quota = nil },
		func(r *TemporaryQuotaRequest) { r.Scope = "" },
		func(r *TemporaryQuotaRequest) { r.Revision = "" },
		func(r *TemporaryQuotaRequest) { r.PlanID = "other" },
		func(r *TemporaryQuotaRequest) { r.WindowID = "other" },
	} {
		r := temporaryQuotaRequest(t, store, TemporaryQuota{})
		modify(&r)
		if _, err := store.SetTemporaryQuota(r); err == nil {
			t.Fatal("incomplete or mismatched target accepted")
		}
	}
	req = temporaryQuotaRequest(t, store, TemporaryQuota{})
	store.ReplaceAll(func(state *State) { state.Keys["s"].DeletedAt = time.Now() })
	if _, err := store.SetTemporaryQuota(req); KindOf(err) != KindNotFound {
		t.Fatal("deleted key was editable")
	}
}

func TestTemporaryQuotaFollowsWindowIdentityAndLimitEdits(t *testing.T) {
	store, _, _ := quotaStore(t, QuotaWindow{AmountUSD: 600, TokenLimit: 10, RequestLimit: 10})
	setTemporaryQuota(t, store, TemporaryQuota{AmountUSD: 100, TokenLimit: 20, RequestLimit: 30})
	req := temporaryQuotaRequest(t, store, TemporaryQuota{AmountUSD: 150, TokenLimit: 20, RequestLimit: 30})
	windows := store.Plans()[0].Windows
	windows[0].Name = "重命名后的周限"
	if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: "p", Windows: &windows}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTemporaryQuota(req); err != nil {
		t.Fatalf("rename changed the target identity: %v", err)
	}
	req = temporaryQuotaRequest(t, store, TemporaryQuota{AmountUSD: 200})
	windows[0].AmountUSD, windows[0].TokenLimit = 700, 0
	if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: "p", Windows: &windows}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTemporaryQuota(req); KindOf(err) != KindConflict {
		t.Fatal("stale plan-limit form was accepted")
	}
	credit := currentQuotaCycle(t, store).TemporaryQuota
	if credit != (TemporaryQuota{AmountUSD: 150, RequestLimit: 30}) {
		t.Fatalf("limit edits lost credit or kept a disabled dimension: %+v", credit)
	}
	before := store.Plans()
	windows[0].RequestLimit = maxQuotaCount
	if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: "p", Windows: &windows}, nil); KindOf(err) != KindInvalid || !reflect.DeepEqual(before, store.Plans()) {
		t.Fatal("combined quota overflow was not rolled back")
	}
}

func TestTemporaryQuotaConcurrentEditsAreNotAccumulated(t *testing.T) {
	store, _, _ := quotaStore(t, QuotaWindow{AmountUSD: 600})
	req := temporaryQuotaRequest(t, store, TemporaryQuota{AmountUSD: 100})
	var successes atomic.Int32
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := store.SetTemporaryQuota(req); err == nil {
				successes.Add(1)
			} else if KindOf(err) != KindConflict {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	group.Wait()
	if successes.Load() != 1 || currentQuotaCycle(t, store).TemporaryQuota.AmountUSD != 100 {
		t.Fatal("concurrent stale forms overwrote or accumulated credit")
	}
}

func TestTemporaryQuotaRevisionRejectsABA(t *testing.T) {
	store, _, _ := quotaStore(t, QuotaWindow{AmountUSD: 600})
	stale := temporaryQuotaRequest(t, store, TemporaryQuota{AmountUSD: 150})
	setTemporaryQuota(t, store, TemporaryQuota{AmountUSD: 100})
	setTemporaryQuota(t, store, TemporaryQuota{})
	if _, err := store.SetTemporaryQuota(stale); KindOf(err) != KindConflict {
		t.Fatalf("stale form after change-and-revert was accepted: %v", err)
	}
}

func TestTemporaryQuotaFixedWindowFirstAdmissionKeepsRevision(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store, _ := newAccountStoreWithRepository(t, now)
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{{ID: "p", Windows: []QuotaWindow{{ID: "w", Name: "额度", PeriodSeconds: 3600, AmountUSD: 600, CycleAnchorAt: now.Add(time.Hour)}}}}
		state.Keys["s"] = &KeyState{PlanID: "p", Preview: "sk-tes…0001"}
	})
	req := temporaryQuotaRequest(t, store, TemporaryQuota{AmountUSD: 100})
	if !store.Authorize("s", now).Allowed {
		t.Fatal("first admission unexpectedly denied")
	}
	view, err := store.SetTemporaryQuota(req)
	if err != nil {
		t.Fatalf("ordinary first admission invalidated the form: %v", err)
	}
	if view.Dimensions[0].Limit != "700" || view.Dimensions[0].TemporaryLimit != "100" {
		t.Fatalf("temporary quota was not applied: %+v", view)
	}
}

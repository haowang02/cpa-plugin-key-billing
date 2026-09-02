package billing

import "testing"

func TestConcurrencySlotsEnforceLimitAndAreIdempotent(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) {
		state.Keys["scope-a"] = &KeyState{ConcurrencyLimit: 2}
	})

	first := store.AcquireSlot("scope-a", "request-1")
	second := store.AcquireSlot("scope-a", "request-2")
	blocked := store.AcquireSlot("scope-a", "request-3")
	if !first.Allowed || first.Active != 1 || !second.Allowed || second.Active != 2 {
		t.Fatalf("admissions = %+v / %+v", first, second)
	}
	if blocked.Allowed || blocked.Active != 2 || blocked.Limit != 2 || blocked.Acquired {
		t.Fatalf("blocked = %+v, want a saturated two-slot key", blocked)
	}
	if duplicate := store.AcquireSlot("scope-a", "request-2"); !duplicate.Allowed || duplicate.Active != 2 || duplicate.Acquired {
		t.Fatalf("duplicate = %+v, want an idempotent reservation", duplicate)
	}
	if store.ReleaseSlot("missing") || !store.ReleaseSlot("request-1") || store.ReleaseSlot("request-1") {
		t.Fatal("slot release was not idempotent")
	}
	if admitted := store.AcquireSlot("scope-a", "request-3"); !admitted.Allowed || admitted.Active != 2 {
		t.Fatalf("admitted after release = %+v", admitted)
	}
}

func TestUnlimitedRequestsCountWhenLimitIsLowered(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) {
		state.Keys["scope-a"] = &KeyState{InConfig: true}
	})
	if decision := store.AcquireSlot("scope-a", "request-1"); !decision.Allowed || decision.Active != 1 {
		t.Fatalf("unlimited admission = %+v", decision)
	}
	if errSet := store.SetConcurrencyLimit("scope-a", 1); errSet != nil {
		t.Fatalf("SetConcurrencyLimit error = %v", errSet)
	}
	if decision := store.AcquireSlot("scope-a", "request-2"); decision.Allowed || decision.Active != 1 {
		t.Fatalf("lowered limit decision = %+v", decision)
	}
	store.ReleaseSlot("request-1")
	if decision := store.AcquireSlot("scope-a", "request-2"); !decision.Allowed {
		t.Fatalf("admission after release = %+v", decision)
	}
}

package billing

import (
	"strings"
	"time"
)

type Decision struct {
	Allowed  bool
	PlanID   string
	PlanName string
	QuotaView
}

func (s *Store) Authorize(scope string, at time.Time) Decision {
	allowed := Decision{Allowed: true}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return allowed
	}
	if at.IsZero() {
		at = s.Now()
	}
	decision := updateResult(s, func(state *State) (Decision, Changes) {
		key := state.Keys[scope]
		if key == nil || key.PlanID == "" {
			return allowed, Changes{}
		}
		touched := Changes{Keys: []string{scope}}
		plan, ok := state.FindPlan(key.PlanID)
		if !ok {
			key.PlanID, key.Cycles = "", nil
			return allowed, touched
		}
		var changed Changes
		if settleExpiredCycles(key, at) {
			changed = touched
		}
		view := quotaView(key, plan, at)
		if !view.Blocked && activateCycles(key, plan, at) {
			changed = touched
			view = quotaView(key, plan, at)
		}
		return Decision{Allowed: !view.Blocked, PlanID: plan.ID, PlanName: plan.Name, QuotaView: view}, changed
	})
	if decision.Allowed {
		s.blocked.clear(scope)
	}
	return decision
}

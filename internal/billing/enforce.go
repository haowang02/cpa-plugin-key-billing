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
		plan := state.FindPlan(key.PlanID)
		if plan == nil {
			return allowed, Changes{}
		}
		changed := settleQuota(scope, key, plan, at)
		view := quotaView(key.Cycles, plan, s.cfg.CNYPerUSD, at)
		if !view.Blocked {
			if key.Cycles.activate(plan, at) {
				changed.Keys = []string{scope}
			}
			if plan.SharedQuota && plan.Cycles.activate(plan, at) {
				changed.PlanCycles = []string{plan.ID}
			}
			view = quotaView(key.Cycles, plan, s.cfg.CNYPerUSD, at)
		}
		return Decision{Allowed: !view.Blocked, PlanID: plan.ID, PlanName: plan.Name, QuotaView: view}, changed
	})
	if decision.Allowed {
		s.blocked.clear(scope)
	}
	return decision
}

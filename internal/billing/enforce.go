package billing

import (
	"strings"
	"time"
)

type Decision struct {
	Allowed      bool
	PlanID       string
	PlanName     string
	LimitUSD     float64
	SpentUSD     float64
	CycleStartAt time.Time
	ResetAt      time.Time
}

// It fails open by design. Anything the plugin cannot resolve — an
// unattributable request, an unknown key, no subscription, or a deleted plan —
// is allowed through. A billing plugin that starts
// rejecting traffic because of its own missing state would be worse than one
// that briefly under-charges.
//
// The first admitted use starts a key-relative period. An elapsed period is
// closed and a fresh one starts at this use; idle time never consumes a new
// period.
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
			// The bound plan was deleted; treat the key as unlimited and drop
			// the stale window instead of blocking it forever.
			key.PlanID = ""
			key.Cycle = Cycle{}
			return allowed, touched
		}
		var changed Changes
		if activateCycle(key, plan, at) {
			changed = touched
		}
		current := Decision{
			Allowed:      true,
			PlanID:       plan.ID,
			PlanName:     plan.Name,
			LimitUSD:     plan.AmountUSD,
			SpentUSD:     key.Cycle.SpentUSD,
			CycleStartAt: key.Cycle.StartAt,
			ResetAt:      key.Cycle.EndAt,
		}
		if key.Cycle.SpentUSD < plan.AmountUSD {
			return current, changed
		}
		current.Allowed = false
		return current, changed
	})
	return decision
}

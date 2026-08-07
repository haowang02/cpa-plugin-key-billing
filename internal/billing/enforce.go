package billing

import (
	"strings"
	"time"
)

// DenyReason explains why a request was refused.
type DenyReason string

// DenyQuotaExhausted means the key's subscription has no budget left in the
// current cycle.
const DenyQuotaExhausted DenyReason = "quota_exhausted"

// Decision is the outcome of an authorization check.
type Decision struct {
	Allowed  bool
	Reason   DenyReason
	PlanID   string
	PlanName string
	LimitUSD float64
	SpentUSD float64
	ResetAt  time.Time
}

// Authorize reports whether a downstream key may issue a request.
//
// It fails open by design. Anything the plugin cannot resolve — an
// unattributable request, an unknown key, no subscription, or a deleted plan —
// is allowed through. A billing plugin that starts
// rejecting traffic because of its own missing state would be worse than one
// that briefly under-charges.
//
// The check also advances the key's cycle, so a window that expired while the
// key was idle is reset here rather than on the next billed request.
func (s *Store) Authorize(scope string, at time.Time) Decision {
	allowed := Decision{Allowed: true}
	if s == nil {
		return allowed
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return allowed
	}
	if at.IsZero() {
		at = s.Now()
	}
	s.authChecks.Add(1)

	decision := UpdateResult(s, func(state *State) (Decision, bool) {
		key := state.Keys[scope]
		if key == nil || key.PlanID == "" {
			return allowed, false
		}
		plan, ok := state.FindPlan(key.PlanID)
		if !ok {
			// The bound plan was deleted; treat the key as unlimited and drop
			// the stale window instead of blocking it forever.
			key.PlanID = ""
			key.Cycle = Cycle{}
			return allowed, true
		}
		changed := rollCycle(key, plan, at, s.locationLocked())
		if key.Cycle.SpentUSD < plan.AmountUSD {
			return allowed, changed
		}
		return Decision{
			Allowed:  false,
			Reason:   DenyQuotaExhausted,
			PlanID:   plan.ID,
			PlanName: plan.Name,
			LimitUSD: plan.AmountUSD,
			SpentUSD: key.Cycle.SpentUSD,
			ResetAt:  key.Cycle.EndAt,
		}, changed
	})
	if !decision.Allowed {
		s.authBlocked.Add(1)
	}
	return decision
}

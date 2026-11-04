package billing

import (
	"math"
	"slices"
	"time"
)

type QuotaCycles map[string]QuotaCycle

// UsageSince excludes requests admitted before binding or an administrative reset.
type QuotaCycle struct {
	PlanID     string    `json:"plan_id,omitempty"`
	StartAt    time.Time `json:"start_at,omitzero"`
	EndAt      time.Time `json:"end_at,omitzero"`
	UsageSince time.Time `json:"usage_since,omitzero"`
	Spending
	UsedTokens   int64 `json:"used_tokens"`
	UsedRequests int64 `json:"used_requests"`
}

func (w QuotaWindow) newCycle(planID string, now time.Time) QuotaCycle {
	cycle := QuotaCycle{PlanID: planID, StartAt: now}
	if !w.CycleAnchorAt.IsZero() {
		// Reduce seconds before converting to Duration, including times before the anchor.
		offset := (now.Unix() - w.CycleAnchorAt.Unix()) % w.PeriodSeconds
		if offset < 0 {
			offset += w.PeriodSeconds
		}
		cycle.StartAt = time.Unix(now.Unix()-offset, 0).UTC()
		cycle.UsageSince = now
	}
	cycle.EndAt = cycle.StartAt.Add(time.Duration(w.PeriodSeconds) * time.Second)
	return cycle
}

// Expiration never starts another window; only admission can do that.
func (cycles QuotaCycles) settle(now time.Time) bool {
	changed := false
	for id, cycle := range cycles {
		if !now.Before(cycle.EndAt) {
			delete(cycles, id)
			changed = true
		}
	}
	return changed
}

func (cycles *QuotaCycles) activate(plan *Plan, now time.Time) bool {
	if *cycles == nil {
		*cycles = make(QuotaCycles)
	}
	changed := false
	for _, window := range plan.Windows {
		if _, exists := (*cycles)[window.ID]; !exists {
			(*cycles)[window.ID] = window.newCycle(plan.ID, now)
			changed = true
		}
	}
	return changed
}

func (key *KeyState) ValidateCycles(plan *Plan) error {
	if key.PlanID != "" && (plan == nil || plan.ID != key.PlanID) {
		return invalidf("The subscription plan bound to this API key does not exist")
	}
	return key.Cycles.Validate(plan)
}

func (plan *Plan) ValidateCycles() error {
	if !plan.SharedQuota && len(plan.Cycles) != 0 {
		return invalidf("Only shared quota plans can hold quota cycles")
	}
	return plan.Cycles.Validate(plan)
}

func (cycles QuotaCycles) Validate(plan *Plan) error {
	for id, cycle := range cycles {
		if plan == nil {
			return invalidf("Invalid quota cycle data")
		}
		index := slices.IndexFunc(plan.Windows, func(window QuotaWindow) bool { return window.ID == id })
		if index < 0 || cycle.PlanID != plan.ID || cycle.StartAt.IsZero() || cycle.EndAt.IsZero() ||
			!cycle.EndAt.Equal(cycle.StartAt.Add(time.Duration(plan.Windows[index].PeriodSeconds)*time.Second)) ||
			!cycle.Spending.valid() || cycle.UsedRequests < 0 || cycle.UsedTokens < 0 {
			return invalidf("Invalid quota cycle data")
		}
		window := plan.Windows[index]
		if !window.CycleAnchorAt.IsZero() && !window.newCycle(plan.ID, cycle.StartAt).StartAt.Equal(cycle.StartAt) ||
			!cycle.UsageSince.IsZero() && (cycle.UsageSince.Before(cycle.StartAt) || !cycle.UsageSince.Before(cycle.EndAt)) {
			return invalidf("Invalid quota cycle data")
		}
	}
	return nil
}

// Key cycles bound shared charges to requests admitted during this binding.
// Pool totals survive member removal and are never reconstructed from history.
func chargeQuota(key *KeyState, plan *Plan, at time.Time, usage quotaUsage) bool {
	sharedChanged := false
	for id, cycle := range key.Cycles {
		if !cycle.accepts(key.PlanID, at) {
			continue
		}
		key.Cycles[id] = cycle.charge(usage)
		if plan != nil && plan.SharedQuota {
			shared := plan.Cycles[id]
			if shared.accepts(plan.ID, at) {
				plan.Cycles[id] = shared.charge(usage)
				sharedChanged = true
			}
		}
	}
	return sharedChanged
}

func (cycle QuotaCycle) accepts(planID string, at time.Time) bool {
	return planID != "" && cycle.PlanID == planID && !at.IsZero() &&
		!at.Before(cycle.StartAt) && !at.Before(cycle.UsageSince) && at.Before(cycle.EndAt)
}

func (cycle QuotaCycle) charge(usage quotaUsage) QuotaCycle {
	// Retain disabled dimensions so editing limits preserves current usage.
	cycle.Spending = cycle.Spending.add(usage.Spending)
	cycle.UsedTokens = addQuotaCount(cycle.UsedTokens, usage.Tokens)
	cycle.UsedRequests = addQuotaCount(cycle.UsedRequests, usage.Requests)
	return cycle
}

func settleQuota(scope string, key *KeyState, plan *Plan, now time.Time) Changes {
	changes := Changes{}
	if key.Cycles.settle(now) {
		changes.Keys = []string{scope}
	}
	if plan != nil && plan.Cycles.settle(now) {
		changes.PlanCycles = []string{plan.ID}
	}
	return changes
}

func addQuotaCount(current, delta int64) int64 {
	if delta > math.MaxInt64-current {
		return math.MaxInt64
	}
	return current + delta
}

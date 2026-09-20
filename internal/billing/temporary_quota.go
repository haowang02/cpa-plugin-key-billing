package billing

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"time"
)

// TemporaryQuota raises enabled limits for one key's current window only.
// It is part of QuotaCycle, so expiry, reset and rebinding discard it together
// with that cycle. Usage remains a single, unmodified set of counters.
type TemporaryQuota struct {
	AmountUSD    float64 `json:"amount_usd"`
	TokenLimit   int64   `json:"token_limit"`
	RequestLimit int64   `json:"request_limit"`
}

func (q TemporaryQuota) validate(window QuotaWindow) error {
	for _, dim := range quotaDimensions {
		if err := dim.validateTemporary(window, q); err != nil {
			return err
		}
	}
	return nil
}

// A missing quota is not a revoke request. Send an explicit zero-valued object
// to clear all three temporary limits for this window.
type TemporaryQuotaRequest struct {
	Scope    string          `json:"scope"`
	PlanID   string          `json:"plan_id"`
	WindowID string          `json:"window_id"`
	Revision string          `json:"revision"`
	Quota    *TemporaryQuota `json:"quota"`
}

// SetTemporaryQuota replaces, never increments, the window's temporary limits.
// The revision protects against stale forms spanning a reset, plan edit or
// another administrative credit edit. Ordinary usage does not invalidate it.
func (s *Store) SetTemporaryQuota(req TemporaryQuotaRequest) (QuotaWindowView, error) {
	req.Scope = normalizeScope(req.Scope)
	req.PlanID, req.WindowID = strings.TrimSpace(req.PlanID), strings.TrimSpace(req.WindowID)
	if req.Scope == "" || req.PlanID == "" || req.WindowID == "" || req.Revision == "" || req.Quota == nil {
		return QuotaWindowView{}, invalidf("Specify the API key, subscription plan, quota window, current revision, and temporary credits")
	}
	quota := *req.Quota
	return editConfiguration(s, func(state *State) (QuotaWindowView, Changes, error) {
		key := state.liveKey(req.Scope)
		if key == nil {
			return QuotaWindowView{}, Changes{}, notFoundf("The API key does not exist or has been deleted")
		}
		plan, ok := state.FindPlan(req.PlanID)
		if !ok || key.PlanID != req.PlanID {
			return QuotaWindowView{}, Changes{}, conflictf("The API key subscription plan changed; refresh and try again")
		}
		index := slices.IndexFunc(plan.Windows, func(w QuotaWindow) bool { return w.ID == req.WindowID })
		if index < 0 {
			return QuotaWindowView{}, Changes{}, conflictf("The quota window no longer exists; refresh and try again")
		}
		window := plan.Windows[index]
		if err := quota.validate(window); err != nil {
			return QuotaWindowView{}, Changes{}, err
		}
		now := s.Now()
		settled := settleExpiredCycles(key, now)
		cycle, persisted := key.Cycles[window.ID]
		if !persisted {
			if window.CycleAnchorAt.IsZero() {
				return QuotaWindowView{}, Changes{}, conflictf("The per-key quota cycle has not started or has ended; refresh and try again")
			}
			cycle = window.newCycle(plan.ID, now)
		}
		if req.Revision != quotaCreditRevision(plan.ID, window, cycle, persisted) {
			return QuotaWindowView{}, Changes{}, conflictf("The quota cycle or temporary credits changed; refresh and try again")
		}
		changes := Changes{}
		if settled {
			changes.Keys = []string{req.Scope}
		}
		if quota != cycle.TemporaryQuota {
			cycle.TemporaryQuota = quota
			cycle.CreditSequence++
			if key.Cycles == nil {
				key.Cycles = make(map[string]QuotaCycle)
			}
			key.Cycles[window.ID] = cycle
			changes.Keys = []string{req.Scope}
			persisted = true
		}
		view := window.view(cycle)
		view.CreditRevision = quotaCreditRevision(plan.ID, window, cycle, persisted)
		return view, changes, nil
	})
}

// Include the current fixed interval even before the first request, but not
// its synthetic UsageSince=now. This keeps unopened fixed-window forms stable.
// UTC normalization also keeps revisions stable across database reloads.
func quotaCreditRevision(planID string, window QuotaWindow, cycle QuotaCycle, persisted bool) string {
	if cycle.StartAt.IsZero() {
		return ""
	}
	usageSince := cycle.UsageSince
	if !persisted {
		usageSince = time.Time{}
	}
	timestamp := func(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }
	var b strings.Builder
	fmt.Fprintf(&b, "%q|%q|%d|%s", planID, window.ID, window.PeriodSeconds, timestamp(window.CycleAnchorAt))
	for _, dim := range quotaDimensions {
		dim.writeWindowLimit(&b, window)
	}
	fmt.Fprintf(&b, "|%t|%s|%s|%s|%d", persisted, timestamp(cycle.StartAt), timestamp(cycle.EndAt), timestamp(usageSince), cycle.CreditSequence)
	for _, dim := range quotaDimensions {
		dim.writeTemporaryLimit(&b, cycle.TemporaryQuota)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%x", sum)
}

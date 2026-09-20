package billing

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"math"
	"slices"
	"strings"
	"time"
)

type Plan struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Windows []QuotaWindow `json:"windows"`
}

// Zero disables a quota dimension. An absent anchor starts cycles on admission.
type QuotaWindow struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	PeriodSeconds int64     `json:"period_seconds"`
	AmountUSD     float64   `json:"amount_usd"`
	TokenLimit    int64     `json:"token_limit"`
	RequestLimit  int64     `json:"request_limit"`
	CycleAnchorAt time.Time `json:"cycle_anchor_at,omitzero"`
}

const maxPeriodSeconds = int64(math.MaxInt64) / int64(time.Second)

// Limits travel through browser number inputs and must be exactly representable.
const maxQuotaCount = int64(1<<53 - 1)

func (p Plan) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return invalidf("Subscription plan ID is required")
	}
	if len(p.Windows) == 0 {
		return invalidf("A subscription plan requires at least one quota window")
	}
	ids := make(map[string]bool)
	names := make(map[string]bool)
	periods := make(map[int64]bool)
	for _, window := range p.Windows {
		if window.ID == "" || ids[window.ID] {
			return invalidf("Invalid or duplicate quota window ID")
		}
		name := strings.TrimSpace(window.Name)
		if name == "" || len(name) > maxRouteNameBytes {
			return invalidf("Window name is required and must not exceed %d bytes", maxRouteNameBytes)
		}
		if names[strings.ToLower(name)] {
			return invalidf("Duplicate window name %q", name)
		}
		hasAny := false
		for _, dim := range quotaDimensions {
			if err := dim.validateWindow(name, window); err != nil {
				return err
			}
			if dim.hasLimit(window) {
				hasAny = true
			}
		}
		if !hasAny {
			return invalidf("Window %q: set at least one quota", name)
		}
		if window.PeriodSeconds <= 0 || window.PeriodSeconds > maxPeriodSeconds {
			return invalidf("Window %q: period must be between 1 and %d seconds", name, maxPeriodSeconds)
		}
		if periods[window.PeriodSeconds] {
			return invalidf("Window %q has the same period as another window", name)
		}
		if window.CycleAnchorAt.IsZero() != p.Windows[0].CycleAnchorAt.IsZero() {
			return invalidf("All windows in a subscription plan must use the same cycle mode")
		}
		if !window.CycleAnchorAt.IsZero() && (window.CycleAnchorAt.Year() < 1970 || window.CycleAnchorAt.Year() > 9999 || window.CycleAnchorAt.Nanosecond() != 0) {
			return invalidf("Window %q: cycle start must be between years 1970 and 9999 with second precision", name)
		}
		ids[window.ID], names[strings.ToLower(name)], periods[window.PeriodSeconds] = true, true, true
	}
	return nil
}

func prepareWindows(windows, existing []QuotaWindow, now time.Time) ([]QuotaWindow, error) {
	windows = slices.Clone(windows)
	for i := range windows {
		window := &windows[i]
		window.Name = strings.TrimSpace(window.Name)
		oldIndex := slices.IndexFunc(existing, func(old QuotaWindow) bool { return old.ID == window.ID })
		if window.ID == "" {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return nil, err
			}
			window.ID = hex.EncodeToString(id[:])
		} else if oldIndex < 0 {
			return nil, invalidf("Quota window %q no longer exists; refresh and try again", window.Name)
		}
		if !window.CycleAnchorAt.IsZero() {
			window.CycleAnchorAt = window.CycleAnchorAt.UTC()
			if oldIndex >= 0 && window.sameSchedule(existing[oldIndex]) {
				window.CycleAnchorAt = existing[oldIndex].CycleAnchorAt
			} else if window.PeriodSeconds <= 0 || window.PeriodSeconds > maxPeriodSeconds ||
				!window.CycleAnchorAt.After(now) || window.CycleAnchorAt.After(now.Add(time.Duration(window.PeriodSeconds)*time.Second)) {
				return nil, invalidf("Window %q: the next cycle must start after now and within one period", window.Name)
			}
		}
	}
	slices.SortFunc(windows, func(a, b QuotaWindow) int {
		return cmp.Compare(a.PeriodSeconds, b.PeriodSeconds)
	})
	return windows, nil
}

func (w QuotaWindow) sameSchedule(other QuotaWindow) bool {
	if w.PeriodSeconds != other.PeriodSeconds || w.CycleAnchorAt.IsZero() != other.CycleAnchorAt.IsZero() {
		return false
	}
	return w.CycleAnchorAt.Equal(other.CycleAnchorAt) || w.PeriodSeconds > 0 &&
		w.CycleAnchorAt.Nanosecond() == other.CycleAnchorAt.Nanosecond() &&
		(w.CycleAnchorAt.Unix()-other.CycleAnchorAt.Unix())%w.PeriodSeconds == 0
}

func clonePlan(plan Plan) Plan {
	plan.Windows = slices.Clone(plan.Windows)
	return plan
}

func (s *State) FindPlan(id string) (Plan, bool) {
	id = strings.TrimSpace(id)
	for _, plan := range s.Plans {
		if plan.ID == id && id != "" {
			return plan, true
		}
	}
	return Plan{}, false
}

func (s *Store) Plans() []Plan {
	plans := []Plan{}
	s.read(func(state *State) {
		for _, plan := range state.Plans {
			plans = append(plans, clonePlan(plan))
		}
	})
	return plans
}

// CreatePlanWithBindings creates a plan and binds the selected currently
// unbound keys in the same state transaction.
func (s *Store) CreatePlanWithBindings(plan Plan, scopes []string) (Plan, error) {
	plan.ID = strings.TrimSpace(plan.ID)
	plan.Name = strings.TrimSpace(plan.Name)
	scopes = normalizeScopes(scopes)
	return editConfiguration(s, func(state *State) (Plan, Changes, error) {
		if plan.ID == "" {
			plan.ID = freeID(plan.Name, "plan", func(id string) bool {
				_, exists := state.FindPlan(id)
				return exists
			})
		}
		windows, err := prepareWindows(plan.Windows, nil, s.Now())
		if err != nil {
			return Plan{}, Changes{}, err
		}
		plan.Windows = windows
		if errValidate := plan.Validate(); errValidate != nil {
			return Plan{}, Changes{}, errValidate
		}
		if _, exists := state.FindPlan(plan.ID); exists {
			return Plan{}, Changes{}, conflictf("Subscription plan %q already exists", plan.ID)
		}
		if plan.Name == "" {
			plan.Name = plan.ID
		}
		for _, scope := range scopes {
			key := state.liveKey(scope)
			if key == nil {
				return Plan{}, Changes{}, notFoundf("API key %q does not exist", scope)
			}
			if key.PlanID != "" {
				return Plan{}, Changes{}, conflictf("API key %q is already bound to another subscription plan", scope)
			}
		}
		state.Plans = append(state.Plans, plan)
		for _, scope := range scopes {
			state.Keys[scope].PlanID = plan.ID
			state.Keys[scope].Cycles = nil
		}
		return clonePlan(plan), Changes{Plans: true, Keys: scopes}, nil
	})
}

type PlanPatch struct {
	ID      string         `json:"id"`
	Name    *string        `json:"name,omitempty"`
	Windows *[]QuotaWindow `json:"windows,omitempty"`
}

// UpdatePlanWithBindings applies a plan edit and, when scopes is non-nil,
// replaces the plan's complete key set. Selected keys may be unbound or already
// on this plan; keys owned by another plan are rejected atomically.
func (s *Store) UpdatePlanWithBindings(patch PlanPatch, scopes *[]string) (Plan, error) {
	patch.ID = strings.TrimSpace(patch.ID)
	if patch.ID == "" {
		return Plan{}, invalidf("Subscription plan ID is required")
	}

	return editConfiguration(s, func(state *State) (Plan, Changes, error) {
		for i := range state.Plans {
			if state.Plans[i].ID != patch.ID {
				continue
			}
			updated := state.Plans[i]
			if patch.Name != nil {
				updated.Name = strings.TrimSpace(*patch.Name)
			}
			if patch.Windows != nil {
				windows, err := prepareWindows(*patch.Windows, updated.Windows, s.Now())
				if err != nil {
					return Plan{}, Changes{}, err
				}
				updated.Windows = windows
			}
			if errValidate := updated.Validate(); errValidate != nil {
				return Plan{}, Changes{}, errValidate
			}
			var selected map[string]struct{}
			if scopes != nil {
				normalized := normalizeScopes(*scopes)
				selected = make(map[string]struct{}, len(normalized))
				for _, scope := range normalized {
					key := state.Keys[scope]
					if key == nil || !key.DeletedAt.IsZero() && key.PlanID != patch.ID {
						return Plan{}, Changes{}, notFoundf("API key %q does not exist", scope)
					}
					if key.PlanID != "" && key.PlanID != patch.ID {
						return Plan{}, Changes{}, conflictf("API key %q is already bound to another subscription plan", scope)
					}
					selected[scope] = struct{}{}
				}
			}

			var resetWindows []string
			if patch.Windows != nil {
				for _, old := range state.Plans[i].Windows {
					if !slices.ContainsFunc(updated.Windows, func(window QuotaWindow) bool {
						return window.ID == old.ID && window.sameSchedule(old)
					}) {
						resetWindows = append(resetWindows, old.ID)
					}
				}
			}

			for scope, key := range state.Keys {
				if key == nil || key.PlanID != patch.ID {
					continue
				}
				_, shouldBind := selected[scope]
				if scopes != nil && !shouldBind {
					key.PlanID, key.Cycles = "", nil
					continue
				}
				for _, id := range resetWindows {
					delete(key.Cycles, id)
				}
				// Credits survive limit/name edits, not disabled dimensions. Reject
				// overflowing combined limits atomically across all bound keys.
				settleExpiredCycles(key, s.Now())
				for _, window := range updated.Windows {
					cycle, exists := key.Cycles[window.ID]
					if !exists {
						continue
					}
					for _, dim := range quotaDimensions {
						dim.resetTemporaryIfDisabled(window, &cycle.TemporaryQuota)
					}
					if err := cycle.TemporaryQuota.validate(window); err != nil {
						return Plan{}, Changes{}, err
					}
					key.Cycles[window.ID] = cycle
				}
			}
			if scopes != nil {
				for scope := range selected {
					key := state.Keys[scope]
					if key.PlanID == "" {
						key.PlanID = patch.ID
						key.Cycles = nil
					}
				}
			}
			state.Plans[i] = updated
			return clonePlan(updated), Changes{Plans: true, AllKeys: true}, nil
		}
		return Plan{}, Changes{}, notFoundf("Subscription plan %q does not exist", patch.ID)
	})
}

func (s *Store) DeletePlan(id string) (int, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return 0, invalidf("Subscription plan ID is required")
	}

	return editConfiguration(s, func(state *State) (int, Changes, error) {
		index := slices.IndexFunc(state.Plans, func(plan Plan) bool { return plan.ID == id })
		if index < 0 {
			return 0, Changes{}, notFoundf("Subscription plan %q does not exist", id)
		}
		state.Plans = slices.Delete(state.Plans, index, index+1)

		released := 0
		for _, key := range state.Keys {
			if key == nil || key.PlanID != id {
				continue
			}
			key.PlanID = ""
			key.Cycles = nil
			released++
		}
		return released, Changes{Plans: true, AllKeys: true}, nil
	})
}

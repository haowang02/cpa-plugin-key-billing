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
	CycleScope string        `json:"cycle_scope,omitempty"`
	StartedAt  time.Time     `json:"started_at,omitzero"`
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Windows    []QuotaWindow `json:"windows"`
}

// Zero disables a dimension within the window's independently timed cycle.
type QuotaWindow struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	PeriodSeconds int64   `json:"period_seconds"`
	AmountUSD     float64 `json:"amount_usd"`
	TokenLimit    int64   `json:"token_limit"`
	RequestLimit  int64   `json:"request_limit"`
}

const maxPeriodSeconds = int64(math.MaxInt64) / int64(time.Second)

// Limits travel through browser number inputs and must be exactly representable.
const maxQuotaCount = int64(1<<53 - 1)

const CycleScopeKey = "key"
const CycleScopePlan = "plan"

func (p Plan) SharedCycles() bool { return p.CycleScope == CycleScopePlan }

func (p Plan) Validate() error {
	if p.CycleScope != "" && p.CycleScope != CycleScopeKey && p.CycleScope != CycleScopePlan {
		return invalidf("周期起点模式无效")
	}
	if strings.TrimSpace(p.ID) == "" {
		return invalidf("订阅计划 ID 不能为空")
	}
	if len(p.Windows) == 0 {
		return invalidf("订阅计划至少需要一个额度窗口")
	}
	ids := make(map[string]bool)
	names := make(map[string]bool)
	periods := make(map[int64]bool)
	for _, window := range p.Windows {
		if window.ID == "" || ids[window.ID] {
			return invalidf("额度窗口 ID 无效或重复")
		}
		name := strings.TrimSpace(window.Name)
		if name == "" || len(name) > maxRouteNameBytes {
			return invalidf("窗口名称不能为空且不能超过 %d 字节", maxRouteNameBytes)
		}
		if names[strings.ToLower(name)] {
			return invalidf("窗口名称 %q 重复", name)
		}
		if window.AmountUSD < 0 || math.IsNaN(window.AmountUSD) || math.IsInf(window.AmountUSD, 0) {
			return invalidf("窗口 %q：金额额度必须为有限非负数", name)
		}
		if window.TokenLimit < 0 || window.TokenLimit > maxQuotaCount || window.RequestLimit < 0 || window.RequestLimit > maxQuotaCount {
			return invalidf("窗口 %q：Token 和请求限额必须为 0 到 %d 的整数", name, maxQuotaCount)
		}
		if window.AmountUSD == 0 && window.TokenLimit == 0 && window.RequestLimit == 0 {
			return invalidf("窗口 %q：至少设置一种额度", name)
		}
		if window.PeriodSeconds <= 0 || window.PeriodSeconds > maxPeriodSeconds {
			return invalidf("窗口 %q：周期必须为 1 到 %d 秒", name, maxPeriodSeconds)
		}
		if periods[window.PeriodSeconds] {
			return invalidf("窗口 %q 的周期与其他窗口重复", name)
		}
		ids[window.ID], names[strings.ToLower(name)], periods[window.PeriodSeconds] = true, true, true
	}
	return nil
}

func prepareWindows(windows, existing []QuotaWindow) ([]QuotaWindow, error) {
	windows = slices.Clone(windows)
	for i := range windows {
		window := &windows[i]
		window.Name = strings.TrimSpace(window.Name)
		if window.ID == "" {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return nil, err
			}
			window.ID = hex.EncodeToString(id[:])
		} else if !slices.ContainsFunc(existing, func(old QuotaWindow) bool { return old.ID == window.ID }) {
			return nil, invalidf("额度窗口 %q 已不存在，请刷新后重试", window.Name)
		}
	}
	slices.SortFunc(windows, func(a, b QuotaWindow) int {
		return cmp.Compare(a.PeriodSeconds, b.PeriodSeconds)
	})
	return windows, nil
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
	plan.StartedAt = time.Time{}
	plan.ID = strings.TrimSpace(plan.ID)
	plan.Name = strings.TrimSpace(plan.Name)
	scopes = normalizeScopes(scopes)
	return editConfiguration(s, func(state *State) (Plan, Changes, error) {
		if plan.ID == "" {
			plan.ID = freeID(plan.Name, "plan", func(id string) bool { _, exists := state.FindPlan(id); return exists })
		}
		windows, err := prepareWindows(plan.Windows, nil)
		if err != nil {
			return Plan{}, Changes{}, err
		}
		plan.Windows = windows
		if errValidate := plan.Validate(); errValidate != nil {
			return Plan{}, Changes{}, errValidate
		}
		if _, exists := state.FindPlan(plan.ID); exists {
			return Plan{}, Changes{}, conflictf("订阅计划 %q 已存在", plan.ID)
		}
		if plan.Name == "" {
			plan.Name = plan.ID
		}
		for _, scope := range scopes {
			key := state.liveKey(scope)
			if key == nil {
				return Plan{}, Changes{}, notFoundf("API Key %q 不存在", scope)
			}
			if key.PlanID != "" {
				return Plan{}, Changes{}, conflictf("API Key %q 已绑定其他订阅计划", scope)
			}
		}
		state.Plans = append(state.Plans, plan)
		for _, scope := range scopes {
			state.Keys[scope].PlanID = plan.ID
			state.Keys[scope].BillingSince = s.Now()
			state.Keys[scope].Cycles = nil
		}
		return clonePlan(plan), Changes{Plans: true, Keys: scopes}, nil
	})
}

type PlanPatch struct {
	CycleScope *string        `json:"cycle_scope,omitempty"`
	ID         string         `json:"id"`
	Name       *string        `json:"name,omitempty"`
	Windows    *[]QuotaWindow `json:"windows,omitempty"`
}

// UpdatePlanWithBindings applies a plan edit and, when scopes is non-nil,
// replaces the plan's complete key set. Selected keys may be unbound or already
// on this plan; keys owned by another plan are rejected atomically.
func (s *Store) UpdatePlanWithBindings(patch PlanPatch, scopes *[]string) (Plan, error) {
	patch.ID = strings.TrimSpace(patch.ID)
	if patch.ID == "" {
		return Plan{}, invalidf("订阅计划 ID 不能为空")
	}

	return editConfiguration(s, func(state *State) (Plan, Changes, error) {
		for i := range state.Plans {
			if state.Plans[i].ID != patch.ID {
				continue
			}
			updated := state.Plans[i]
			if patch.CycleScope != nil {
				updated.CycleScope = *patch.CycleScope
			}
			scopeChanged := updated.SharedCycles() != state.Plans[i].SharedCycles()
			if scopeChanged {
				updated.StartedAt = time.Time{}
			}
			if patch.Name != nil {
				updated.Name = strings.TrimSpace(*patch.Name)
			}
			if patch.Windows != nil {
				windows, err := prepareWindows(*patch.Windows, updated.Windows)
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
						return Plan{}, Changes{}, notFoundf("API Key %q 不存在", scope)
					}
					if key.PlanID != "" && key.PlanID != patch.ID {
						return Plan{}, Changes{}, conflictf("API Key %q 已绑定其他订阅计划", scope)
					}
					selected[scope] = struct{}{}
				}
			}

			var resetWindows []string
			if patch.Windows != nil {
				for _, old := range state.Plans[i].Windows {
					if !slices.ContainsFunc(updated.Windows, func(window QuotaWindow) bool {
						return window.ID == old.ID && window.PeriodSeconds == old.PeriodSeconds
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
					key.BillingSince = s.Now()
					continue
				}
				if scopeChanged {
					key.Cycles = nil
					key.BillingSince = s.Now()
				}
				for _, id := range resetWindows {
					delete(key.Cycles, id)
				}
			}
			if scopes != nil {
				for scope := range selected {
					key := state.Keys[scope]
					if key.PlanID == "" {
						key.PlanID = patch.ID
						key.BillingSince = s.Now()
						key.Cycles = nil
					}
				}
			}
			state.Plans[i] = updated
			return clonePlan(updated), Changes{Plans: true, AllKeys: true}, nil
		}
		return Plan{}, Changes{}, notFoundf("订阅计划 %q 不存在", patch.ID)
	})
}

func (s *Store) DeletePlan(id string) (int, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return 0, invalidf("订阅计划 ID 不能为空")
	}

	return editConfiguration(s, func(state *State) (int, Changes, error) {
		index := slices.IndexFunc(state.Plans, func(plan Plan) bool { return plan.ID == id })
		if index < 0 {
			return 0, Changes{}, notFoundf("订阅计划 %q 不存在", id)
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

// A free response starts a token/request plan only when it consumes an enabled
// dimension; an amount-only plan waits for a positive monetary charge.
func (p Plan) startsWithUsage(usage quotaUsage) bool {
	for _, window := range p.Windows {
		if window.AmountUSD > 0 && usage.AmountUSD > 0 || window.TokenLimit > 0 && usage.Tokens > 0 || window.RequestLimit > 0 && usage.Requests > 0 {
			return true
		}
	}
	return false
}

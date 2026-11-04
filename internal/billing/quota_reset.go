package billing

import "strings"

type ResetRequest struct {
	Mode   string   `json:"mode"`
	Scopes []string `json:"scopes,omitempty"`
}

type ResetResult struct {
	Keys    int `json:"keys"`
	Windows int `json:"windows"`
	Pools   int `json:"pools"`
}

func (s *Store) ResetQuota(req ResetRequest) (ResetResult, error) {
	req.Scopes = normalizeScopes(req.Scopes)
	switch req.Mode {
	case "global":
		if len(req.Scopes) != 0 {
			return ResetResult{}, invalidf("A reset of all quotas cannot specify individual keys")
		}
	case "all":
		if len(req.Scopes) == 0 {
			return ResetResult{}, invalidf("Select the API keys to reset")
		}
	default:
		return ResetResult{}, invalidf("Invalid quota reset mode")
	}
	return editConfiguration(s, func(state *State) (ResetResult, Changes, error) {
		keys := make(map[string]bool)
		pools := make(map[string]bool)
		if req.Mode == "global" {
			for _, plan := range state.Plans {
				if plan.SharedQuota {
					pools[plan.ID] = true
				}
			}
			for scope, key := range state.Keys {
				if key != nil && key.DeletedAt.IsZero() && key.PlanID != "" {
					keys[scope] = true
				}
			}
		}
		for _, scope := range req.Scopes {
			key := state.liveKey(scope)
			if key == nil || key.PlanID == "" {
				return ResetResult{}, Changes{}, invalidf("The API key does not exist or has no subscription plan")
			}
			keys[scope] = true
		}
		result, changes := resetQuotas(state, keys, pools)
		return result, changes, nil
	})
}

func (s *Store) ResetPlanQuota(id string) (ResetResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ResetResult{}, invalidf("Subscription plan ID is required")
	}
	return editConfiguration(s, func(state *State) (ResetResult, Changes, error) {
		plan := state.FindPlan(id)
		if plan == nil {
			return ResetResult{}, Changes{}, notFoundf("Subscription plan %q does not exist", id)
		}
		if !plan.SharedQuota {
			return ResetResult{}, Changes{}, invalidf("This subscription plan does not share quota")
		}
		result, changes := resetQuotas(state, nil, map[string]bool{id: true})
		return result, changes, nil
	})
}

func resetQuotas(state *State, keys, pools map[string]bool) (ResetResult, Changes) {
	for scope := range keys {
		if plan := state.FindPlan(state.Keys[scope].PlanID); plan != nil && plan.SharedQuota {
			pools[plan.ID] = true
		}
	}
	result := ResetResult{}
	changes := Changes{}
	for id := range pools {
		plan := state.FindPlan(id)
		result.Windows += len(plan.Cycles)
		if len(plan.Cycles) > 0 {
			result.Pools++
		}
		plan.Cycles = nil
		changes.PlanCycles = append(changes.PlanCycles, id)
	}
	for scope, key := range state.Keys {
		if key == nil || !keys[scope] && !pools[key.PlanID] {
			continue
		}
		count := len(key.Cycles)
		key.Cycles = nil
		changes.Keys = append(changes.Keys, scope)
		if !key.DeletedAt.IsZero() {
			continue
		}
		if count > 0 || pools[key.PlanID] {
			result.Keys++
		}
		if !pools[key.PlanID] {
			result.Windows += count
		}
	}
	return result, changes
}

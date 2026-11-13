package billing

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

type KeyView struct {
	Scope     string    `json:"scope"`
	Preview   string    `json:"preview,omitempty"`
	Label     string    `json:"label,omitempty"`
	InConfig  bool      `json:"in_config"`
	DeletedAt time.Time `json:"deleted_at,omitzero"`

	PlanID             string            `json:"plan_id,omitempty"`
	PlanName           string            `json:"plan_name,omitempty"`
	ConcurrencyLimit   int               `json:"concurrency_limit"`
	CurrentConcurrency int               `json:"current_concurrency"`
	RouteBindings      RouteBindings     `json:"route_bindings"`
	RouteNames         map[string]string `json:"route_names"`
	QuotaView
}

func (s *Store) KeyViews() []KeyView {
	now := s.Now()
	return updateResult(s, func(state *State) ([]KeyView, Changes) {
		var changes Changes
		for i := range state.Plans {
			plan := &state.Plans[i]
			if plan.Cycles.settle(now) {
				changes.PlanCycles = append(changes.PlanCycles, plan.ID)
			}
		}
		views := make([]KeyView, 0, len(state.Keys))
		for scope, key := range state.Keys {
			if key == nil {
				continue
			}
			if key.DeletedAt.IsZero() && key.Cycles.settle(now) {
				changes.Keys = append(changes.Keys, scope)
			}
			views = append(views, s.keyView(state, scope, key, state.FindPlan(key.PlanID), now))
		}
		sortKeyViews(views, s.configOrder)
		return views, changes
	})
}

// Caller holds s.mu.
func (s *Store) keyView(state *State, scope string, key *KeyState, plan *Plan, now time.Time) KeyView {
	planName := ""
	if plan != nil {
		planName = plan.Name
	}
	routeNames := make(map[string]string, len(key.RouteBindings.RouteIDs))
	for _, id := range key.RouteBindings.RouteIDs {
		route, _ := state.findRoute(id)
		routeNames[id] = route.Name
	}
	return KeyView{
		Scope:              scope,
		Preview:            key.Preview,
		Label:              key.Label,
		InConfig:           key.InConfig,
		DeletedAt:          key.DeletedAt,
		PlanID:             key.PlanID,
		PlanName:           planName,
		QuotaView:          quotaView(key.Cycles, plan, s.cfg.CNYPerUSD, now),
		ConcurrencyLimit:   key.ConcurrencyLimit,
		CurrentConcurrency: s.activeByScope[scope],
		RouteBindings:      key.RouteBindings.clone(),
		RouteNames:         routeNames,
	}
}

func (s *Store) KeyViewForScope(scope string) (KeyView, bool) {
	scope = normalizeScope(scope)
	if scope == "" {
		return KeyView{}, false
	}
	type result struct {
		view KeyView
		ok   bool
	}
	current := updateResult(s, func(state *State) (result, Changes) {
		key := state.Keys[scope]
		if key == nil || !key.DeletedAt.IsZero() {
			return result{}, Changes{}
		}
		now := s.Now()
		plan := state.FindPlan(key.PlanID)
		changed := settleQuota(scope, key, plan, now)
		return result{view: s.keyView(state, scope, key, plan, now), ok: true}, changed
	})
	return current.view, current.ok
}

// Keys CPA holds keep its configuration order; the rest follow by label.
func sortKeyViews(views []KeyView, configOrder map[string]int) {
	rank := func(scope string) int {
		if position, listed := configOrder[scope]; listed {
			return position
		}
		return len(configOrder)
	}
	slices.SortFunc(views, func(a, b KeyView) int {
		return cmp.Or(
			cmp.Compare(rank(a.Scope), rank(b.Scope)),
			cmp.Compare(strings.ToLower(a.Label), strings.ToLower(b.Label)),
			cmp.Compare(a.Scope, b.Scope),
		)
	})
}

func (key *KeyState) bindPlan(id string) bool {
	if key.PlanID == id {
		return false
	}
	key.PlanID, key.Cycles = id, nil
	return true
}

func (s *Store) BindKey(scope, planID string) error {
	scope = normalizeScope(scope)
	planID = strings.TrimSpace(planID)
	if scope == "" || planID == "" {
		return invalidf("API key identifier and subscription plan ID are required")
	}
	_, err := editConfiguration(s, func(state *State) (struct{}, Changes, error) {
		plan := state.FindPlan(planID)
		if plan == nil {
			return struct{}{}, Changes{}, notFoundf("Subscription plan %q does not exist", planID)
		}
		key := state.liveKey(scope)
		if key == nil {
			return struct{}{}, Changes{}, notFoundf("API key %q does not exist", scope)
		}
		if !key.bindPlan(plan.ID) {
			return struct{}{}, Changes{}, nil
		}
		return struct{}{}, Changes{Keys: []string{scope}}, nil
	})
	return err
}

func (s *Store) UnbindKey(scope string) error {
	scope = normalizeScope(scope)
	if scope == "" {
		return invalidf("API key identifier is required")
	}
	_, err := editConfiguration(s, func(state *State) (struct{}, Changes, error) {
		key := state.liveKey(scope)
		if key == nil || key.PlanID == "" {
			return struct{}{}, Changes{}, nil
		}
		key.bindPlan("")
		return struct{}{}, Changes{Keys: []string{scope}}, nil
	})
	return err
}

func (s *Store) SetLabel(scope, label string) error {
	scope = normalizeScope(scope)
	if scope == "" {
		return invalidf("API key identifier is required")
	}
	label = strings.TrimSpace(label)

	_, err := editConfiguration(s, func(state *State) (struct{}, Changes, error) {
		key := state.Keys[scope]
		if key == nil {
			return struct{}{}, Changes{}, notFoundf("API key %q does not exist", scope)
		}
		key.Label = label
		return struct{}{}, Changes{Keys: []string{scope}}, nil
	})
	return err
}

func (s *Store) LiveScope(scopes []string) string {
	result := ""
	s.read(func(state *State) {
		for _, scope := range scopes {
			if scope = normalizeScope(scope); state.liveKey(scope) != nil {
				result = scope
				return
			}
		}
	})
	return result
}

// Deleted keys retain their settings but cannot be rebound or reset.
func (s *State) liveKey(scope string) *KeyState {
	key := s.Keys[scope]
	if key == nil || !key.DeletedAt.IsZero() {
		return nil
	}
	return key
}

type SyncResult struct {
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
}

func (s *State) ensureKey(scope, preview string) *KeyState {
	if scope == "" {
		return nil
	}
	preview = strings.TrimSpace(preview)
	if preview == "" {
		preview = UnknownKeyPreview
	}
	key := s.Keys[scope]
	if key == nil {
		key = &KeyState{Preview: preview}
		s.Keys[scope] = key
	} else if key.Preview == "" || key.Preview == UnknownKeyPreview {
		key.Preview = preview
	}
	return key
}

// SyncKeys keeps only scope hashes and masked previews. A missing key is marked deleted only if an earlier
// sync saw it, so other access providers' principals survive; allowEmpty guards against an accidental empty
// push. A restored key keeps its bindings and spent quota.
func (s *Store) SyncKeys(keys []string, allowEmpty bool) (SyncResult, error) {
	scopes := make(map[string]string, len(keys))
	order := make(map[string]int, len(keys))
	for _, key := range keys {
		scope := CallerScope(key)
		if _, listed := scopes[scope]; scope == "" || listed {
			continue
		}
		scopes[scope] = PreviewKey(key)
		order[scope] = len(order)
	}
	if len(scopes) == 0 && !allowEmpty {
		return SyncResult{}, invalidf("The API key list is empty; pass allow_empty to clear it")
	}

	now := s.Now()

	result, err := editConfiguration(s, func(state *State) (SyncResult, Changes, error) {
		var result SyncResult
		changed := false
		for scope, preview := range scopes {
			if key := state.Keys[scope]; key == nil || key.Preview != preview {
				changed = true
			}
			key := state.ensureKey(scope, preview)
			key.Preview = preview
			if !key.InConfig {
				result.Added++
			}
			if !key.DeletedAt.IsZero() {
				key.DeletedAt = time.Time{}
				changed = true
			}
			if !key.InConfig {
				key.InConfig = true
				changed = true
			}
		}
		for scope, key := range state.Keys {
			if _, listed := scopes[scope]; listed || key == nil {
				continue
			}
			if key.InConfig {
				key.InConfig = false
				key.DeletedAt = now
				result.Deleted++
				changed = true
			}
		}
		if !changed {
			return result, Changes{}, nil
		}
		return result, Changes{AllKeys: true}, nil
	})
	if err == nil {
		s.mu.Lock()
		s.configOrder = order
		s.mu.Unlock()
	}
	return result, err
}

// Scopes are hex digests, so case folding is safe for hand-typed input.
func normalizeScope(scope string) string {
	return strings.ToLower(strings.TrimSpace(scope))
}

func normalizeScopes(scopes []string) []string {
	lowered := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		lowered = append(lowered, normalizeScope(scope))
	}
	return dedupe(lowered)
}

// Values compare case-insensitively but keep their original spelling for display.
func dedupe(values []string) []string {
	var deduped []string
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		lowered := strings.ToLower(value)
		if _, exists := seen[lowered]; exists {
			continue
		}
		seen[lowered] = struct{}{}
		deduped = append(deduped, value)
	}
	return deduped
}

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

// Settle expired cycles before displaying quota status for active keys.
func (s *Store) KeyViews() []KeyView {
	now := s.Now()
	return updateResult(s, func(state *State) ([]KeyView, Changes) {
		var settled []string
		views := make([]KeyView, 0, len(state.Keys))
		for scope, key := range state.Keys {
			if key == nil {
				continue
			}
			if key.DeletedAt.IsZero() && settleExpiredCycles(key, now) {
				settled = append(settled, scope)
			}
			views = append(views, s.keyView(state, scope, key, now))
		}
		sortKeyViews(views, s.configOrder)
		return views, Changes{Keys: settled}
	})
}

// keyView needs the store lock for its concurrency and exchange rate.
func (s *Store) keyView(state *State, scope string, key *KeyState, now time.Time) KeyView {
	plan, _ := state.FindPlan(key.PlanID)
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
		PlanName:           plan.Name,
		QuotaView:          quotaView(key, plan, s.cfg.CNYPerUSD, now),
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
		changed := Changes{}
		if settleExpiredCycles(key, now) {
			changed.Keys = []string{scope}
		}
		return result{view: s.keyView(state, scope, key, now), ok: true}, changed
	})
	return current.view, current.ok
}

// Keys CPA holds keep their configuration order, as its panels list them; the
// rest follow by label.
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

func (s *Store) BindKey(scope, planID string) error {
	scope = normalizeScope(scope)
	planID = strings.TrimSpace(planID)
	if scope == "" || planID == "" {
		return invalidf("API key identifier and subscription plan ID are required")
	}
	_, err := editConfiguration(s, func(state *State) (struct{}, Changes, error) {
		plan, exists := state.FindPlan(planID)
		if !exists {
			return struct{}{}, Changes{}, notFoundf("Subscription plan %q does not exist", planID)
		}
		key := state.liveKey(scope)
		if key == nil {
			return struct{}{}, Changes{}, notFoundf("API key %q does not exist", scope)
		}
		if key.PlanID == plan.ID {
			return struct{}{}, Changes{}, nil
		}
		key.Cycles = nil
		key.PlanID = plan.ID
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
		key.PlanID = ""
		key.Cycles = nil
		return struct{}{}, Changes{Keys: []string{scope}}, nil
	})
	return err
}

type ResetRequest struct {
	Mode   string   `json:"mode"`
	Scopes []string `json:"scopes,omitempty"`
}

type ResetResult struct {
	Keys    int `json:"keys"`
	Windows int `json:"windows"`
}

func (s *Store) ResetQuota(req ResetRequest) (ResetResult, error) {
	req.Scopes = normalizeScopes(req.Scopes)
	if req.Mode == "global" {
		if len(req.Scopes) != 0 {
			return ResetResult{}, invalidf("A reset of all quotas cannot specify individual keys")
		}
	} else if req.Mode == "all" {
		if len(req.Scopes) == 0 {
			return ResetResult{}, invalidf("Select the API keys to reset")
		}
	} else {
		return ResetResult{}, invalidf("Invalid quota reset mode")
	}
	return editConfiguration(s, func(state *State) (ResetResult, Changes, error) {
		scopes := req.Scopes
		if req.Mode == "global" {
			for scope, key := range state.Keys {
				if key != nil && key.DeletedAt.IsZero() && key.PlanID != "" {
					scopes = append(scopes, scope)
				}
			}
		}
		for _, scope := range scopes {
			key := state.liveKey(scope)
			if key == nil || key.PlanID == "" {
				return ResetResult{}, Changes{}, invalidf("The API key does not exist or has no subscription plan")
			}
		}
		result := ResetResult{}
		var changed []string
		for _, scope := range scopes {
			key := state.Keys[scope]
			count := len(key.Cycles)
			key.Cycles = nil
			if count > 0 {
				changed = append(changed, scope)
				result.Keys++
				result.Windows += count
			}
		}
		return result, Changes{Keys: changed}, nil
	})
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

// LiveScope returns the first scope that names a live key, or "".
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

// SyncKeys reconciles the tracked keys with the list CPA currently holds.
//
// Plaintext keys are discarded after producing a scope hash and masked preview.
// A key missing from the list is marked deleted rather than dropped, and only if
// an earlier sync saw it, so principals from other access providers survive.
// allowEmpty prevents an accidental empty push from marking every synchronized
// record deleted.
//
// Restoring a key preserves its bindings and spent quota. Normal cycle expiry
// and explicit resets still apply.
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

// dedupe drops blanks and repeats from an operator's list. Values are compared
// case-insensitively but kept in the spelling they arrived in, because model
// names and identifiers are read back on screen.
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

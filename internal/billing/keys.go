package billing

import (
	"sort"
	"strings"
	"time"
)

type KeyView struct {
	Scope    string `json:"scope"`
	Preview  string `json:"preview,omitempty"`
	Label    string `json:"label,omitempty"`
	InConfig bool   `json:"in_config"`

	PlanID             string        `json:"plan_id,omitempty"`
	PlanName           string        `json:"plan_name,omitempty"`
	ConcurrencyLimit   int           `json:"concurrency_limit"`
	CurrentConcurrency int           `json:"current_concurrency"`
	RouteBindings      RouteBindings `json:"route_bindings"`
	// Keys without a plan are still fully accounted for.
	Unlimited   bool      `json:"unlimited"`
	Blocked     bool      `json:"blocked"`
	LimitUSD    float64   `json:"limit_usd"`
	SpentUSD    float64   `json:"spent_usd"`
	UsedPercent float64   `json:"used_percent"`
	CycleEndAt  time.Time `json:"cycle_end_at,omitzero"`
}

// Listing rolls expired windows first, so what an operator reads is exactly
// what the next request would be judged against — a key whose budget reset an
// hour ago must not still be displayed as blocked.
func (s *Store) KeyViews() []KeyView {
	now := s.Now()
	return updateResult(s, func(state *State) ([]KeyView, Changes) {
		var settled []string
		plans := make(map[string]Plan, len(state.Plans))
		for _, plan := range state.Plans {
			plans[plan.ID] = plan
		}
		views := make([]KeyView, 0, len(state.Keys))
		for scope, key := range state.Keys {
			if key == nil || !key.DeletedAt.IsZero() {
				continue
			}
			if settleKeyPlan(key, plans, now) {
				settled = append(settled, scope)
			}
			views = append(views, keyView(scope, key, plans, s.activeByScope[scope]))
		}
		sortKeyViews(views)
		return views, Changes{Keys: settled}
	})
}

func keyView(scope string, key *KeyState, plans map[string]Plan, currentConcurrency int) KeyView {
	view := KeyView{
		Scope:              scope,
		Preview:            key.Preview,
		Label:              key.Label,
		InConfig:           key.InConfig,
		PlanID:             key.PlanID,
		ConcurrencyLimit:   key.ConcurrencyLimit,
		CurrentConcurrency: currentConcurrency,
		RouteBindings: RouteBindings{
			RouteIDs:            append([]string{}, key.RouteBindings.RouteIDs...),
			Models:              append([]string{}, key.RouteBindings.Models...),
			CredentialIDs:       append([]string{}, key.RouteBindings.CredentialIDs...),
			CredentialProviders: append([]CredentialProviderSelector{}, key.RouteBindings.CredentialProviders...),
		},
		Unlimited:  true,
		SpentUSD:   key.Cycle.SpentUSD,
		CycleEndAt: key.Cycle.EndAt,
	}
	if plan, exists := plans[key.PlanID]; exists {
		view.PlanName = plan.Name
		view.LimitUSD = plan.AmountUSD
		view.Unlimited = false
		if plan.AmountUSD > 0 {
			view.UsedPercent = key.Cycle.SpentUSD / plan.AmountUSD * 100
			view.Blocked = key.Cycle.SpentUSD >= plan.AmountUSD
		} else {
			// Invalid persisted plans must never grant unlimited usage.
			view.UsedPercent = 100
			view.Blocked = true
		}
	}
	return view
}

func settleKeyPlan(key *KeyState, plans map[string]Plan, now time.Time) bool {
	if key.PlanID == "" {
		return false
	}
	plan, exists := plans[key.PlanID]
	if exists {
		return settleExpiredCycle(key, plan, now)
	}
	key.PlanID = ""
	key.Cycle = Cycle{}
	return true
}

// KeyViewForScope returns one active principal without materializing every
// other key. It applies the same expired-cycle settlement as KeyViews so the
// account page and the next admission decision agree.
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
		plans := make(map[string]Plan, len(state.Plans))
		for _, plan := range state.Plans {
			plans[plan.ID] = plan
		}
		changed := Changes{}
		if settleKeyPlan(key, plans, s.Now()) {
			changed.Keys = []string{scope}
		}
		return result{view: keyView(scope, key, plans, s.activeByScope[scope]), ok: true}, changed
	})
	return current.view, current.ok
}

func sortKeyViews(views []KeyView) {
	sort.Slice(views, func(i, j int) bool {
		if views[i].Blocked != views[j].Blocked {
			return views[i].Blocked
		}
		left, right := strings.ToLower(views[i].Label), strings.ToLower(views[j].Label)
		if left != right {
			return left < right
		}
		return views[i].Scope < views[j].Scope
	})
}

// Rebinding returns the subscription to its inactive state. Its first period
// starts only when the key is next used.
func (s *Store) BindKey(scope, planID string) error {
	scope = normalizeScope(scope)
	planID = strings.TrimSpace(planID)
	if scope == "" || planID == "" {
		return invalidf("API Key 标识和订阅计划 ID 都不能为空")
	}
	var errApply error
	updateResult(s, func(state *State) (struct{}, Changes) {
		plan, exists := state.FindPlan(planID)
		if !exists {
			errApply = notFoundf("订阅计划 %q 不存在", planID)
			return struct{}{}, Changes{}
		}
		key := state.liveKey(scope)
		if key == nil {
			errApply = notFoundf("API Key %q 不存在，请先同步 Key 列表", scope)
			return struct{}{}, Changes{}
		}
		if key.PlanID == plan.ID {
			return struct{}{}, Changes{}
		}
		key.Cycle = Cycle{}
		key.PlanID = plan.ID
		return struct{}{}, Changes{Keys: []string{scope}}
	})
	return errApply
}

func (s *Store) UnbindKey(scope string) error {
	scope = normalizeScope(scope)
	if scope == "" {
		return invalidf("API Key 标识不能为空")
	}
	updateResult(s, func(state *State) (struct{}, Changes) {
		key := state.liveKey(scope)
		if key == nil || key.PlanID == "" {
			return struct{}{}, Changes{}
		}
		key.PlanID = ""
		key.Cycle = Cycle{}
		return struct{}{}, Changes{Keys: []string{scope}}
	})
	return nil
}

// The next request starts a fresh period after a reset.
func (s *Store) ResetCycle(scope string) error {
	scope = normalizeScope(scope)
	if scope == "" {
		return invalidf("API Key 标识不能为空")
	}
	updateResult(s, func(state *State) (struct{}, Changes) {
		key := state.liveKey(scope)
		if key == nil {
			return struct{}{}, Changes{}
		}
		if _, exists := state.FindPlan(key.PlanID); !exists {
			return struct{}{}, Changes{}
		}
		key.Cycle = Cycle{}
		return struct{}{}, Changes{Keys: []string{scope}}
	})
	return nil
}

// ResetAllCycles resets every key on a plan that repeats, and reports how many
// had a period to end. A plan that never resets grants its budget once, so a
// bulk action must not hand it out again.
func (s *Store) ResetAllCycles() int {
	return updateResult(s, func(state *State) (int, Changes) {
		var reset []string
		for scope, key := range state.Keys {
			if key == nil || !key.DeletedAt.IsZero() || key.Cycle == (Cycle{}) {
				continue
			}
			plan, exists := state.FindPlan(key.PlanID)
			if !exists || plan.PeriodSeconds == 0 {
				continue
			}
			key.Cycle = Cycle{}
			reset = append(reset, scope)
		}
		return len(reset), Changes{Keys: reset}
	})
}

func (s *Store) SetLabel(scope, label string) error {
	scope = normalizeScope(scope)
	if scope == "" {
		return invalidf("API Key 标识不能为空")
	}
	label = strings.TrimSpace(label)

	var errApply error
	updateResult(s, func(state *State) (struct{}, Changes) {
		key := state.liveKey(scope)
		if key == nil {
			errApply = notFoundf("API Key %q 不存在，请先同步 Key 列表", scope)
			return struct{}{}, Changes{}
		}
		key.Label = label
		return struct{}{}, Changes{Keys: []string{scope}}
	})
	return errApply
}

// Management addresses the keys CPA holds. A deleted key keeps its record for
// its history alone, so it is neither bindable nor renameable.
func (s *State) liveKey(scope string) *KeyState {
	key := s.Keys[scope]
	if key == nil || !key.DeletedAt.IsZero() {
		return nil
	}
	return key
}

type SyncResult struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// SyncKeys reconciles the tracked keys with the list CPA currently holds.
//
// Plaintext keys are discarded after producing a scope hash and masked preview.
// A key missing from the list is marked deleted rather than dropped, and only if
// an earlier sync saw it, so principals from other access providers survive.
// allowEmpty prevents an accidental empty push from retiring every synchronized
// record.
//
// Deletion keeps the plan binding, which is what makes an accidental retirement
// recoverable: re-adding the same key restores enforcement rather than silently
// leaving it unlimited. Only the period is dropped, and only on the way back, so
// that a window already exhausted cannot block a key the moment it returns.
func (s *Store) SyncKeys(keys []string, allowEmpty bool) (SyncResult, error) {
	scopes := make(map[string]string, len(keys))
	for _, key := range keys {
		scope := CallerScope(key)
		if scope == "" {
			continue
		}
		scopes[scope] = PreviewKey(key)
	}
	if len(scopes) == 0 && !allowEmpty {
		return SyncResult{}, invalidf("API Key 列表为空；如需清空，请传入 allow_empty")
	}

	now := s.Now()
	// Request events decide which retired records may finally go, and they are read
	// before the mutation, which takes the same lock exclusively.
	referenced, errScopes := withRepository(s, func(repo Repository) (map[string]struct{}, error) {
		return repo.RequestEventScopes(now.Add(-RequestEventRetention))
	})
	if errScopes != nil {
		return SyncResult{}, errScopes
	}

	var result SyncResult
	updateResult(s, func(state *State) (struct{}, Changes) {
		changed := false
		for scope, preview := range scopes {
			if state.Keys[scope] == nil {
				changed = true
			}
			key := state.ensureKey(scope)
			// The live key list is authoritative for its masked preview.
			if key.Preview != preview {
				key.Preview = preview
				changed = true
			}
			if !key.InConfig {
				result.Added++
			}
			if !key.DeletedAt.IsZero() {
				key.DeletedAt = time.Time{}
				key.Cycle = Cycle{}
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
				result.Removed++
				changed = true
			}
		}
		purged := purgeDeletedKeys(state, referenced, now)
		if len(purged) > 0 {
			s.blocked.forget(purged...)
			changed = true
		}
		if !changed {
			return struct{}{}, Changes{}
		}
		return struct{}{}, Changes{AllKeys: true}
	})
	return result, nil
}

// A deleted key is kept for exactly as long as it can still be read: its own
// request history. Once no request event names it, the record is finally
// dropped, which bounds what an operator who rotates keys accumulates on disk.
// The count says whether the sync that called this has anything to write.
func purgeDeletedKeys(state *State, referenced map[string]struct{}, now time.Time) []string {
	cutoff := now.Add(-RequestEventRetention)
	var purged []string
	for scope, key := range state.Keys {
		if key == nil || key.DeletedAt.IsZero() || key.DeletedAt.After(cutoff) {
			continue
		}
		if _, exists := referenced[scope]; exists {
			continue
		}
		delete(state.Keys, scope)
		purged = append(purged, scope)
	}
	return purged
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

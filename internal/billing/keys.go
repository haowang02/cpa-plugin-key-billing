package billing

import (
	"sort"
	"strings"
	"time"
)

type ModelTotals struct {
	BillingModel string `json:"billing_model"`
	Totals
}

type KeyView struct {
	Scope    string `json:"scope"`
	Preview  string `json:"preview,omitempty"`
	Label    string `json:"label,omitempty"`
	InConfig bool   `json:"in_config"`
	// The UI leaves deleted keys out of the key list and the plan bindings,
	// while the totals below still count them.
	DeletedAt time.Time `json:"deleted_at,omitzero"`

	PlanID   string `json:"plan_id,omitempty"`
	PlanName string `json:"plan_name,omitempty"`
	// Keys without a plan are still fully accounted for.
	Unlimited   bool      `json:"unlimited"`
	Blocked     bool      `json:"blocked"`
	LimitUSD    float64   `json:"limit_usd"`
	SpentUSD    float64   `json:"spent_usd"`
	UsedPercent float64   `json:"used_percent"`
	CycleEndAt  time.Time `json:"cycle_end_at,omitzero"`

	Lifetime Totals        `json:"lifetime"`
	ByModel  []ModelTotals `json:"by_model,omitempty"`
}

type KeyDirectory struct {
	Keys []KeyView `json:"keys"`
}

// Listing rolls expired windows first, so what an operator reads is exactly
// what the next request would be judged against — a key whose budget reset an
// hour ago must not still be displayed as blocked.
func (s *Store) KeyDirectory() KeyDirectory {
	now := s.Now()
	directory := KeyDirectory{Keys: []KeyView{}}
	directory.Keys = updateResult(s, func(state *State) ([]KeyView, Changes) {
		var settled []string
		plans := make(map[string]Plan, len(state.Plans))
		for _, plan := range state.Plans {
			plans[plan.ID] = plan
		}
		views := make([]KeyView, 0, len(state.Keys))
		for scope, key := range state.Keys {
			if key == nil {
				continue
			}
			if key.PlanID != "" {
				plan, exists := plans[key.PlanID]
				if !exists {
					// Self-heal a binding whose plan was removed out of band.
					key.PlanID = ""
					key.Cycle = Cycle{}
					settled = append(settled, scope)
				} else if settleExpiredCycle(key, plan, now) {
					settled = append(settled, scope)
				}
			}
			views = append(views, keyView(scope, key, plans))
		}
		sortKeyViews(views)
		return views, Changes{Keys: settled}
	})
	return directory
}

func keyView(scope string, key *KeyState, plans map[string]Plan) KeyView {
	view := KeyView{
		Scope:      scope,
		Preview:    key.Preview,
		Label:      key.Label,
		InConfig:   key.InConfig,
		DeletedAt:  key.DeletedAt,
		PlanID:     key.PlanID,
		Unlimited:  true,
		SpentUSD:   key.Cycle.SpentUSD,
		CycleEndAt: key.Cycle.EndAt,
		Lifetime:   key.Lifetime,
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
	for model, totals := range key.ByModel {
		if totals == nil {
			continue
		}
		view.ByModel = append(view.ByModel, ModelTotals{BillingModel: model, Totals: *totals})
	}
	sortModelTotals(view.ByModel)
	return view
}

func sortKeyViews(views []KeyView) {
	sort.Slice(views, func(i, j int) bool {
		if views[i].Blocked != views[j].Blocked {
			return views[i].Blocked
		}
		if views[i].Lifetime.CostUSD != views[j].Lifetime.CostUSD {
			return views[i].Lifetime.CostUSD > views[j].Lifetime.CostUSD
		}
		return views[i].Scope < views[j].Scope
	})
}

func sortModelTotals(entries []ModelTotals) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CostUSD != entries[j].CostUSD {
			return entries[i].CostUSD > entries[j].CostUSD
		}
		return entries[i].BillingModel < entries[j].BillingModel
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
			if !exists || plan.Period.Kind == PeriodNever {
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
	Matched int `json:"matched"`
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
	// The log decides which retired records may finally go, and it is read
	// before the mutation, which takes the same lock exclusively.
	referenced, errScopes := withRepository(s, func(repo Repository) (map[string]struct{}, error) {
		return repo.LoggedScopes(now.Add(-LogRetention))
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
			if key.InConfig {
				result.Matched++
			} else {
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
		if purgeDeletedKeys(state, referenced, now) > 0 {
			changed = true
		}
		// The panel synchronizes on every session start, and a sync that moved
		// nothing is the common case. Rewriting every key and its per-model rows
		// to record that would be the largest write the plugin makes.
		if !changed {
			return struct{}{}, Changes{}
		}
		return struct{}{}, Changes{AllKeys: true}
	})
	return result, nil
}

// A deleted key is kept for exactly as long as it can still be read: its own
// billing history. Once the log holds nothing about it, the record is finally
// dropped, which bounds what an operator who rotates keys accumulates on disk.
// The count says whether the sync that called this has anything to write.
func purgeDeletedKeys(state *State, referenced map[string]struct{}, now time.Time) int {
	cutoff := now.Add(-LogRetention)
	purged := 0
	for scope, key := range state.Keys {
		if key == nil || key.DeletedAt.IsZero() || key.DeletedAt.After(cutoff) {
			continue
		}
		if _, exists := referenced[scope]; exists {
			continue
		}
		delete(state.Keys, scope)
		purged++
	}
	return purged
}

type StatsView struct {
	Keys        int           `json:"keys"`
	BlockedKeys int           `json:"blocked_keys"`
	Lifetime    Totals        `json:"lifetime"`
	ByModel     []ModelTotals `json:"by_model"`
}

func (s *Store) Stats() StatsView {
	return StatsFrom(s.KeyDirectory())
}

// Totals are derived from the listing rather than counted separately, so they
// cannot disagree with the rows an operator is reading. Listing also settles
// expired cycles, which is why a caller that needs both lists once and passes
// the result here instead of asking for each.
func StatsFrom(directory KeyDirectory) StatsView {
	stats := StatsView{ByModel: []ModelTotals{}}
	byModel := make(map[string]*Totals)
	for _, view := range directory.Keys {
		// A deleted key still spent what it spent, and its billing log is still
		// there to be read. It is no longer a key, so it is not counted as one.
		stats.Lifetime.Add(view.Lifetime)
		if view.DeletedAt.IsZero() {
			stats.Keys++
			if view.Blocked {
				stats.BlockedKeys++
			}
		}
		for _, entry := range view.ByModel {
			totals := byModel[entry.BillingModel]
			if totals == nil {
				totals = &Totals{}
				byModel[entry.BillingModel] = totals
			}
			totals.Add(entry.Totals)
		}
	}
	for model, totals := range byModel {
		stats.ByModel = append(stats.ByModel, ModelTotals{BillingModel: model, Totals: *totals})
	}
	sortModelTotals(stats.ByModel)
	return stats
}

// Scopes are hex digests, so case folding is safe for hand-typed input.
func normalizeScope(scope string) string {
	return strings.ToLower(strings.TrimSpace(scope))
}

func normalizeScopes(scopes []string) []string {
	normalized := make([]string, 0, len(scopes))
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scope = normalizeScope(scope)
		if scope == "" {
			continue
		}
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		normalized = append(normalized, scope)
	}
	return normalized
}

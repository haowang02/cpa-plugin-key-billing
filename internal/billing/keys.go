package billing

import (
	"sort"
	"strings"
	"time"
)

// MaxSyncKeys bounds one key-list push and the resulting state growth.
const MaxSyncKeys = 10000

const MaxTopKeys = 20

type ModelTotals struct {
	Model string `json:"model"`
	Totals
}

// KeyView is the merged per-key row the admin UI renders: identity, binding,
// current window, and statistics.
type KeyView struct {
	Scope    string `json:"scope"`
	Preview  string `json:"preview,omitempty"`
	Label    string `json:"label,omitempty"`
	InConfig bool   `json:"in_config"`

	PlanID   string `json:"plan_id,omitempty"`
	PlanName string `json:"plan_name,omitempty"`
	// Unlimited is true when the key has no plan. Such keys are still fully
	// accounted for.
	Unlimited bool `json:"unlimited"`
	// Blocked mirrors what Authorize would decide right now.
	Blocked       bool      `json:"blocked"`
	LimitUSD      float64   `json:"limit_usd"`
	SpentUSD      float64   `json:"spent_usd"`
	RemainingUSD  float64   `json:"remaining_usd"`
	UsedPercent   float64   `json:"used_percent"`
	CycleStartAt  time.Time `json:"cycle_start_at,omitzero"`
	CycleEndAt    time.Time `json:"cycle_end_at,omitzero"`
	CycleRequests int64     `json:"cycle_requests"`

	Lifetime     Totals         `json:"lifetime"`
	ByModel      []ModelTotals  `json:"by_model,omitempty"`
	RecentCycles []CycleSummary `json:"recent_cycles,omitempty"`
	FirstSeen    time.Time      `json:"first_seen,omitzero"`
	LastSeen     time.Time      `json:"last_seen,omitzero"`
}

type KeyDirectory struct {
	Keys        []KeyView `json:"keys"`
	LastSyncAt  time.Time `json:"last_sync_at,omitzero"`
	GeneratedAt time.Time `json:"generated_at"`
}

// KeyDirectory lists every tracked key.
//
// Listing rolls expired windows first, so what an operator reads is exactly
// what the next request would be judged against — a key whose budget reset an
// hour ago must not still be displayed as blocked.
func (s *Store) KeyDirectory() KeyDirectory {
	now := s.Now()
	directory := KeyDirectory{Keys: []KeyView{}, GeneratedAt: now}
	directory.Keys = updateResult(s, func(state *State) ([]KeyView, bool) {
		changed := false
		directory.LastSyncAt = state.LastSyncAt
		plans := make(map[string]Plan, len(state.Plans))
		for _, plan := range state.Plans {
			plans[plan.ID] = plan
		}
		views := make([]KeyView, 0, len(state.Keys))
		for _, key := range state.Keys {
			if key == nil {
				continue
			}
			if key.PlanID != "" {
				plan, exists := plans[key.PlanID]
				if !exists {
					// Self-heal a binding whose plan was removed out of band.
					key.PlanID = ""
					key.Cycle = Cycle{}
					changed = true
				} else if settleExpiredCycle(key, plan, now) {
					changed = true
				}
			}
			views = append(views, keyView(key, plans))
		}
		sortKeyViews(views)
		return views, changed
	})
	return directory
}

func keyView(key *KeyState, plans map[string]Plan) KeyView {
	view := KeyView{
		Scope:         key.Scope,
		Preview:       key.Preview,
		Label:         key.Label,
		InConfig:      key.InConfig,
		PlanID:        key.PlanID,
		Unlimited:     true,
		SpentUSD:      key.Cycle.SpentUSD,
		CycleStartAt:  key.Cycle.StartAt,
		CycleEndAt:    key.Cycle.EndAt,
		CycleRequests: key.Cycle.Requests,
		Lifetime:      key.Lifetime,
		RecentCycles:  key.RecentCycles,
		FirstSeen:     key.FirstSeen,
		LastSeen:      key.LastSeen,
	}
	if plan, exists := plans[key.PlanID]; exists {
		view.PlanName = plan.Name
		view.LimitUSD = plan.AmountUSD
		view.Unlimited = false
		if plan.AmountUSD > 0 {
			view.RemainingUSD = plan.AmountUSD - key.Cycle.SpentUSD
			if view.RemainingUSD < 0 {
				view.RemainingUSD = 0
			}
			view.UsedPercent = key.Cycle.SpentUSD / plan.AmountUSD * 100
			view.Blocked = key.Cycle.SpentUSD >= plan.AmountUSD
		} else {
			// Plans created by current versions cannot reach this branch. Treat
			// legacy or manually edited zero-value plans as exhausted.
			view.UsedPercent = 100
			view.Blocked = true
		}
	}
	for model, totals := range key.ByModel {
		if totals == nil {
			continue
		}
		view.ByModel = append(view.ByModel, ModelTotals{Model: model, Totals: *totals})
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
		return entries[i].Model < entries[j].Model
	})
}

// BindKeys attaches scopes to a plan and reports how many were changed.
//
// Rebinding returns the subscription to its inactive state. Its first period
// starts only when the key is next used.
func (s *Store) BindKeys(scopes []string, planID string) (int, error) {
	scopes = normalizeScopes(scopes)
	if len(scopes) == 0 {
		return 0, invalidf("请至少选择一个 API Key")
	}
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return 0, invalidf("订阅计划 ID 不能为空")
	}
	var errApply error
	bound := updateResult(s, func(state *State) (int, bool) {
		plan, exists := state.FindPlan(planID)
		if !exists {
			errApply = notFoundf("订阅计划 %q 不存在", planID)
			return 0, false
		}
		for _, scope := range scopes {
			if state.Keys[scope] == nil {
				errApply = notFoundf("API Key %q 不存在，请先同步 Key 列表", scope)
				return 0, false
			}
		}
		changed := 0
		for _, scope := range scopes {
			key := state.Keys[scope]
			if key.PlanID == plan.ID {
				continue
			}
			previousLimit := plan.AmountUSD
			if previous, okPrevious := state.FindPlan(key.PlanID); okPrevious {
				previousLimit = previous.AmountUSD
			}
			archiveCycle(key, previousLimit, key.PlanID)
			key.Cycle = Cycle{}
			key.PlanID = plan.ID
			changed++
		}
		return changed, changed > 0
	})
	return bound, errApply
}

func (s *Store) UnbindKeys(scopes []string) (int, error) {
	scopes = normalizeScopes(scopes)
	if len(scopes) == 0 {
		return 0, invalidf("请至少选择一个 API Key")
	}
	return updateResult(s, func(state *State) (int, bool) {
		changed := 0
		for _, scope := range scopes {
			key := state.Keys[scope]
			if key == nil || key.PlanID == "" {
				continue
			}
			limit := 0.0
			if plan, exists := state.FindPlan(key.PlanID); exists {
				limit = plan.AmountUSD
			}
			archiveCycle(key, limit, key.PlanID)
			key.PlanID = ""
			key.Cycle = Cycle{}
			changed++
		}
		return changed, changed > 0
	}), nil
}

// ResetCycles gives scopes their budget back immediately, without waiting for
// the period to end. The spend that is being forgiven is archived first.
func (s *Store) ResetCycles(scopes []string) (int, error) {
	scopes = normalizeScopes(scopes)
	if len(scopes) == 0 {
		return 0, invalidf("请至少选择一个 API Key")
	}
	return updateResult(s, func(state *State) (int, bool) {
		changed := 0
		for _, scope := range scopes {
			key := state.Keys[scope]
			if key == nil {
				continue
			}
			plan, exists := state.FindPlan(key.PlanID)
			if !exists {
				continue
			}
			archiveCycle(key, plan.AmountUSD, key.PlanID)
			key.Cycle = Cycle{}
			changed++
		}
		return changed, changed > 0
	}), nil
}

func (s *Store) SetLabel(scope, label string) error {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return invalidf("API Key 标识不能为空")
	}
	label = strings.TrimSpace(label)
	if len(label) > MaxNameLength {
		return invalidf("API Key 备注不能超过 %d 个字符", MaxNameLength)
	}

	var errApply error
	updateResult(s, func(state *State) (struct{}, bool) {
		key := state.Keys[scope]
		if key == nil {
			errApply = notFoundf("API Key %q 不存在，请先同步 Key 列表", scope)
			return struct{}{}, false
		}
		key.Label = label
		return struct{}{}, true
	})
	return errApply
}

func (s *Store) ForgetKeys(scopes []string) (int, error) {
	scopes = normalizeScopes(scopes)
	if len(scopes) == 0 {
		return 0, invalidf("请至少选择一个 API Key")
	}
	return updateResult(s, func(state *State) (int, bool) {
		removed := 0
		for _, scope := range scopes {
			if _, exists := state.Keys[scope]; exists {
				delete(state.Keys, scope)
				removed++
			}
		}
		if removed > 0 {
			pruneLogOrphans(state)
		}
		return removed, removed > 0
	}), nil
}

type SyncResult struct {
	Received int       `json:"received"`
	Added    int       `json:"added"`
	Matched  int       `json:"matched"`
	Removed  int       `json:"removed"`
	Retained int       `json:"retained"`
	SyncedAt time.Time `json:"synced_at"`
}

// SyncKeys reconciles the tracked keys with the list CPA currently holds.
//
// The plaintext keys are hashed into caller scopes and discarded; nothing here
// is persisted or logged. Keys missing from the list are dropped only if a
// previous sync had seen them, so a key belonging to some other access provider
// — which would never appear in this list — survives instead of being deleted
// on every sync. allowEmpty guards the obvious foot-gun of an empty push
// wiping every record.
func (s *Store) SyncKeys(keys []string, allowEmpty bool) (SyncResult, error) {
	if len(keys) > MaxSyncKeys {
		return SyncResult{}, invalidf("API Key 数量过多：最多允许 %d 个，实际收到 %d 个", MaxSyncKeys, len(keys))
	}
	now := s.Now()
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

	result := SyncResult{Received: len(scopes), SyncedAt: now}
	updateResult(s, func(state *State) (struct{}, bool) {
		for scope, preview := range scopes {
			key := state.ensureKey(scope, now)
			// The preview is derived from the live key, so it is authoritative
			// and overwrites whatever a usage record produced earlier.
			key.Preview = preview
			if key.InConfig {
				result.Matched++
			} else {
				result.Added++
			}
			key.InConfig = true
		}
		for scope, key := range state.Keys {
			if _, listed := scopes[scope]; listed || key == nil {
				continue
			}
			if key.InConfig {
				delete(state.Keys, scope)
				result.Removed++
				continue
			}
			result.Retained++
		}
		if result.Removed > 0 {
			pruneLogOrphans(state)
		}
		state.LastSyncAt = now
		return struct{}{}, true
	})
	return result, nil
}

type KeySummary struct {
	Scope    string  `json:"scope"`
	Preview  string  `json:"preview,omitempty"`
	Label    string  `json:"label,omitempty"`
	PlanID   string  `json:"plan_id,omitempty"`
	CostUSD  float64 `json:"cost_usd"`
	Requests int64   `json:"requests"`
	Blocked  bool    `json:"blocked"`
}

type StatsView struct {
	GeneratedAt   time.Time     `json:"generated_at"`
	Keys          int           `json:"keys"`
	BoundKeys     int           `json:"bound_keys"`
	BlockedKeys   int           `json:"blocked_keys"`
	Lifetime      Totals        `json:"lifetime"`
	CycleSpentUSD float64       `json:"cycle_spent_usd"`
	CycleLimitUSD float64       `json:"cycle_limit_usd"`
	ByModel       []ModelTotals `json:"by_model"`
	TopKeys       []KeySummary  `json:"top_keys"`
	LastSyncAt    time.Time     `json:"last_sync_at,omitzero"`
}

// Stats aggregates every tracked key. It reuses KeyDirectory so the totals can
// never disagree with the per-key rows shown beside them.
func (s *Store) Stats() StatsView {
	directory := s.KeyDirectory()
	stats := StatsView{
		GeneratedAt: directory.GeneratedAt,
		Keys:        len(directory.Keys),
		LastSyncAt:  directory.LastSyncAt,
		ByModel:     []ModelTotals{},
		TopKeys:     []KeySummary{},
	}
	byModel := make(map[string]*Totals)
	for _, view := range directory.Keys {
		stats.Lifetime.Add(view.Lifetime)
		if view.PlanID != "" {
			stats.BoundKeys++
			stats.CycleSpentUSD += view.SpentUSD
			stats.CycleLimitUSD += view.LimitUSD
		}
		if view.Blocked {
			stats.BlockedKeys++
		}
		for _, entry := range view.ByModel {
			totals := byModel[entry.Model]
			if totals == nil {
				totals = &Totals{}
				byModel[entry.Model] = totals
			}
			totals.Add(entry.Totals)
		}
		stats.TopKeys = append(stats.TopKeys, KeySummary{
			Scope:    view.Scope,
			Preview:  view.Preview,
			Label:    view.Label,
			PlanID:   view.PlanID,
			CostUSD:  view.Lifetime.CostUSD,
			Requests: view.Lifetime.Requests,
			Blocked:  view.Blocked,
		})
	}
	for model, totals := range byModel {
		stats.ByModel = append(stats.ByModel, ModelTotals{Model: model, Totals: *totals})
	}
	sortModelTotals(stats.ByModel)
	sort.Slice(stats.TopKeys, func(i, j int) bool {
		if stats.TopKeys[i].CostUSD != stats.TopKeys[j].CostUSD {
			return stats.TopKeys[i].CostUSD > stats.TopKeys[j].CostUSD
		}
		return stats.TopKeys[i].Scope < stats.TopKeys[j].Scope
	})
	if len(stats.TopKeys) > MaxTopKeys {
		stats.TopKeys = stats.TopKeys[:MaxTopKeys]
	}
	return stats
}

// normalizeScopes trims, lowercases, and de-duplicates caller scopes. Scopes
// are hex digests, so case folding is safe and makes hand-typed input work.
func normalizeScopes(scopes []string) []string {
	normalized := make([]string, 0, len(scopes))
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scope = strings.ToLower(strings.TrimSpace(scope))
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

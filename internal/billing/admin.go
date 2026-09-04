package billing

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

type PriceRow struct {
	PriceRule
	Source PriceSource `json:"source"`
}

func (s *Store) PriceRows() []PriceRow {
	loaded := builtinCatalog()
	rows := []PriceRow{}
	s.read(func(state *State) {
		for _, rule := range state.Prices {
			rows = append(rows, PriceRow{PriceRule: rule, Source: priceSourceOfCatalog(rule, loaded)})
		}
	})
	return rows
}

type CatalogRefreshResult struct {
	Catalog       CatalogInfo `json:"catalog"`
	UpdatedModels int         `json:"updated_models"`
}

// Refreshing advances rows that followed the previous built-in value while
// preserving explicit custom prices.
func (s *Store) RefreshPriceCatalog() (CatalogRefreshResult, error) {
	previous := cachedBuiltinCatalog()
	info, errRefresh := RefreshBuiltinCatalog()
	if errRefresh != nil {
		return CatalogRefreshResult{}, errRefresh
	}
	current := builtinCatalog()
	updated := updateResult(s, func(state *State) (int, Changes) {
		changed := 0
		for i := range state.Prices {
			rule := state.Prices[i]
			previousDefault, previouslyKnown := lookupCatalog(previous, rule.Pattern, "")
			followedBuiltin := previouslyKnown && samePrice(rule, previousDefault)
			if !previouslyKnown && rule.InputPer1M == 0 && rule.OutputPer1M == 0 && rule.CacheReadPer1M == nil && rule.CacheWritePer1M == nil && rule.LongContext == nil {
				followedBuiltin = true
			}
			if !followedBuiltin {
				continue
			}
			fresh, known := lookupCatalog(current, rule.Pattern, "")
			if !known {
				fresh = PriceRule{Pattern: rule.Pattern}
			} else {
				fresh.Pattern = rule.Pattern
			}
			if samePrice(rule, fresh) {
				continue
			}
			state.Prices[i] = fresh
			changed++
		}
		return changed, Changes{Prices: changed > 0}
	})
	return CatalogRefreshResult{Catalog: info, UpdatedModels: updated}, nil
}

func priceSourceOfCatalog(rule PriceRule, loaded *catalog) PriceSource {
	def, known := lookupCatalog(loaded, rule.Pattern, "")
	if !known {
		if rule.InputPer1M == 0 && rule.OutputPer1M == 0 && rule.CacheReadPer1M == nil && rule.CacheWritePer1M == nil && rule.LongContext == nil {
			return PriceSourceNone
		}
		return PriceSourceCustom
	}
	if samePrice(rule, def) {
		return PriceSourceBuiltin
	}
	return PriceSourceCustom
}

func samePrice(a, b PriceRule) bool {
	return a.InputPer1M == b.InputPer1M &&
		a.OutputPer1M == b.OutputPer1M &&
		sameOptionalPrice(a.CacheReadPer1M, b.CacheReadPer1M) &&
		sameOptionalPrice(a.CacheWritePer1M, b.CacheWritePer1M) &&
		sameLongContextPrice(a.LongContext, b.LongContext)
}

func sameLongContextPrice(a, b *LongContextPrice) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.ThresholdInputTokens == b.ThresholdInputTokens &&
		a.InputPer1M == b.InputPer1M && a.OutputPer1M == b.OutputPer1M &&
		sameOptionalPrice(a.CacheReadPer1M, b.CacheReadPer1M) &&
		sameOptionalPrice(a.CacheWritePer1M, b.CacheWritePer1M)
}

func sameOptionalPrice(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

type PriceCatalogSyncResult struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Priced  int `json:"priced"`
}

// New models arrive priced from the runtime catalog, or at zero when it has no
// entry for them. Models that disappeared are dropped. Rows that survive keep
// whatever an administrator set, because a model list refresh must never quietly
// undo an edit.
//
// Glob rows cannot be reconciled against model names, so they are preserved.
func (s *Store) SyncPriceCatalog(models []string) (PriceCatalogSyncResult, error) {
	wanted := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		lowered := strings.ToLower(model)
		if _, exists := seen[lowered]; exists {
			continue
		}
		seen[lowered] = struct{}{}
		wanted = append(wanted, model)
	}
	if len(wanted) == 0 {
		// An empty list is far more likely to be a failed read than a proxy
		// that serves nothing, and wiping the table would discard every edit.
		return PriceCatalogSyncResult{}, invalidf("模型列表为空，未执行同步")
	}

	var result PriceCatalogSyncResult
	kept := 0
	loaded := builtinCatalog()
	updateResult(s, func(state *State) (struct{}, Changes) {
		existing := make(map[string]PriceRule, len(state.Prices))
		var globs []PriceRule
		for _, rule := range state.Prices {
			if isGlob(rule.Pattern) {
				globs = append(globs, rule)
				continue
			}
			existing[strings.ToLower(strings.TrimSpace(rule.Pattern))] = rule
		}
		rows := make([]PriceRule, 0, len(wanted)+len(globs))
		for _, model := range wanted {
			if rule, exists := existing[strings.ToLower(model)]; exists {
				rows = append(rows, rule)
				kept++
				continue
			}
			seeded, known := lookupCatalog(loaded, model, "")
			if !known {
				seeded = PriceRule{Pattern: model}
			} else {
				seeded.Pattern = model
			}
			rows = append(rows, seeded)
			result.Added++
			if known {
				result.Priced++
			}
		}
		result.Removed = len(existing) - kept
		// Keep the proxy's own ordering: it is what the operator sees elsewhere.
		// Globs go last, which is also where ResolvePrice consults them.
		next := append(rows, globs...)
		// The panel synchronizes on every session start. A kept rule is the
		// stored value itself, pointer fields and all, so an unchanged list is
		// equal to the stored one and not worth rewriting the price table for.
		if slices.Equal(state.Prices, next) {
			return struct{}{}, Changes{}
		}
		state.Prices = next
		return struct{}{}, Changes{Prices: true}
	})
	return result, nil
}

func (r PriceRule) Validate() error {
	pattern := strings.TrimSpace(r.Pattern)
	if pattern == "" {
		return invalidf("模型名称或匹配规则不能为空")
	}
	if invalidPrice(r.InputPer1M) || invalidPrice(r.OutputPer1M) {
		return invalidf("模型 %q：Token 单价必须是有限的非负数", pattern)
	}
	if r.CacheReadPer1M != nil && invalidPrice(*r.CacheReadPer1M) {
		return invalidf("模型 %q：缓存读取单价必须是有限的非负数", pattern)
	}
	if r.CacheWritePer1M != nil && invalidPrice(*r.CacheWritePer1M) {
		return invalidf("模型 %q：缓存写入单价必须是有限的非负数", pattern)
	}
	if tier := r.LongContext; tier != nil {
		if tier.ThresholdInputTokens <= 0 {
			return invalidf("模型 %q：长上下文阈值必须大于 0", pattern)
		}
		if invalidPrice(tier.InputPer1M) || invalidPrice(tier.OutputPer1M) {
			return invalidf("模型 %q：长上下文 Token 单价必须是有限的非负数", pattern)
		}
		if tier.CacheReadPer1M != nil && invalidPrice(*tier.CacheReadPer1M) {
			return invalidf("模型 %q：长上下文缓存读取单价必须是有限的非负数", pattern)
		}
		if tier.CacheWritePer1M != nil && invalidPrice(*tier.CacheWritePer1M) {
			return invalidf("模型 %q：长上下文缓存写入单价必须是有限的非负数", pattern)
		}
	}
	return nil
}

func invalidPrice(value float64) bool {
	return value < 0 || math.IsNaN(value) || math.IsInf(value, 0)
}

func (s *Store) UpsertPrice(rule PriceRule) (PriceRule, error) {
	rule.Pattern = strings.TrimSpace(rule.Pattern)
	if errValidate := rule.Validate(); errValidate != nil {
		return PriceRule{}, errValidate
	}
	return updateResult(s, func(state *State) (PriceRule, Changes) {
		for i := range state.Prices {
			if strings.EqualFold(strings.TrimSpace(state.Prices[i].Pattern), rule.Pattern) {
				state.Prices[i] = rule
				return rule, Changes{Prices: true}
			}
		}
		state.Prices = append(state.Prices, rule)
		return rule, Changes{Prices: true}
	}), nil
}

// ResetPrices restores every row to its catalog default, dropping edits. Models
// the catalog does not know go back to zero. It reports how many rows changed.
func (s *Store) ResetPrices() int {
	loaded := builtinCatalog()
	return updateResult(s, func(state *State) (int, Changes) {
		changed := 0
		for i := range state.Prices {
			pattern := state.Prices[i].Pattern
			def, known := lookupCatalog(loaded, pattern, "")
			if !known {
				def = PriceRule{Pattern: pattern}
			} else {
				def.Pattern = pattern
			}
			if samePrice(state.Prices[i], def) {
				continue
			}
			state.Prices[i] = def
			changed++
		}
		return changed, Changes{Prices: changed > 0}
	})
}

func (s *Store) Plans() []Plan {
	plans := []Plan{}
	s.read(func(state *State) { plans = append(plans, state.Plans...) })
	return plans
}

// CreatePlanWithBindings creates a plan and binds the selected currently
// unbound keys in the same state transaction.
func (s *Store) CreatePlanWithBindings(plan Plan, scopes []string) (Plan, error) {
	plan.ID = strings.TrimSpace(plan.ID)
	plan.Name = strings.TrimSpace(plan.Name)
	scopes = normalizeScopes(scopes)
	var errApply error
	stored := updateResult(s, func(state *State) (Plan, Changes) {
		if plan.ID == "" {
			plan.ID = state.freePlanID(plan.Name)
		}
		if errValidate := plan.Validate(); errValidate != nil {
			errApply = errValidate
			return Plan{}, Changes{}
		}
		if _, exists := state.FindPlan(plan.ID); exists {
			errApply = conflictf("订阅计划 %q 已存在", plan.ID)
			return Plan{}, Changes{}
		}
		if plan.Name == "" {
			plan.Name = plan.ID
		}
		for _, scope := range scopes {
			key := state.liveKey(scope)
			if key == nil {
				errApply = notFoundf("API Key %q 不存在，请先同步 Key 列表", scope)
				return Plan{}, Changes{}
			}
			if key.PlanID != "" {
				errApply = conflictf("API Key %q 已绑定其他订阅计划", scope)
				return Plan{}, Changes{}
			}
		}
		state.Plans = append(state.Plans, plan)
		for _, scope := range scopes {
			state.Keys[scope].PlanID = plan.ID
			state.Keys[scope].Cycle = Cycle{}
		}
		return plan, Changes{Plans: true, Keys: scopes}
	})
	return stored, errApply
}

type PlanPatch struct {
	ID            string   `json:"id"`
	Name          *string  `json:"name,omitempty"`
	AmountUSD     *float64 `json:"amount_usd,omitempty"`
	PeriodSeconds *int64   `json:"period_seconds,omitempty"`
}

// UpdatePlanWithBindings applies a plan edit and, when scopes is non-nil,
// replaces the plan's complete key set. Selected keys may be unbound or already
// on this plan; keys owned by another plan are rejected atomically.
func (s *Store) UpdatePlanWithBindings(patch PlanPatch, scopes *[]string) (Plan, error) {
	patch.ID = strings.TrimSpace(patch.ID)
	if patch.ID == "" {
		return Plan{}, invalidf("订阅计划 ID 不能为空")
	}

	var errApply error
	stored := updateResult(s, func(state *State) (Plan, Changes) {
		for i := range state.Plans {
			if state.Plans[i].ID != patch.ID {
				continue
			}
			updated := state.Plans[i]
			if patch.Name != nil {
				updated.Name = strings.TrimSpace(*patch.Name)
			}
			if patch.AmountUSD != nil {
				updated.AmountUSD = *patch.AmountUSD
			}
			if patch.PeriodSeconds != nil {
				updated.PeriodSeconds = *patch.PeriodSeconds
			}
			if errValidate := updated.Validate(); errValidate != nil {
				errApply = errValidate
				return Plan{}, Changes{}
			}
			var selected map[string]struct{}
			if scopes != nil {
				normalized := normalizeScopes(*scopes)
				selected = make(map[string]struct{}, len(normalized))
				for _, scope := range normalized {
					key := state.liveKey(scope)
					if key == nil {
						errApply = notFoundf("API Key %q 不存在，请先同步 Key 列表", scope)
						return Plan{}, Changes{}
					}
					if key.PlanID != "" && key.PlanID != patch.ID {
						errApply = conflictf("API Key %q 已绑定其他订阅计划", scope)
						return Plan{}, Changes{}
					}
					selected[scope] = struct{}{}
				}
			}

			periodChanged := updated.PeriodSeconds != state.Plans[i].PeriodSeconds
			if periodChanged || scopes != nil {
				for scope, key := range state.Keys {
					// A deleted key is absent from the editor, so its absence
					// from the selection says nothing. Unbinding it here would
					// throw away the binding kept for a later re-add.
					if key == nil || !key.DeletedAt.IsZero() {
						continue
					}
					_, shouldBind := selected[scope]
					if key.PlanID == patch.ID && (periodChanged || !shouldBind) {
						key.Cycle = Cycle{}
					}
					if scopes != nil && key.PlanID == patch.ID && !shouldBind {
						key.PlanID = ""
					}
				}
			}
			if scopes != nil {
				for scope := range selected {
					key := state.Keys[scope]
					if key.PlanID == "" {
						key.PlanID = patch.ID
						key.Cycle = Cycle{}
					}
				}
			}
			state.Plans[i] = updated
			return updated, Changes{Plans: true, AllKeys: true}
		}
		errApply = notFoundf("订阅计划 %q 不存在", patch.ID)
		return Plan{}, Changes{}
	})
	return stored, errApply
}

func (s *Store) DeletePlan(id string) (int, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return 0, invalidf("订阅计划 ID 不能为空")
	}

	var errApply error
	unbound := updateResult(s, func(state *State) (int, Changes) {
		index := slices.IndexFunc(state.Plans, func(plan Plan) bool { return plan.ID == id })
		if index < 0 {
			errApply = notFoundf("订阅计划 %q 不存在", id)
			return 0, Changes{}
		}
		state.Plans = slices.Delete(state.Plans, index, index+1)

		released := 0
		for _, key := range state.Keys {
			if key == nil || key.PlanID != id {
				continue
			}
			key.PlanID = ""
			key.Cycle = Cycle{}
			released++
		}
		return released, Changes{Plans: true, AllKeys: true}
	})
	return unbound, errApply
}

func (s *State) freePlanID(name string) string {
	return freeID(name, "plan", func(id string) bool {
		_, exists := s.FindPlan(id)
		return exists
	})
}

// freeID turns a display name into an identifier nothing else answers to.
func freeID(name, prefix string, taken func(string) bool) string {
	// A slug with no letters is not a readable identifier: "日额度 0.003" would
	// otherwise become "0-003". The counter below reads better.
	if base := slugify(name); strings.ContainsAny(base, "abcdefghijklmnopqrstuvwxyz") {
		if !taken(base) {
			return base
		}
		for i := 2; i < 1000; i++ {
			if candidate := fmt.Sprintf("%s-%d", base, i); !taken(candidate) {
				return candidate
			}
		}
	}
	for i := 1; ; i++ {
		if candidate := fmt.Sprintf("%s-%d", prefix, i); !taken(candidate) {
			return candidate
		}
	}
}

func slugify(value string) string {
	var builder strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen && builder.Len() > 0 {
				builder.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	return slug
}

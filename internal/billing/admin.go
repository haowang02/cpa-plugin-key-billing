package billing

import (
	"fmt"
	"strings"
	"time"
)

// Administrative limits. They bound the state document rather than express
// policy: the whole document is loaded on every start, so an unbounded table
// would eventually turn into a plugin that fails to load.
const (
	// MaxPriceRules caps the price table, which holds one row per model.
	MaxPriceRules = 5000
	// MaxPlans caps the number of subscription definitions.
	MaxPlans = 200
	// MaxPatternLength caps a price rule pattern.
	MaxPatternLength = 200
	// MaxNameLength caps plan names and key labels.
	MaxNameLength = 120
)

// PriceRow is one line of the price table: what a model currently bills at,
// plus where that number came from.
type PriceRow struct {
	PriceRule
	// Source is builtin when the row still matches the shipped catalog, custom
	// when it was edited, and none when the catalog has no entry and the model
	// bills at zero.
	Source PriceSource `json:"source"`
}

// PriceTable is the whole per-model price list.
//
// It holds one row per model the proxy currently serves, not a sparse set of
// overrides: an operator asking "what does this cost" should not have to know
// whether the answer comes from a rule they wrote or from the shipped catalog.
type PriceTable struct {
	CatalogVersion string      `json:"catalog_version"`
	Catalog        CatalogInfo `json:"catalog"`
	Models         []PriceRow  `json:"models"`
}

// PriceTable returns every priced model.
func (s *Store) PriceTable() PriceTable {
	table := PriceTable{
		Catalog: BuiltinCatalog(),
		Models:  []PriceRow{},
	}
	table.CatalogVersion = table.Catalog.FetchedAt
	s.Read(func(state *State) {
		for _, rule := range state.Prices {
			table.Models = append(table.Models, PriceRow{PriceRule: rule, Source: priceSourceOf(rule)})
		}
	})
	return table
}

// priceSourceOf classifies a stored row against the shipped catalog.
func priceSourceOf(rule PriceRule) PriceSource {
	def, known := CatalogDefault(rule.Pattern)
	if !known {
		if rule.InputPer1M == 0 && rule.OutputPer1M == 0 && rule.CacheReadPer1M == nil && rule.CacheWritePer1M == nil {
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
		sameOptionalPrice(a.CacheWritePer1M, b.CacheWritePer1M)
}

func sameOptionalPrice(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ModelSyncResult reports what one model-list reconciliation changed.
type ModelSyncResult struct {
	Received int `json:"received"`
	Added    int `json:"added"`
	Kept     int `json:"kept"`
	Removed  int `json:"removed"`
	// Priced counts the added rows the catalog had a published price for; the
	// rest were seeded at zero and need an operator to fill them in.
	Priced int `json:"priced"`
}

// SyncModels reconciles the price table with the models the proxy serves.
//
// New models arrive priced from the shipped catalog, or at zero when it has no
// entry for them. Models that disappeared are dropped. Rows that survive keep
// whatever an administrator set, because a model list refresh must never quietly
// undo an edit.
//
// Glob rows are carried across untouched. Only a row naming a model can be
// reconciled against a list of model names; a pattern is a standing instruction
// from an administrator, and dropping it for not being a model would delete
// pricing nobody asked to delete — silently, since the admin page syncs every
// time it is opened.
func (s *Store) SyncModels(models []string) (ModelSyncResult, error) {
	if len(models) > MaxPriceRules {
		return ModelSyncResult{}, invalidf("模型数量过多：最多允许 %d 个，实际收到 %d 个", MaxPriceRules, len(models))
	}
	wanted := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || len(model) > MaxPatternLength {
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
		return ModelSyncResult{}, invalidf("拒绝同步空的模型列表")
	}

	result := ModelSyncResult{Received: len(wanted)}
	now := s.Now()
	s.Update(func(state *State) {
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
				result.Kept++
				continue
			}
			seeded, known := CatalogDefault(model)
			seeded.UpdatedAt = now
			rows = append(rows, seeded)
			result.Added++
			if known {
				result.Priced++
			}
		}
		result.Removed = len(existing) - result.Kept
		// Keep the proxy's own ordering: it is what the operator sees elsewhere.
		// Globs go last, which is also where ResolvePrice consults them.
		state.Prices = append(rows, globs...)
	})
	return result, nil
}

// Validate reports whether a price rule can be stored.
func (r PriceRule) Validate() error {
	pattern := strings.TrimSpace(r.Pattern)
	if pattern == "" {
		return invalidf("模型名称或匹配规则不能为空")
	}
	if len(pattern) > MaxPatternLength {
		return invalidf("模型名称或匹配规则不能超过 %d 个字符", MaxPatternLength)
	}
	if r.InputPer1M < 0 || r.OutputPer1M < 0 {
		return invalidf("模型 %q：Token 单价不能为负数", pattern)
	}
	if r.CacheReadPer1M != nil && *r.CacheReadPer1M < 0 {
		return invalidf("模型 %q：缓存读取单价不能为负数", pattern)
	}
	if r.CacheWritePer1M != nil && *r.CacheWritePer1M < 0 {
		return invalidf("模型 %q：缓存写入单价不能为负数", pattern)
	}
	return nil
}

// UpsertPrice stores one row, replacing any existing row with the same pattern.
// Matching is case-insensitive because model names are.
func (s *Store) UpsertPrice(rule PriceRule) (PriceRule, error) {
	rule.Pattern = strings.TrimSpace(rule.Pattern)
	if errValidate := rule.Validate(); errValidate != nil {
		return PriceRule{}, errValidate
	}
	rule.UpdatedAt = s.Now()

	var errApply error
	stored := UpdateResult(s, func(state *State) (PriceRule, bool) {
		for i := range state.Prices {
			if strings.EqualFold(strings.TrimSpace(state.Prices[i].Pattern), rule.Pattern) {
				state.Prices[i] = rule
				return rule, true
			}
		}
		if len(state.Prices) >= MaxPriceRules {
			errApply = invalidf("模型价格表已满，最多允许 %d 条", MaxPriceRules)
			return PriceRule{}, false
		}
		state.Prices = append(state.Prices, rule)
		return rule, true
	})
	return stored, errApply
}

// ReplacePrices swaps the whole table, which is what a table editor saving
// every row at once needs. Rows keep the order they arrive in, since glob
// patterns are matched in order.
func (s *Store) ReplacePrices(rules []PriceRule) ([]PriceRule, error) {
	if len(rules) > MaxPriceRules {
		return nil, invalidf("模型价格表已满：最多允许 %d 条，实际收到 %d 条", MaxPriceRules, len(rules))
	}
	now := s.Now()
	seen := make(map[string]struct{}, len(rules))
	normalized := make([]PriceRule, 0, len(rules))
	for _, rule := range rules {
		rule.Pattern = strings.TrimSpace(rule.Pattern)
		if errValidate := rule.Validate(); errValidate != nil {
			return nil, errValidate
		}
		lowered := strings.ToLower(rule.Pattern)
		if _, exists := seen[lowered]; exists {
			return nil, conflictf("模型名称或匹配规则 %q 重复", rule.Pattern)
		}
		seen[lowered] = struct{}{}
		if rule.UpdatedAt.IsZero() {
			rule.UpdatedAt = now
		}
		normalized = append(normalized, rule)
	}
	s.Update(func(state *State) {
		state.Prices = normalized
	})
	return normalized, nil
}

// ResetPrices restores every row to its catalog default, dropping edits. Models
// the catalog does not know go back to zero. It reports how many rows changed.
func (s *Store) ResetPrices() int {
	now := s.Now()
	return UpdateResult(s, func(state *State) (int, bool) {
		changed := 0
		for i := range state.Prices {
			def, _ := CatalogDefault(state.Prices[i].Pattern)
			if samePrice(state.Prices[i], def) {
				continue
			}
			def.UpdatedAt = now
			state.Prices[i] = def
			changed++
		}
		return changed, changed > 0
	})
}

// PlanView is a plan plus the derived facts the admin UI shows next to it.
type PlanView struct {
	Plan
	BoundKeys   int       `json:"bound_keys"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
}

// PlanViews lists every plan with its current window and binding count.
func (s *Store) PlanViews() []PlanView {
	now := s.Now()
	views := []PlanView{}
	s.Read(func(state *State) {
		bound := make(map[string]int, len(state.Plans))
		for _, key := range state.Keys {
			if key != nil && key.PlanID != "" {
				bound[key.PlanID]++
			}
		}
		for _, plan := range state.Plans {
			start, end := plan.CycleWindow(now, s.locationLocked())
			views = append(views, PlanView{
				Plan:        plan,
				BoundKeys:   bound[plan.ID],
				WindowStart: start,
				WindowEnd:   end,
			})
		}
	})
	return views
}

// CreatePlan stores a new subscription. An empty ID is derived from the name,
// so the UI can offer a single "name" field.
func (s *Store) CreatePlan(plan Plan) (Plan, error) {
	plan.ID = strings.TrimSpace(plan.ID)
	plan.Name = strings.TrimSpace(plan.Name)
	if len(plan.Name) > MaxNameLength {
		return Plan{}, invalidf("订阅计划名称不能超过 %d 个字符", MaxNameLength)
	}
	if plan.Period.Kind == "" {
		plan.Period.Kind = PeriodDaily
	}
	now := s.Now()
	plan.CreatedAt = now
	plan.UpdatedAt = now

	var errApply error
	stored := UpdateResult(s, func(state *State) (Plan, bool) {
		if plan.ID == "" {
			plan.ID = state.freePlanID(plan.Name)
		}
		if errValidate := plan.Validate(); errValidate != nil {
			errApply = errValidate
			return Plan{}, false
		}
		if _, exists := state.FindPlan(plan.ID); exists {
			errApply = conflictf("订阅计划 %q 已存在", plan.ID)
			return Plan{}, false
		}
		if len(state.Plans) >= MaxPlans {
			errApply = invalidf("订阅计划数量已达上限（%d 个）", MaxPlans)
			return Plan{}, false
		}
		if plan.Name == "" {
			plan.Name = plan.ID
		}
		state.Plans = append(state.Plans, plan)
		return plan, true
	})
	return stored, errApply
}

// PlanPatch is a partial plan update. A nil field is left untouched, which lets
// the UI edit one attribute without resending the whole record.
type PlanPatch struct {
	ID        string   `json:"id"`
	Name      *string  `json:"name,omitempty"`
	AmountUSD *float64 `json:"amount_usd,omitempty"`
	Period    *Period  `json:"period,omitempty"`
}

// UpdatePlan applies a partial update.
//
// Changing the period moves the window boundaries; bound keys pick that up on
// their next request, when rollCycle notices the mismatch and opens a fresh
// window. That is deliberate: a plan edit takes effect immediately rather than
// at the next natural reset.
func (s *Store) UpdatePlan(patch PlanPatch) (Plan, error) {
	patch.ID = strings.TrimSpace(patch.ID)
	if patch.ID == "" {
		return Plan{}, invalidf("订阅计划 ID 不能为空")
	}

	var errApply error
	stored := UpdateResult(s, func(state *State) (Plan, bool) {
		for i := range state.Plans {
			if state.Plans[i].ID != patch.ID {
				continue
			}
			updated := state.Plans[i]
			if patch.Name != nil {
				name := strings.TrimSpace(*patch.Name)
				if len(name) > MaxNameLength {
					errApply = invalidf("订阅计划名称不能超过 %d 个字符", MaxNameLength)
					return Plan{}, false
				}
				updated.Name = name
			}
			if patch.AmountUSD != nil {
				updated.AmountUSD = *patch.AmountUSD
			}
			if patch.Period != nil {
				updated.Period = *patch.Period
			}
			if errValidate := updated.Validate(); errValidate != nil {
				errApply = errValidate
				return Plan{}, false
			}
			updated.UpdatedAt = s.Now()
			state.Plans[i] = updated
			return updated, true
		}
		errApply = notFoundf("订阅计划 %q 不存在", patch.ID)
		return Plan{}, false
	})
	return stored, errApply
}

// DeletePlan removes a plan and unbinds every key that used it, reporting how
// many keys were released. Their spend is retained in the trend history.
func (s *Store) DeletePlan(id string) (int, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return 0, invalidf("订阅计划 ID 不能为空")
	}

	var errApply error
	unbound := UpdateResult(s, func(state *State) (int, bool) {
		index := -1
		for i := range state.Plans {
			if state.Plans[i].ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			errApply = notFoundf("订阅计划 %q 不存在", id)
			return 0, false
		}
		limit := state.Plans[index].AmountUSD
		state.Plans = append(state.Plans[:index], state.Plans[index+1:]...)

		released := 0
		for _, key := range state.Keys {
			if key == nil || key.PlanID != id {
				continue
			}
			archiveCycle(key, limit, id)
			key.PlanID = ""
			key.Cycle = Cycle{}
			released++
		}
		return released, true
	})
	return unbound, errApply
}

// freePlanID derives an unused plan ID from a name, falling back to a counter
// when the name yields no usable slug.
func (s *State) freePlanID(name string) string {
	base := slugify(name)
	if !strings.ContainsAny(base, "abcdefghijklmnopqrstuvwxyz") {
		// A slug with no letters is not a readable identifier: "日额度 0.003"
		// would otherwise become "0-003". A counter reads better.
		base = ""
	}
	if base != "" {
		if _, exists := s.FindPlan(base); !exists {
			return base
		}
		for i := 2; i < 1000; i++ {
			candidate := fmt.Sprintf("%s-%d", base, i)
			if _, exists := s.FindPlan(candidate); !exists {
				return candidate
			}
		}
	}
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("plan-%d", i)
		if _, exists := s.FindPlan(candidate); !exists {
			return candidate
		}
	}
}

// slugify reduces a display name to a lowercase ASCII identifier. Characters
// outside [a-z0-9] collapse into single hyphens.
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

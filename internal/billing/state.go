package billing

import "time"

// StateVersion is bumped whenever the on-disk document changes shape in a way
// that needs migration. Load() refuses documents from a newer version rather
// than silently dropping fields it does not understand.
const StateVersion = 1

// MaxModelsPerKey caps the per-key model breakdown. Overflow is folded into
// OtherModelsBucket so a key that sweeps through many model names cannot grow
// the state file without bound.
const MaxModelsPerKey = 200

const OtherModelsBucket = "__other__"

// MaxRecentCycles is how many closed billing cycles are retained per key.
const MaxRecentCycles = 12

type State struct {
	Version int `json:"version"`
	// Prices is one row per model the proxy serves, seeded from the built-in
	// catalog and editable. It is kept in the proxy's own model order.
	Prices []PriceRule `json:"prices"`
	// Plans are the subscription definitions keys can be bound to.
	Plans []Plan `json:"plans"`
	// Keys maps a caller scope to its binding, current cycle, and statistics.
	Keys map[string]*KeyState `json:"keys"`
	// Log is the retained per-request billing log, oldest first, bounded by the
	// configured retention. It is the one place individual requests survive;
	// everything else here is an aggregate.
	Log []LogEntry `json:"log,omitempty"`
	// LastSyncAt is when the admin UI last pushed the CPA key list.
	LastSyncAt time.Time `json:"last_sync_at,omitzero"`
}

func NewState() *State {
	return &State{Version: StateVersion, Keys: make(map[string]*KeyState)}
}

// PriceRule prices one model. Pattern is the model or alias name, matched
// case-insensitively; a pattern containing '*' or '?' is still honoured as a
// glob for rules written by hand, though SyncModels does not preserve those.
// All prices are USD per 1,000,000 tokens.
//
// The cache prices are pointers so "not specified" and "explicitly free" stay
// distinguishable. Unspecified falls back to the input price, because a
// Claude-style request can be almost entirely cache reads and silently billing
// those at zero would under-charge by an order of magnitude. Set them to 0 to
// really mean free.
type PriceRule struct {
	Pattern         string    `json:"pattern"`
	InputPer1M      float64   `json:"input_per_1m"`
	OutputPer1M     float64   `json:"output_per_1m"`
	CacheReadPer1M  *float64  `json:"cache_read_per_1m,omitempty"`
	CacheWritePer1M *float64  `json:"cache_write_per_1m,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitzero"`
}

type PeriodKind string

const (
	// PeriodDaily resets at midnight.
	PeriodDaily PeriodKind = "daily"
	// PeriodWeekly resets at midnight on Monday.
	PeriodWeekly PeriodKind = "weekly"
	// PeriodMonthly resets at midnight on the first of the month.
	PeriodMonthly PeriodKind = "monthly"
	// PeriodCustom resets every Period.Seconds from Period.Anchor.
	PeriodCustom PeriodKind = "custom"
)

// Period describes a plan's reset schedule.
//
// The calendar kinds have no knobs: a week starts on Monday and a month starts
// on the first. Letting each plan pick its own start day made every budget
// question ("has this key reset yet?") require looking the plan up first, for a
// choice nobody actually needs.
type Period struct {
	Kind PeriodKind `json:"kind"`
	// Seconds is the window length for PeriodCustom.
	Seconds int64 `json:"seconds,omitempty"`
	// Anchor is the origin of the first PeriodCustom window.
	Anchor time.Time `json:"anchor,omitzero"`
}

// Plan is a subscription: an amount of spend per repeating period.
//
// Periods are anchored in the plugin's configured time zone. Plans deliberately
// carry no zone of their own: one deployment-wide answer to "when does the day
// roll over" is far easier to reason about than a per-plan one, and a plan whose
// window silently disagreed with the rest would be a support nightmare.
type Plan struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	AmountUSD float64   `json:"amount_usd"`
	Period    Period    `json:"period"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// KeyState is everything tracked for one downstream API key, identified only by
// its caller scope. The plaintext key is never stored.
type KeyState struct {
	Scope string `json:"scope"`
	// Preview is a masked rendering of the key for display, learned from a
	// usage record or from the key list pushed by the admin UI.
	Preview string `json:"preview,omitempty"`
	// Label is an operator-supplied name.
	Label string `json:"label,omitempty"`
	// InConfig records that this scope appeared in a key list pushed by the
	// admin UI. It is what makes pruning safe: a scope that was once in the
	// list and later vanished has genuinely been deleted from CPA, while a
	// scope only ever seen in traffic may belong to another access provider
	// and is kept until an operator removes it explicitly.
	InConfig bool `json:"in_config,omitempty"`
	// PlanID binds this key to a subscription. Empty means unlimited.
	PlanID string `json:"plan_id,omitempty"`
	// Cycle is the current billing window. Zero when no plan is bound.
	Cycle Cycle `json:"cycle"`
	// Lifetime accumulates across all cycles and survives plan changes.
	Lifetime Totals `json:"lifetime"`
	// ByModel breaks Lifetime down per model, capped at MaxModelsPerKey.
	ByModel map[string]*Totals `json:"by_model,omitempty"`
	// RecentCycles holds the last MaxRecentCycles closed windows.
	RecentCycles []CycleSummary `json:"recent_cycles,omitempty"`
	FirstSeen    time.Time      `json:"first_seen,omitzero"`
	LastSeen     time.Time      `json:"last_seen,omitzero"`
}

type Cycle struct {
	// PlanID records which plan opened this window, so archiving attributes it
	// correctly even if the key was rebound in the meantime.
	PlanID   string    `json:"plan_id,omitempty"`
	StartAt  time.Time `json:"start_at,omitzero"`
	EndAt    time.Time `json:"end_at,omitzero"`
	SpentUSD float64   `json:"spent_usd"`
	Requests int64     `json:"requests"`
}

type CycleSummary struct {
	StartAt  time.Time `json:"start_at"`
	EndAt    time.Time `json:"end_at"`
	SpentUSD float64   `json:"spent_usd"`
	Requests int64     `json:"requests"`
	LimitUSD float64   `json:"limit_usd"`
	PlanID   string    `json:"plan_id,omitempty"`
}

// Totals is an additive usage counter set.
//
// Token counts are stored post-normalization, which gives them the same meaning
// for every provider:
//
//	total input  = UncachedInputTokens + CacheReadTokens + CacheCreationTokens
//	OutputTokens is the full billed output and always includes ReasoningTokens
//
// Storing raw provider counters instead would make these sums meaningless,
// since Anthropic reports cache outside input while OpenAI reports it inside.
type Totals struct {
	CostUSD             float64 `json:"cost_usd"`
	Requests            int64   `json:"requests"`
	FailedRequests      int64   `json:"failed_requests"`
	UncachedInputTokens int64   `json:"uncached_input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
}

func (t Totals) InputTokens() int64 {
	return t.UncachedInputTokens + t.CacheReadTokens + t.CacheCreationTokens
}

func (t *Totals) Add(other Totals) {
	t.CostUSD += other.CostUSD
	t.Requests += other.Requests
	t.FailedRequests += other.FailedRequests
	t.UncachedInputTokens += other.UncachedInputTokens
	t.OutputTokens += other.OutputTokens
	t.ReasoningTokens += other.ReasoningTokens
	t.CacheReadTokens += other.CacheReadTokens
	t.CacheCreationTokens += other.CacheCreationTokens
}

// normalize repairs a document loaded from disk so the rest of the code can
// assume non-nil maps and a populated Scope on every entry.
func (s *State) normalize() {
	if s.Keys == nil {
		s.Keys = make(map[string]*KeyState)
	}
	for scope, key := range s.Keys {
		if key == nil {
			delete(s.Keys, scope)
			continue
		}
		key.Scope = scope
		if key.ByModel == nil {
			key.ByModel = make(map[string]*Totals)
		}
		for model, totals := range key.ByModel {
			if totals == nil {
				delete(key.ByModel, model)
			}
		}
	}
}

// removeNonPositivePlans migrates the former "zero means unlimited" state to
// its canonical representation: no plan binding. Existing usage is preserved.
func (s *State) removeNonPositivePlans() bool {
	validIDs := make(map[string]struct{}, len(s.Plans))
	removedIDs := make(map[string]struct{})
	plans := s.Plans[:0]
	for _, plan := range s.Plans {
		if plan.AmountUSD > 0 {
			plans = append(plans, plan)
			validIDs[plan.ID] = struct{}{}
		} else {
			removedIDs[plan.ID] = struct{}{}
		}
	}
	if len(plans) == len(s.Plans) {
		return false
	}
	s.Plans = plans
	for _, key := range s.Keys {
		if key == nil || key.PlanID == "" {
			continue
		}
		if _, removed := removedIDs[key.PlanID]; !removed {
			continue
		}
		if _, exists := validIDs[key.PlanID]; exists {
			continue
		}
		archiveCycle(key, 0, key.PlanID)
		key.PlanID = ""
		key.Cycle = Cycle{}
	}
	return true
}

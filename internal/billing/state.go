package billing

import "time"

// StateVersion identifies the only on-disk document shape this build accepts.
const StateVersion = 4

// MaxModelsPerKey caps the per-key model breakdown. Overflow is folded into
// OtherModelsBucket so a key that sweeps through many model names cannot grow
// the state file without bound.
const MaxModelsPerKey = 200

const OtherModelsBucket = "__other__"

const MaxRecentCycles = 12

type State struct {
	Version    int                  `json:"version"`
	Prices     []PriceRule          `json:"prices"`
	Plans      []Plan               `json:"plans"`
	Keys       map[string]*KeyState `json:"keys"`
	Log        []LogEntry           `json:"log,omitempty"`
	LastSyncAt time.Time            `json:"last_sync_at,omitzero"`
}

func NewState() *State {
	return &State{Version: StateVersion, Keys: make(map[string]*KeyState)}
}

// PriceRule prices one model. Pattern is the model name or matching rule,
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
	Pattern         string            `json:"pattern"`
	InputPer1M      float64           `json:"input_per_1m"`
	OutputPer1M     float64           `json:"output_per_1m"`
	CacheReadPer1M  *float64          `json:"cache_read_per_1m,omitempty"`
	CacheWritePer1M *float64          `json:"cache_write_per_1m,omitempty"`
	LongContext     *LongContextPrice `json:"long_context,omitempty"`
	UpdatedAt       time.Time         `json:"updated_at,omitzero"`
}

// LongContextPrice replaces the whole request's rates when total canonical
// input exceeds ThresholdInputTokens. Cache prices use the tier's input price
// when omitted, exactly like the standard price.
type LongContextPrice struct {
	ThresholdInputTokens int64    `json:"threshold_input_tokens"`
	InputPer1M           float64  `json:"input_per_1m"`
	OutputPer1M          float64  `json:"output_per_1m"`
	CacheReadPer1M       *float64 `json:"cache_read_per_1m,omitempty"`
	CacheWritePer1M      *float64 `json:"cache_write_per_1m,omitempty"`
}

type PeriodKind string

const (
	PeriodDaily   PeriodKind = "daily"
	PeriodWeekly  PeriodKind = "weekly"
	PeriodMonthly PeriodKind = "monthly"
	PeriodCustom  PeriodKind = "custom"
	PeriodNever   PeriodKind = "never"
)

// Period describes only a subscription length. Every key supplies its own
// start time on first use; a plan has no shared reset boundary.
type Period struct {
	Kind    PeriodKind `json:"kind"`
	Seconds int64      `json:"seconds,omitempty"`
}

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
	Preview string `json:"preview,omitempty"`
	Label   string `json:"label,omitempty"`
	// InConfig records that this scope appeared in a key list pushed by the
	// admin UI. It is what makes pruning safe: a scope that was once in the
	// list and later vanished has genuinely been deleted from CPA, while a
	// scope only ever seen in traffic may belong to another access provider
	// and must not be pruned by a CPA Key-list sync.
	InConfig     bool               `json:"in_config,omitempty"`
	PlanID       string             `json:"plan_id,omitempty"`
	Cycle        Cycle              `json:"cycle"`
	Lifetime     Totals             `json:"lifetime"`
	ByModel      map[string]*Totals `json:"by_model,omitempty"`
	RecentCycles []CycleSummary     `json:"recent_cycles,omitempty"`
	FirstSeen    time.Time          `json:"first_seen,omitzero"`
	LastSeen     time.Time          `json:"last_seen,omitzero"`
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

// normalize initializes maps omitted from the JSON document when empty.
func (s *State) normalize() {
	if s.Keys == nil {
		s.Keys = make(map[string]*KeyState)
	}
	for scope, key := range s.Keys {
		if key == nil {
			delete(s.Keys, scope)
			continue
		}
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

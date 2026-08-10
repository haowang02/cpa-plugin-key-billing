package billing

import "time"

// Advance StateVersion for releases that change the on-disk shape.
const StateVersion = 6

type State struct {
	Version int                  `json:"version"`
	Prices  []PriceRule          `json:"prices"`
	Plans   []Plan               `json:"plans"`
	Keys    map[string]*KeyState `json:"keys"`
	// Credentials names the upstream credentials seen so far, keyed by the
	// host's runtime auth index. The log stores that index and reads the name
	// from here, so a credential renamed upstream renames its history too.
	Credentials map[string]Credential `json:"credentials,omitempty"`
	Log         []LogEntry            `json:"log,omitempty"`
}

func NewState() *State {
	return &State{Version: StateVersion, Keys: make(map[string]*KeyState)}
}

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
}

// LongContextPrice replaces the whole request's rates when total normalized
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
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	AmountUSD float64 `json:"amount_usd"`
	Period    Period  `json:"period"`
}

// KeyState is identified by caller scope; plaintext keys are never stored.
type KeyState struct {
	Preview string `json:"preview,omitempty"`
	Label   string `json:"label,omitempty"`
	// InConfig records that this scope appeared in a key list pushed by the
	// admin UI. It is what makes pruning safe: a scope that was once in the
	// list and later vanished has genuinely been deleted from CPA, while a
	// scope only ever seen in traffic may belong to another access provider
	// and must not be pruned by a CPA Key-list sync.
	InConfig bool               `json:"in_config,omitempty"`
	PlanID   string             `json:"plan_id,omitempty"`
	Cycle    Cycle              `json:"cycle"`
	Lifetime Totals             `json:"lifetime"`
	ByModel  map[string]*Totals `json:"by_model,omitempty"`
}

type Cycle struct {
	// PlanID records which plan opened this window, so a completion admitted
	// under an earlier binding is recognized as belonging elsewhere.
	PlanID   string    `json:"plan_id,omitempty"`
	StartAt  time.Time `json:"start_at,omitzero"`
	EndAt    time.Time `json:"end_at,omitzero"`
	SpentUSD float64   `json:"spent_usd"`
}

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
	UncachedInputTokens int64   `json:"uncached_input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
}

func (t *Totals) Add(other Totals) {
	t.CostUSD += other.CostUSD
	t.Requests += other.Requests
	t.UncachedInputTokens += other.UncachedInputTokens
	t.OutputTokens += other.OutputTokens
	t.ReasoningTokens += other.ReasoningTokens
	t.CacheReadTokens += other.CacheReadTokens
	t.CacheCreationTokens += other.CacheCreationTokens
}

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

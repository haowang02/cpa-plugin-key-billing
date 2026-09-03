package billing

import "time"

// State is the working set the store keeps in memory. It holds everything the
// request path has to consult without touching disk. Request and error events
// grow with traffic and are queried directly from the repository.
type State struct {
	Prices []PriceRule
	Plans  []Plan
	Keys   map[string]*KeyState
	// Routes are the reusable model/Credential policies. The reserved
	// system:all route is materialized here so every management view has one
	// canonical unrestricted choice, although API keys store unrestricted as
	// an empty binding list.
	Routes []Route
	// Credentials names the upstream credentials seen so far, keyed by the
	// host's runtime auth index. Request events store that index and read the name
	// from here, so a credential renamed upstream renames its history too.
	Credentials map[string]Credential
}

func NewState() *State {
	return &State{Keys: make(map[string]*KeyState), Credentials: make(map[string]Credential)}
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
	// These two tell the three kinds of record apart:
	//
	//	InConfig set     a key CPA currently holds
	//	DeletedAt set    a key CPA held and no longer does
	//	neither set      a principal only ever seen in traffic, which may belong
	//	                 to another access provider and must therefore never be
	//	                 retired by a CPA Key-list sync
	//
	// A deleted key is marked rather than dropped because the record is what
	// gives request history its identity: each event stores a scope and reads the
	// masked key and remark from here.
	InConfig         bool           `json:"in_config,omitempty"`
	DeletedAt        time.Time      `json:"deleted_at,omitzero"`
	PlanID           string         `json:"plan_id,omitempty"`
	ConcurrencyLimit int            `json:"concurrency_limit,omitempty"`
	RouteBindings    []RouteBinding `json:"route_bindings,omitempty"`
	Cycle            Cycle          `json:"cycle"`
}

type Cycle struct {
	// PlanID records which plan opened this window, so a completion admitted
	// under an earlier binding is recognized as belonging elsewhere.
	PlanID   string    `json:"plan_id,omitempty"`
	StartAt  time.Time `json:"start_at,omitzero"`
	EndAt    time.Time `json:"end_at,omitzero"`
	SpentUSD float64   `json:"spent_usd"`
}

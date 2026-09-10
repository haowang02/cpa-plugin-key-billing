package billing

import "time"

// State is the working set the store keeps in memory. It holds everything the
// request path has to consult without touching disk. Request and error events
// grow with traffic and are queried directly from the repository.
type State struct {
	// Prices is keyed by NormalizeModelID; each value retains its stored spelling.
	Prices            map[string]CustomPrice
	Plans             []Plan
	Keys              map[string]*KeyState
	Routes            []Route
	ConfigCredentials map[string]ConfigCredential
}

type ConfigCredential struct {
	Provider   string
	KeyPreview string
	Disabled   bool
}

func NewState() *State {
	return &State{
		Prices:            make(map[string]CustomPrice),
		Keys:              make(map[string]*KeyState),
		ConfigCredentials: make(map[string]ConfigCredential),
	}
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

// KeyState is identified by caller scope; plaintext keys are never stored.
type KeyState struct {
	BillingSince time.Time `json:"billing_since,omitzero"`
	Preview      string    `json:"preview,omitempty"`
	Label        string    `json:"label,omitempty"`
	// These two tell the three kinds of record apart:
	//
	//	InConfig set     a key CPA currently holds
	//	DeletedAt set    a key CPA held and no longer does
	//	neither set      a principal only ever seen in traffic, which may belong
	//	                 to another access provider and must therefore never be
	//	                 marked deleted by a CPA Key-list sync
	//
	// Key records are permanent: historical events resolve their scope to the
	// preview and label here, even after the key leaves CPA's configuration.
	InConfig         bool                  `json:"in_config,omitempty"`
	DeletedAt        time.Time             `json:"deleted_at,omitzero"`
	PlanID           string                `json:"plan_id,omitempty"`
	ConcurrencyLimit int                   `json:"concurrency_limit,omitempty"`
	RouteBindings    RouteBindings         `json:"route_bindings"`
	Cycles           map[string]QuotaCycle `json:"cycles"`
}

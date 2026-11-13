package billing

import "time"

// State holds everything the request path consults without disk; events are queried from the repository.
type State struct {
	// Prices is keyed by NormalizeModelID; each value retains its stored spelling.
	Prices            map[string]CustomPrice
	Plans             []Plan
	Keys              map[string]*KeyState
	Routes            []Route
	ConfigCredentials map[string]ConfigCredential
}

func NewState() *State {
	return &State{
		Prices:            make(map[string]CustomPrice),
		Keys:              make(map[string]*KeyState),
		ConfigCredentials: make(map[string]ConfigCredential),
	}
}

// LongContextPrice replaces the whole request's rates above ThresholdInputTokens; omitted cache prices use
// the tier's input price.
type LongContextPrice struct {
	ThresholdInputTokens int64    `json:"threshold_input_tokens"`
	InputPer1M           float64  `json:"input_per_1m"`
	OutputPer1M          float64  `json:"output_per_1m"`
	CacheReadPer1M       *float64 `json:"cache_read_per_1m,omitempty"`
	CacheWritePer1M      *float64 `json:"cache_write_per_1m,omitempty"`
}

// KeyState is identified by caller scope; plaintext keys are never stored.
type KeyState struct {
	Preview string `json:"preview,omitempty"`
	Label   string `json:"label,omitempty"`
	// InConfig: CPA holds the key. DeletedAt: CPA held it and no longer does. Neither: a principal seen only in
	// traffic, possibly another access provider's, which a key-list sync must never delete. Records are permanent.
	InConfig         bool          `json:"in_config,omitempty"`
	DeletedAt        time.Time     `json:"deleted_at,omitzero"`
	PlanID           string        `json:"plan_id,omitempty"`
	ConcurrencyLimit int           `json:"concurrency_limit,omitempty"`
	RouteBindings    RouteBindings `json:"route_bindings"`
	Cycles           QuotaCycles   `json:"cycles"`
}

package billing

import "time"

// RequestEvent is one persisted request record and never stores a plaintext API key.
type RequestEvent struct {
	At              time.Time `json:"at"`
	Scope           string    `json:"scope"`
	AuthIndex       string    `json:"auth_index,omitempty"`
	Provider        string    `json:"provider,omitempty"`
	ExecutorType    string    `json:"executor_type,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	ServiceTier     string    `json:"service_tier,omitempty"`
	UpstreamModel   string    `json:"upstream_model,omitempty"`
	BillingModel    string    `json:"billing_model,omitempty"`
	Failed          bool      `json:"failed"`
	LatencyMS       int64     `json:"latency_ms,omitempty"`
	TTFTMS          int64     `json:"ttft_ms,omitempty"`
	// AccountingQuality is empty when the host reported no token detail.
	AccountingQuality TokenAccountingQuality `json:"accounting_quality,omitempty"`
	// PriceSource says where the numbers came from. "none" means no rule
	// matched and the usage event was billed at zero.
	PriceSource PriceSource `json:"price_source,omitempty"`
	Cost        Cost        `json:"cost"`
	// ReasoningTokens is already included in Cost.BilledOutputTokens.
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty"`
}

const RequestEventRetention = 30 * 24 * time.Hour

// Display identity is joined rather than copied into every entry, so Key and
// credential renames update historical rows without rewriting request events.
type RequestEventRow struct {
	RequestEvent
	Preview string `json:"preview,omitempty"`
	Label   string `json:"label,omitempty"`
	Source  string `json:"source,omitempty"`
}

const (
	RequestEventStatusNormal = "normal"
	RequestEventStatusFailed = "failed"
)

// RequestEventQuery selects one filtered page of request events.
type RequestEventQuery struct {
	// Scope is an internal authorization boundary. Callers never select it from
	// a query parameter: account endpoints derive it from the presented API key.
	Scope          string
	KeyScope       string
	Model          string
	Source         string
	Executor       string
	Provider       string
	Status         string
	From           time.Time
	To             time.Time
	Timezone       *time.Location
	IncludeFilters bool
	Offset         int
	// Limit is the page size; a non-positive limit returns every match.
	Limit int
}

// RequestEventView is one page plus totals that cannot be inferred from it.
type RequestEventView struct {
	Entries  []RequestEventRow         `json:"entries"`
	Total    int                       `json:"total"`
	Statuses RequestEventStatusCounts  `json:"status_counts"`
	Filters  *RequestEventFilterValues `json:"filter_options,omitempty"`
}

type RequestEventFilterValues struct {
	APIKeys   []APIKeyFilterOption `json:"api_keys"`
	Models    []string             `json:"models"`
	Sources   []string             `json:"sources"`
	Executors []string             `json:"executors,omitempty"`
	Providers []string             `json:"providers,omitempty"`
}

type APIKeyFilterOption struct {
	Scope   string `json:"scope"`
	Preview string `json:"preview,omitempty"`
	Label   string `json:"label,omitempty"`
}

type RequestEventStatusCounts struct {
	All    int `json:"all"`
	Normal int `json:"normal"`
	Failed int `json:"failed"`
}

func (c RequestEventStatusCounts) Total(status string) int {
	switch status {
	case "":
		return c.All
	case RequestEventStatusNormal:
		return c.Normal
	case RequestEventStatusFailed:
		return c.Failed
	default:
		return 0
	}
}

func ValidRequestEventStatus(status string) bool {
	switch status {
	case "", RequestEventStatusNormal, RequestEventStatusFailed:
		return true
	default:
		return false
	}
}

func (s *Store) RequestEvents(query RequestEventQuery) (RequestEventView, error) {
	view, err := withRepository(s, func(repo Repository) (RequestEventView, error) {
		return repo.RequestEvents(query, s.Now().Add(-RequestEventRetention))
	})
	if view.Entries == nil {
		view.Entries = []RequestEventRow{}
	}
	return view, err
}

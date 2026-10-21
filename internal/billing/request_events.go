package billing

import "time"

// ExecutionMetadata contains host-reported execution identity. CredentialRef is
// a plugin fingerprint, and BaseURL omits user info, query strings and fragments.
type ExecutionMetadata struct {
	RequestID       string `json:"request_id,omitempty"`
	TraceID         string `json:"trace_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	CredentialRef   string `json:"credential_ref,omitempty"`
	BaseURL         string `json:"base_url,omitempty"`
	AccessProvider  string `json:"access_provider,omitempty"`
	IsNativeKey     *bool  `json:"is_native_key,omitempty"`
}

// RequestEvent is one persisted request record and never stores a plaintext API key.
// Account contains only an OAuth identity or masked API key.
type RequestEvent struct {
	Execution *ExecutionMetadata `json:"execution,omitempty"`
	Endpoint  string             `json:"endpoint,omitempty"`
	Transport string             `json:"transport,omitempty"`
	Stream    *bool              `json:"stream,omitempty"`
	// CredentialName is the display name of the upstream credential that served
	// the request, in the same "provider · name" form the routing rules show.
	// It is empty when the host reported no credential identity.
	CredentialName string `json:"credential_name,omitempty"`
	// UserAgent is the client User-Agent of the request, as the host reported it.
	UserAgent           string    `json:"user_agent,omitempty"`
	At                  time.Time `json:"at"`
	Scope               string    `json:"scope"`
	AuthIndex           string    `json:"auth_index,omitempty"`
	Provider            string    `json:"provider,omitempty"`
	Account             string    `json:"account,omitempty"`
	ExecutorType        string    `json:"executor_type,omitempty"`
	ReasoningEffort     string    `json:"reasoning_effort,omitempty"`
	ServiceTier         string    `json:"service_tier,omitempty"`
	ResponseServiceTier string    `json:"response_service_tier,omitempty"`
	UpstreamModel       string    `json:"upstream_model,omitempty"`
	ResponseModel       string    `json:"response_model,omitempty"`
	BillingModel        string    `json:"billing_model,omitempty"`
	Failed              bool      `json:"failed"`
	LatencyMS           int64     `json:"latency_ms,omitempty"`
	TTFTMS              int64     `json:"ttft_ms,omitempty"`
	// AccountingQuality is empty when the host reported no token detail.
	AccountingQuality TokenAccountingQuality `json:"accounting_quality,omitempty"`
	// PriceSource says where the numbers came from. "none" means no rule
	// matched and the usage event was billed at zero.
	PriceSource PriceSource `json:"price_source,omitempty"`
	Cost        Cost        `json:"cost"`
	// ReasoningTokens is already included in Cost.BilledOutputTokens.
	ReasoningTokens int64        `json:"reasoning_tokens,omitempty"`
	Quota           *QuotaReport `json:"-"`
}

const RequestEventRetention = 365 * 24 * time.Hour

func usageTimeRange(from, to, now time.Time) (time.Time, time.Time, error) {
	if to.IsZero() || to.After(now) {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-30 * 24 * time.Hour)
	}
	if cutoff := now.Add(-RequestEventRetention); from.Before(cutoff) {
		from = cutoff
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, invalidf("The time range is outside the retained request history")
	}
	return from, to, nil
}

// Source uses the event's account snapshot; key labels use their current values.
type RequestEventRow struct {
	RequestEvent
	// Encode the database identity as a string to preserve all 64 bits in browsers.
	ID        int64  `json:"id,string"`
	Preview   string `json:"preview,omitempty"`
	Label     string `json:"label,omitempty"`
	Source    string `json:"source,omitempty"`
	ErrorBody string `json:"error_body,omitempty"`
}

// RequestEventQuery selects one filtered page of request events.
type RequestEventQuery struct {
	// Scope is an internal authorization boundary. Callers never select it from
	// a query parameter: account endpoints derive it from the presented API key.
	Scope          string
	KeyScope       string
	KeyScopeEmpty  bool
	Model          string
	Source         string
	Executor       string
	Provider       string
	Failed         *bool
	From           time.Time
	To             time.Time
	Timezone       *time.Location
	IncludeFilters bool
	SnapshotID     *int64
	Offset         int
	// Limit is the page size; a non-positive limit returns every match.
	Limit int
}

// RequestEventView is one page plus totals that cannot be inferred from it.
type RequestEventView struct {
	SnapshotID int64                     `json:"snapshot_id,string"`
	Entries    []RequestEventRow         `json:"entries"`
	Total      int                       `json:"total"`
	Statuses   RequestEventStatusCounts  `json:"status_counts"`
	Filters    *RequestEventFilterValues `json:"filter_options,omitempty"`
}

type RequestEventFilterValues struct {
	Models        []string              `json:"models"`
	Sources       []string              `json:"-"`
	SourceOptions []RequestSourceOption `json:"source_options"`
	Executors     []string              `json:"executors,omitempty"`
	Providers     []string              `json:"providers,omitempty"`
}

type RequestSourceOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type RequestEventStatusCounts struct {
	All    int `json:"all"`
	Normal int `json:"normal"`
	Failed int `json:"failed"`
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

// EventKey identifies a key with at least one request or error in the time range.
// An empty Scope represents unassigned events and sorts last.
type EventKey struct {
	Scope     string    `json:"scope"`
	Preview   string    `json:"preview"`
	Label     string    `json:"label,omitempty"`
	DeletedAt time.Time `json:"deleted_at,omitzero"`
}

func (s *Store) EventKeys(from, to time.Time) ([]EventKey, error) {
	return withRepository(s, func(repo Repository) ([]EventKey, error) {
		return repo.EventKeys(from, to, s.Now().Add(-RequestEventRetention))
	})
}

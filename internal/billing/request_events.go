package billing

import (
	"fmt"
	"time"
)

// CredentialRef is a plugin fingerprint; BaseURL omits user info, query strings and fragments.
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

// UserAgent and XForwardedFor are client-reported and unverified by the host.
type RequestOrigin struct {
	Endpoint      string `json:"endpoint,omitempty"`
	Transport     string `json:"transport,omitempty"`
	UserAgent     string `json:"user_agent,omitempty"`
	XForwardedFor string `json:"x_forwarded_for,omitempty"`
}

// A RequestEvent never stores a plaintext API key.
type RequestEvent struct {
	RequestOrigin
	Execution       *ExecutionMetadata `json:"execution,omitempty"`
	Stream          *bool              `json:"stream,omitempty"`
	At              time.Time          `json:"at"`
	Scope           string             `json:"scope"`
	AuthIndex       string             `json:"auth_index,omitempty"`
	Provider        string             `json:"provider,omitempty"`
	Account         string             `json:"account,omitempty"`
	ExecutorType    string             `json:"executor_type,omitempty"`
	ReasoningEffort string             `json:"reasoning_effort,omitempty"`
	ServiceTier     string             `json:"service_tier,omitempty"`
	// EffectiveServiceTier is the final outbound tier, which payload rules may change; none before schema 27.
	EffectiveServiceTier *string `json:"effective_service_tier,omitempty"`
	ResponseServiceTier  string  `json:"response_service_tier,omitempty"`
	UpstreamModel        string  `json:"upstream_model,omitempty"`
	ResponseModel        string  `json:"response_model,omitempty"`
	BillingModel         string  `json:"billing_model,omitempty"`
	Failed               bool    `json:"failed"`
	LatencyMS            int64   `json:"latency_ms,omitempty"`
	TTFTMS               int64   `json:"ttft_ms,omitempty"`
	// AccountingQuality is empty when the host reported no token detail.
	AccountingQuality TokenAccountingQuality `json:"accounting_quality,omitempty"`
	// PriceSource "none" means no rule matched and the usage was billed at zero.
	PriceSource PriceSource `json:"price_source,omitempty"`
	Cost        Cost        `json:"cost"`
	// ReasoningTokens is already included in Cost.BilledOutputTokens.
	ReasoningTokens int64        `json:"reasoning_tokens,omitempty"`
	Quota           *QuotaReport `json:"-"`
}

// Credential uses the event's account snapshot; key labels use their current values.
type RequestEventRow struct {
	RequestEvent
	// Encode the database identity as a string to preserve all 64 bits in browsers.
	ID         int64  `json:"id,string"`
	Preview    string `json:"preview,omitempty"`
	Label      string `json:"label,omitempty"`
	Credential string `json:"credential,omitempty"`
	ErrorBody  string `json:"error_body,omitempty"`
}

type RequestEventQuery struct {
	// Scope is an authorization boundary: account endpoints derive it from the presented key, never a parameter.
	Scope          string
	KeyScope       string
	KeyScopeEmpty  bool
	Model          string
	Credential     string
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

type RequestEventView struct {
	SnapshotID int64                     `json:"snapshot_id,string"`
	Entries    []RequestEventRow         `json:"entries"`
	Total      int                       `json:"total"`
	Statuses   RequestEventStatusCounts  `json:"status_counts"`
	Filters    *RequestEventFilterValues `json:"filter_options,omitempty"`
}

type RequestEventFilterValues struct {
	KeyCounts         map[string]int           `json:"key_counts,omitempty"`
	ModelCounts       map[string]int           `json:"model_counts"`
	CredentialCounts  map[string]int           `json:"-"`
	Models            []string                 `json:"models"`
	Credentials       []string                 `json:"-"`
	CredentialOptions []CredentialFilterOption `json:"credential_options"`
	Executors         []string                 `json:"executors,omitempty"`
	Providers         []string                 `json:"providers,omitempty"`
}

type CredentialFilterOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

type RequestEventStatusCounts struct {
	All    int `json:"all"`
	Normal int `json:"normal"`
	Failed int `json:"failed"`
}

func (s *Store) RequestEvents(query RequestEventQuery) (RequestEventView, error) {
	view, err := withRepository(s, func(repo Repository) (RequestEventView, error) {
		return repo.RequestEvents(query, s.cfg.RequestEventRetentionDays.cutoff(s.Now()))
	})
	if view.Entries == nil {
		view.Entries = []RequestEventRow{}
	}
	return view, err
}

// An empty Scope stands for unassigned events and sorts last.
type EventKey struct {
	Scope     string    `json:"scope"`
	Preview   string    `json:"preview"`
	Label     string    `json:"label,omitempty"`
	DeletedAt time.Time `json:"deleted_at,omitzero"`
}

func (s *Store) EventKeys(from, to time.Time) ([]EventKey, error) {
	return withRepository(s, func(repo Repository) ([]EventKey, error) {
		return repo.EventKeys(from, to, s.cfg.RequestEventRetentionDays.cutoff(s.Now()))
	})
}

func (s *Store) ClearRequestEvents(days RetentionDays) (int, error) {
	if days < 0 || days > maxRetentionDays {
		return 0, invalidf("Cleanup age must be an integer from 0 to 106751")
	}
	before := days.cutoff(s.Now())
	cleared, err := func() (int, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.repo == nil {
			return 0, nil
		}
		// Pending usage must reach storage before deletion so retries cannot restore it.
		if err := s.flushChanges(Changes{}); err != nil {
			return 0, fmt.Errorf("Save pending usage before clearing request events: %w", err)
		}
		return s.repo.ClearRequestEvents(before)
	}()
	if err != nil {
		s.recordWriteError(err)
	} else {
		s.recordWriteSuccess()
	}
	return cleared, err
}

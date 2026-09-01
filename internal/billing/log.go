package billing

import "time"

// LogEntry is one persisted usage event and never stores a plaintext API key.
type LogEntry struct {
	At              time.Time `json:"at"`
	Scope           string    `json:"scope"`
	AuthIndex       string    `json:"auth_index,omitempty"`
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

const LogRetention = 30 * 24 * time.Hour

// Display identity is joined rather than copied into every entry, so Key and
// credential renames update historical rows without rewriting the log.
type LogRow struct {
	LogEntry
	Preview  string `json:"preview,omitempty"`
	Label    string `json:"label,omitempty"`
	Source   string `json:"source,omitempty"`
	Provider string `json:"-"`
}

const (
	UsageStatusNormal = "normal"
	UsageStatusFailed = "failed"
)

// LogQuery selects one filtered page of the usage log.
type LogQuery struct {
	// Scope is an internal authorization boundary. Callers never select it from
	// a query parameter: account endpoints derive it from the presented API key.
	Scope          string
	KeyScope       string
	Model          string
	Source         string
	Status         string
	From           time.Time
	To             time.Time
	IncludeFilters bool
	Offset         int
	// Limit is the page size; a non-positive limit returns every match.
	Limit int
}

// LogView is one page plus totals that cannot be inferred from that page.
type LogView struct {
	Entries  []LogRow         `json:"entries"`
	Total    int              `json:"total"`
	Statuses LogStatusCounts  `json:"status_counts"`
	Filters  *LogFilterValues `json:"filter_options,omitempty"`
}

type LogFilterValues struct {
	APIKeys []LogAPIKeyOption `json:"api_keys"`
	Models  []string          `json:"models"`
	Sources []string          `json:"sources"`
}

type LogAPIKeyOption struct {
	Scope   string `json:"scope"`
	Preview string `json:"preview,omitempty"`
	Label   string `json:"label,omitempty"`
}

type LogStatusCounts struct {
	All    int `json:"all"`
	Normal int `json:"normal"`
	Failed int `json:"failed"`
}

func (c LogStatusCounts) Total(status string) int {
	switch status {
	case "":
		return c.All
	case UsageStatusNormal:
		return c.Normal
	case UsageStatusFailed:
		return c.Failed
	default:
		return 0
	}
}

func ValidLogStatus(status string) bool {
	switch status {
	case "", UsageStatusNormal, UsageStatusFailed:
		return true
	default:
		return false
	}
}

func (s *Store) Logs(query LogQuery) (LogView, error) {
	view, errLogs := withRepository(s, func(repo Repository) (LogView, error) {
		return repo.Logs(query, s.Now().Add(-LogRetention))
	})
	if view.Entries == nil {
		view.Entries = []LogRow{}
	}
	return view, errLogs
}

func (s *Store) ClearLogs() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.repo == nil {
		return 0, nil
	}
	cleared, errClear := s.repo.ClearLogs()
	if errClear == nil {
		s.dirty.Log = nil
		s.dirty.LogCutoff = time.Time{}
	}
	return cleared, errClear
}

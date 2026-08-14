package billing

import "time"

// LogEntry never stores the plaintext API key.
type LogEntry struct {
	At            time.Time `json:"at"`
	Scope         string    `json:"scope"`
	RequestID     string    `json:"request_id,omitempty"`
	Endpoint      string    `json:"endpoint,omitempty"`
	AuthIndex     string    `json:"auth_index,omitempty"`
	UpstreamModel string    `json:"upstream_model,omitempty"`
	BillingModel  string    `json:"billing_model,omitempty"`
	// Outcome is empty for a request that completed normally.
	Outcome RequestOutcome `json:"outcome,omitempty"`
	// AccountingQuality is empty when no upstream usage was ever observed, which
	// is the usual shape of a canceled request.
	AccountingQuality TokenAccountingQuality `json:"accounting_quality,omitempty"`
	// PriceSource says where the numbers came from. "none" is the one to look
	// for: it means no rule matched and the request was billed at zero.
	PriceSource PriceSource `json:"price_source,omitempty"`
	Cost        Cost        `json:"cost"`
	// ReasoningTokens is already included in Cost.BilledOutputTokens.
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty"`
}

const LogRetention = 30 * 24 * time.Hour

// Display identity is looked up rather than copied into every entry, so Key
// synchronization, remark changes and newly learned credentials update
// historical rows too. Deleting a key in CPA therefore does not take its history
// with it: the record outlives the key precisely so these rows keep naming it.
type LogRow struct {
	LogEntry
	Preview string `json:"preview,omitempty"`
	Label   string `json:"label,omitempty"`
	Source  string `json:"source,omitempty"`
}

// OutcomeSucceeded is not stored on an entry — a request that completed
// normally carries no outcome at all — but a filter still needs a name for it.
const OutcomeSucceeded = "succeeded"

// LogQuery selects one page of the log. Searching and filtering happen in the
// database rather than in the browser so that a window holding weeks of traffic
// never has to travel over the wire in full.
type LogQuery struct {
	// Search matches any one of the identity fields, case-insensitively.
	Search string
	// Outcome is empty for every status, OutcomeSucceeded for the entries that
	// carry none, or a stored RequestOutcome.
	Outcome string
	Offset  int
	// Limit is the page size; a non-positive limit returns every match.
	Limit int
}

// LogView is one page plus the two things a client holding a single page cannot
// work out for itself.
type LogView struct {
	Entries []LogRow `json:"entries"`
	// Total counts every entry matching the query, of which Entries is one page.
	Total int `json:"total"`
	// Outcomes counts what each status filter would return for the same search,
	// so picking one does not collapse the other counts to zero.
	Outcomes LogOutcomeCounts `json:"outcome_counts"`
}

type LogOutcomeCounts struct {
	All       int `json:"all"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Canceled  int `json:"canceled"`
}

// Total is the count for one status filter, which is a projection of the counts
// the same search already produced rather than a second query.
func (c LogOutcomeCounts) Total(outcome string) int {
	switch outcome {
	case OutcomeSucceeded:
		return c.Succeeded
	case string(OutcomeFailed):
		return c.Failed
	case string(OutcomeCanceled):
		return c.Canceled
	default:
		return c.All
	}
}

// An unknown filter is rejected by the caller rather than matching nothing, so
// a typo in a query string does not read as "there is no traffic".
func ValidLogOutcome(filter string) bool {
	switch filter {
	case "", OutcomeSucceeded, string(OutcomeFailed), string(OutcomeCanceled):
		return true
	}
	return false
}

// Entries that fell out of the retention window are left out rather than
// returned: they are dropped on the next append, and until then they are no
// longer part of the log.
func (s *Store) Logs(query LogQuery) (LogView, error) {
	repo := s.repository()
	if repo == nil {
		return LogView{Entries: []LogRow{}}, nil
	}
	return repo.Logs(query, s.Now().Add(-LogRetention))
}

func (s *Store) ClearLogs() (int, error) {
	repo := s.repository()
	if repo == nil {
		return 0, nil
	}
	return repo.ClearLogs()
}

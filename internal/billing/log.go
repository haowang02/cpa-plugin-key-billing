package billing

import (
	"strings"
	"time"
)

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

// Entries stay oldest-first to keep the persisted representation append-only.
// Appending is also when the window is trimmed: the log only ever grows here,
// so this is the one place stale entries can be dropped from the document.
func appendLog(state *State, entry LogEntry, now time.Time) {
	cutoff := now.Add(-LogRetention)
	kept := state.Log[:0]
	for _, existing := range state.Log {
		if !existing.At.Before(cutoff) {
			kept = append(kept, existing)
		}
	}
	state.Log = append(kept, entry)
}

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

// LogQuery selects one page of the log. Searching and filtering happen here
// rather than in the browser so that a window holding weeks of traffic never
// has to travel over the wire in full.
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

func (c *LogOutcomeCounts) add(outcome RequestOutcome) {
	c.All++
	switch outcome {
	case OutcomeFailed:
		c.Failed++
	case OutcomeCanceled:
		c.Canceled++
	case "":
		c.Succeeded++
	}
}

// Any single field the table can show, including the ones resolved at read time.
func (r LogRow) matches(search string) bool {
	if search == "" {
		return true
	}
	for _, field := range []string{
		r.Label, r.Preview, r.Scope, r.UpstreamModel, r.BillingModel,
		r.Endpoint, r.Source, r.RequestID,
	} {
		if strings.Contains(strings.ToLower(field), search) {
			return true
		}
	}
	return false
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

func matchesOutcome(outcome RequestOutcome, filter string) bool {
	switch filter {
	case "":
		return true
	case OutcomeSucceeded:
		return outcome == ""
	default:
		return string(outcome) == filter
	}
}

// Entries that fell out of the retention window are skipped rather than
// returned: they are dropped from the document on the next append, and until
// then they are no longer part of the log.
//
// Results are newest first, so an offset counts back from the newest entry.
func (s *Store) Logs(query LogQuery) LogView {
	view := LogView{Entries: []LogRow{}}
	search := strings.ToLower(strings.TrimSpace(query.Search))
	cutoff := s.Now().Add(-LogRetention)
	s.Read(func(state *State) {
		for i := len(state.Log) - 1; i >= 0; i-- {
			entry := state.Log[i]
			if entry.At.Before(cutoff) {
				continue
			}
			// The display identity is searchable, so it is resolved before the
			// row is known to be wanted.
			row := LogRow{LogEntry: entry}
			if key := state.Keys[entry.Scope]; key != nil {
				row.Preview = key.Preview
				row.Label = key.Label
			}
			row.Source = state.Credentials[entry.AuthIndex].Name()
			if !row.matches(search) {
				continue
			}
			view.Outcomes.add(entry.Outcome)
			if !matchesOutcome(entry.Outcome, query.Outcome) {
				continue
			}
			view.Total++
			if view.Total <= query.Offset {
				continue
			}
			if query.Limit > 0 && len(view.Entries) >= query.Limit {
				continue
			}
			view.Entries = append(view.Entries, row)
		}
	})
	return view
}

func (s *Store) ClearLogs() int {
	return updateResult(s, func(state *State) (int, bool) {
		count := len(state.Log)
		if count == 0 {
			return 0, false
		}
		state.Log = nil
		return count, true
	})
}

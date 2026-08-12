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

type LogView struct {
	Entries []LogRow `json:"entries"`
	Total   int      `json:"total"`
}

// Entries that fell out of the retention window are skipped rather than
// returned: they are dropped from the document on the next append, and until
// then they are no longer part of the log.
//
// Results are newest first; a non-positive limit returns everything retained.
func (s *Store) Logs(limit int) LogView {
	view := LogView{Entries: []LogRow{}}
	cutoff := s.Now().Add(-LogRetention)
	s.Read(func(state *State) {
		for i := len(state.Log) - 1; i >= 0; i-- {
			entry := state.Log[i]
			if entry.At.Before(cutoff) {
				continue
			}
			view.Total++
			if limit > 0 && len(view.Entries) >= limit {
				continue
			}
			row := LogRow{LogEntry: entry}
			if key := state.Keys[entry.Scope]; key != nil {
				row.Preview = key.Preview
				row.Label = key.Label
			}
			row.Source = state.Credentials[entry.AuthIndex].Name()
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

package billing

import "time"

// LogEntry stores the canonical inputs and result of one bill. It never stores
// the plaintext API key.
type LogEntry struct {
	At             time.Time `json:"at"`
	Scope          string    `json:"scope"`
	Preview        string    `json:"preview,omitempty"`
	RequestID      string    `json:"request_id,omitempty"`
	UsageIndex     int       `json:"usage_index,omitempty"`
	ClientProtocol string    `json:"client_protocol,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	ExecutorType   string    `json:"executor_type,omitempty"`
	// Model is what was billed; Alias is what the client asked for.
	Model             string                 `json:"model,omitempty"`
	Alias             string                 `json:"alias,omitempty"`
	Failed            bool                   `json:"failed,omitempty"`
	AccountingQuality TokenAccountingQuality `json:"accounting_quality,omitempty"`
	// PriceSource says where the numbers came from. "none" is the one to look
	// for: it means no rule matched and the request was billed at zero.
	PriceSource PriceSource `json:"price_source,omitempty"`
	Cost        Cost        `json:"cost"`
	// ReasoningTokens is already included in Cost.BilledOutputTokens.
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty"`
	// PlanID, CycleSpentUSD and CycleLimitUSD place the request in its
	// subscription window as it stood immediately after being billed. They are
	// empty for an unbound key, which has no window to place it in.
	PlanID        string  `json:"plan_id,omitempty"`
	CycleSpentUSD float64 `json:"cycle_spent_usd,omitempty"`
	CycleLimitUSD float64 `json:"cycle_limit_usd,omitempty"`
}

// Entries stay oldest-first to keep the persisted representation append-only.
func appendLog(state *State, entry LogEntry, limit int) {
	if limit <= 0 {
		state.Log = nil
		return
	}
	if len(state.Log) >= limit {
		drop := len(state.Log) - limit + 1
		state.Log = append(state.Log[:0], state.Log[drop:]...)
	}
	state.Log = append(state.Log, entry)
}

// pruneLogOrphans drops entries whose key is no longer tracked.
//
// Forgetting a key is described to the operator as deleting everything held
// about it, and the log is the most detailed thing held about it. Leaving the
// entries behind would also leave rows the UI can put no name to, since the
// label lives on the key.
func pruneLogOrphans(state *State) {
	if len(state.Log) == 0 {
		return
	}
	kept := state.Log[:0]
	for _, entry := range state.Log {
		if _, exists := state.Keys[entry.Scope]; exists {
			kept = append(kept, entry)
		}
	}
	if len(kept) == 0 {
		state.Log = nil
		return
	}
	state.Log = kept
}

// LogRow is one log entry as the admin UI reads it, with the key's current
// display name resolved.
//
// Label is looked up rather than stored so renaming a key relabels its history
// too. A row whose key has since been forgotten keeps only what the entry itself
// carries, which is why Preview is recorded at billing time.
type LogRow struct {
	LogEntry
	Label string `json:"label,omitempty"`
}

type LogView struct {
	Entries  []LogRow `json:"entries"`
	Retained int      `json:"retained"`
	Limit    int      `json:"limit"`
}

// Logs returns the retained billing log, newest first.
//
// limit trims the response for a caller that wants only the head of it; zero or
// negative means everything retained.
func (s *Store) Logs(limit int) LogView {
	view := LogView{Entries: []LogRow{}}
	s.Read(func(state *State) {
		view.Retained = len(state.Log)
		view.Limit = s.cfg.LogEntries
		count := len(state.Log)
		if limit > 0 && limit < count {
			count = limit
		}
		for i := 0; i < count; i++ {
			entry := state.Log[len(state.Log)-1-i]
			row := LogRow{LogEntry: entry}
			if key := state.Keys[entry.Scope]; key != nil {
				row.Label = key.Label
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

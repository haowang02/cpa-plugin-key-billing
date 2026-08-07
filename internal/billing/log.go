package billing

import "time"

// LogEntry is one billed request, kept so an operator can answer "what did this
// key actually spend money on" without correlating against the proxy's own logs.
//
// It records what the bill was computed from, not what the provider reported:
// the token counts are the billed buckets after the payload's layout has been
// normalized, which is what the amounts below were derived from. A raw provider
// counter would be the more faithful record and the less useful one, because
// reading it still requires knowing which layout it arrived in.
//
// No plaintext key is stored. Scope is the hashed caller scope and Preview is
// the masked rendering the rest of the plugin already displays.
type LogEntry struct {
	At      time.Time `json:"at"`
	Scope   string    `json:"scope"`
	Preview string    `json:"preview,omitempty"`
	// Model is what was billed; Alias is what the client asked for, recorded
	// only when the two differ.
	Model  string `json:"model,omitempty"`
	Alias  string `json:"alias,omitempty"`
	Failed bool   `json:"failed,omitempty"`
	// BillingType is the provider-style counter layout detected for this bill.
	BillingType BillingType `json:"billing_type,omitempty"`
	// PriceSource says where the numbers came from. "none" is the one to look
	// for: it means no rule matched and the request was billed at zero.
	PriceSource PriceSource `json:"price_source,omitempty"`
	// Cost is the priced breakdown, carrying both the billed token buckets and
	// what each of them cost.
	Cost Cost `json:"cost"`
	// ReasoningTokens is reported for the record. It is not a bucket of its own
	// in Cost, because every layout bills reasoning at the output rate and it is
	// already inside Cost.BilledOutputTokens.
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty"`
	// PlanID, CycleSpentUSD and CycleLimitUSD place the request in its
	// subscription window as it stood immediately after being billed. They are
	// empty for an unbound key, which has no window to place it in.
	PlanID        string  `json:"plan_id,omitempty"`
	CycleSpentUSD float64 `json:"cycle_spent_usd,omitempty"`
	CycleLimitUSD float64 `json:"cycle_limit_usd,omitempty"`
}

// appendLog records one entry, discarding the oldest once the log is full.
//
// Entries are stored oldest first, so appending is the common path and only an
// overflow costs a shift. That shift is O(limit) and runs at most once per
// billed request; at the retention this plugin allows it is a memmove of a few
// hundred kilobytes, on the terminal lifecycle event rather than on a client's
// critical path. A ring buffer would avoid it and would have to carry its head
// index through the JSON document, which is a worse trade for a table this size.
//
// A limit of zero clears the log rather than merely skipping the append: turning
// retention off is a request to stop keeping this, not to freeze whatever was
// already kept.
func appendLog(state *State, entry LogEntry, limit int) {
	if state == nil {
		return
	}
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
	if state == nil || len(state.Log) == 0 {
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

// LogView is the payload of the /logs route.
type LogView struct {
	// Entries are newest first, which is the order they are read in.
	Entries []LogRow `json:"entries"`
	// Retained is how many entries the log currently holds, and Limit is how
	// many it is configured to keep. Reporting both is what makes a log that
	// stops growing legible: it has either filled up or been turned off.
	Retained int `json:"retained"`
	Limit    int `json:"limit"`
}

// Logs returns the retained billing log, newest first.
//
// limit trims the response for a caller that wants only the head of it; zero or
// negative means everything retained.
func (s *Store) Logs(limit int) LogView {
	view := LogView{Entries: []LogRow{}}
	if s == nil {
		return view
	}
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

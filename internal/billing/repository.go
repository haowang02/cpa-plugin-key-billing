package billing

import "time"

// Repository is the persistence boundary of this package. Nothing above it
// knows that a database exists, and nothing below it decides what billing
// means; the two meet on the domain types alone.
type Repository interface {
	// Load reads the working set and drops the entries of both logs older than
	// cutoff, which is the other moment retention is enforced: a deployment
	// that has stopped receiving traffic never appends. It must answer a
	// non-nil State whenever it answers no error.
	Load(cutoff time.Time) (Snapshot, error)

	// Save applies one mutation in a single transaction: it writes the rows
	// named by changes, reading their current values out of state, and appends
	// the log entries the mutation produced.
	Save(state *State, changes Changes) error

	// Logs answers one page of the billing log along with the totals a page
	// cannot carry. since is the retention cutoff.
	Logs(query LogQuery, since time.Time) (LogView, error)
	ClearLogs() (int, error)
	// LoggedScopes reports which keys the log still names. A deleted key's
	// record is kept for exactly as long as this says something reads it.
	LoggedScopes(since time.Time) (map[string]struct{}, error)

	// AppendEvent adds one plugin log line and drops the lines past cutoff.
	// Appending is the only moment that log grows, so it is also the only
	// moment retention applies to it.
	AppendEvent(event Event, cutoff time.Time) error
	// Events answers the plugin log newest first. since is the retention
	// cutoff.
	Events(since time.Time) ([]Event, error)
	ClearEvents() (int, error)

	Close() error
}

// Snapshot is the working set the store keeps in memory, plus the size of the
// log it deliberately leaves behind.
type Snapshot struct {
	State      *State
	LogEntries int
}

// Changes names the rows one mutation touched, so a save writes those and no
// others. Plans, prices, model groups and credentials are replaced whole because
// an operator changes them a few rows at a time and there are never many; keys
// are named individually because usage accounting runs on every proxied request
// and must touch a single row.
type Changes struct {
	// Keys lists the scopes to write. AllKeys instead replaces the entire key
	// set, dropping the records the state no longer holds.
	Keys    []string
	AllKeys bool

	Plans       bool
	Prices      bool
	ModelGroups bool
	Credentials bool

	// Appending is the only moment the log grows, so it is also the only moment
	// entries past LogCutoff need removing.
	Log       []LogEntry
	LogCutoff time.Time
}

const maxPendingLogEntries = 1000

// A mutation that only prunes the log is still a mutation: LogCutoff counts
// here so that a save carrying nothing but a cutoff reaches the repository.
func (c Changes) empty() bool {
	return len(c.Keys) == 0 && !c.AllKeys && !c.Plans && !c.Prices && !c.ModelGroups && !c.Credentials &&
		len(c.Log) == 0 && c.LogCutoff.IsZero()
}

func (c Changes) merge(next Changes) Changes {
	if c.empty() {
		return next.withBoundedLog()
	}
	if next.empty() {
		return c.withBoundedLog()
	}
	merged := Changes{
		AllKeys:     c.AllKeys || next.AllKeys,
		Plans:       c.Plans || next.Plans,
		Prices:      c.Prices || next.Prices,
		ModelGroups: c.ModelGroups || next.ModelGroups,
		Credentials: c.Credentials || next.Credentials,
		Log:         append(append([]LogEntry(nil), c.Log...), next.Log...),
		LogCutoff:   next.LogCutoff,
	}
	if merged.LogCutoff.Before(c.LogCutoff) {
		merged.LogCutoff = c.LogCutoff
	}
	if merged.AllKeys {
		return merged.withBoundedLog()
	}
	seen := make(map[string]struct{}, len(c.Keys)+len(next.Keys))
	for _, scope := range append(append([]string(nil), c.Keys...), next.Keys...) {
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		merged.Keys = append(merged.Keys, scope)
	}
	return merged.withBoundedLog()
}

func (c Changes) withBoundedLog() Changes {
	if len(c.Log) > maxPendingLogEntries {
		c.Log = append([]LogEntry(nil), c.Log[len(c.Log)-maxPendingLogEntries:]...)
	}
	return c
}

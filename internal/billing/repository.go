package billing

import "time"

type Repository interface {
	Load(requestEventCutoff, pluginLogCutoff time.Time) (Snapshot, error)
	Save(state *State, changes Changes) error

	RequestEvents(query RequestEventQuery, since time.Time) (RequestEventView, error)
	RequestErrors(query RequestErrorQuery, since time.Time) (RequestErrorView, error)
	Analysis(query RequestEventQuery, since time.Time) (AnalysisView, error)
	RequestEventScopes(since time.Time) (map[string]struct{}, error)

	AppendPluginLog(entry PluginLog, cutoff time.Time) error
	PluginLogsPage(query PluginLogQuery) (PluginLogPage, error)
	ClearPluginLogs() (int, error)

	Close() error
}

type Snapshot struct {
	State             *State
	RequestEventCount int
}

// Changes names the rows one mutation touched, so a save writes those and no
// others. Plans, prices, routes, and credentials are replaced whole because
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
	Routes      bool
	Credentials bool

	NormalRequestEvents []RequestEvent
	RequestErrorEvents  []RequestErrorEvent
	RequestEventCutoff  time.Time
}

const maxPendingRequestRecords = 1000

func (c Changes) empty() bool {
	return len(c.Keys) == 0 && !c.AllKeys && !c.Plans && !c.Prices && !c.Routes && !c.Credentials &&
		len(c.NormalRequestEvents) == 0 && len(c.RequestErrorEvents) == 0 && c.RequestEventCutoff.IsZero()
}

func (c Changes) merge(next Changes) Changes {
	if c.empty() {
		return next.withBoundedRequestRecords()
	}
	if next.empty() {
		return c.withBoundedRequestRecords()
	}
	merged := Changes{
		AllKeys:             c.AllKeys || next.AllKeys,
		Plans:               c.Plans || next.Plans,
		Prices:              c.Prices || next.Prices,
		Routes:              c.Routes || next.Routes,
		Credentials:         c.Credentials || next.Credentials,
		NormalRequestEvents: append(append([]RequestEvent(nil), c.NormalRequestEvents...), next.NormalRequestEvents...),
		RequestErrorEvents:  append(append([]RequestErrorEvent(nil), c.RequestErrorEvents...), next.RequestErrorEvents...),
		RequestEventCutoff:  next.RequestEventCutoff,
	}
	if merged.RequestEventCutoff.Before(c.RequestEventCutoff) {
		merged.RequestEventCutoff = c.RequestEventCutoff
	}
	if merged.AllKeys {
		return merged.withBoundedRequestRecords()
	}
	seen := make(map[string]struct{}, len(c.Keys)+len(next.Keys))
	for _, scope := range append(append([]string(nil), c.Keys...), next.Keys...) {
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		merged.Keys = append(merged.Keys, scope)
	}
	return merged.withBoundedRequestRecords()
}

func (c Changes) withBoundedRequestRecords() Changes {
	if len(c.NormalRequestEvents) > maxPendingRequestRecords {
		c.NormalRequestEvents = append([]RequestEvent(nil), c.NormalRequestEvents[len(c.NormalRequestEvents)-maxPendingRequestRecords:]...)
	}
	if len(c.RequestErrorEvents) > maxPendingRequestRecords {
		c.RequestErrorEvents = append([]RequestErrorEvent(nil), c.RequestErrorEvents[len(c.RequestErrorEvents)-maxPendingRequestRecords:]...)
	}
	return c
}

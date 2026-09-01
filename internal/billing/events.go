package billing

import (
	"fmt"
	"time"
)

type EventLevel string

const (
	EventInfo  EventLevel = "info"
	EventError EventLevel = "error"
)

// Event is one line of the plugin log. It is kept for LogRetention and bounded
// by nothing else: the log records occasional operational events — a reload, a
// failing disk — and dropping the oldest to make room for the newest would hide
// exactly the onset an operator is looking for.
type Event struct {
	At             time.Time       `json:"at"`
	Level          EventLevel      `json:"level"`
	Message        string          `json:"message"`
	RequestFailure *RequestFailure `json:"request_failure,omitempty"`
}

// RequestFailure contains only fields delivered by usage.handle, plus the
// plugin's already-masked display names for the downstream key and upstream
// credential. Body is a normalized error envelope; it never contains the
// unfiltered upstream response object.
type RequestFailure struct {
	APIKey     string `json:"api_key,omitempty"`
	Model      string `json:"model,omitempty"`
	Upstream   string `json:"upstream,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	ErrorType  string `json:"error_type,omitempty"`
	Body       string `json:"body,omitempty"`
}

// Event tolerates a nil store because the panic handler reports through it; a
// diagnostics sink that can itself fail is worse than no diagnostics. For the
// same reason a database that refuses the line drops it rather than reporting
// the refusal, which would be another write to the same database.
func (s *Store) Event(level EventLevel, format string, args ...any) {
	s.appendEvent(Event{Level: level, Message: fmt.Sprintf(format, args...)})
}

func (s *Store) appendEvent(event Event) {
	if s == nil {
		return
	}
	event.At = s.Now()
	_, _ = withRepository(s, func(repo Repository) (struct{}, error) {
		return struct{}{}, repo.AppendEvent(event, event.At.Add(-LogRetention))
	})
}

func (s *Store) Events() ([]Event, error) {
	events, errEvents := withRepository(s, func(repo Repository) ([]Event, error) {
		return repo.Events(s.Now().Add(-LogRetention))
	})
	if events == nil {
		events = []Event{}
	}
	return events, errEvents
}

func (s *Store) ClearEvents() (int, error) {
	return withRepository(s, func(repo Repository) (int, error) {
		return repo.ClearEvents()
	})
}

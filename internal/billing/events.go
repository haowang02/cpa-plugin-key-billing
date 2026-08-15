package billing

import (
	"fmt"
	"sync"
	"time"
)

// EventRetention is how long a plugin log line is kept. The log is bounded by
// age rather than by count because it records occasional operational events —
// a reload, a failing disk — and dropping the oldest of those to make room for
// the newest would hide exactly the onset an operator is looking for.
const EventRetention = 30 * 24 * time.Hour

// MaxRequestEvents bounds the entries downstream traffic can put in the log.
// Operational events stay bounded by age alone for the reason above, but a
// request that did not succeed is not an occasional event: an upstream that is
// failing produces one per attempt, and the newest are the ones being
// diagnosed. So these age out like the rest and, past this many, the oldest of
// them make room — never an operational entry.
const MaxRequestEvents = 500

type EventLevel string

const (
	EventInfo  EventLevel = "info"
	EventError EventLevel = "error"
)

// Events are held in memory only. They describe this process rather than the
// billing record, and a restart is precisely when they stop being relevant —
// persisting them would also mean writing the state file from the very paths
// that report a state file that cannot be written.
type Event struct {
	At      time.Time  `json:"at"`
	Level   EventLevel `json:"level"`
	Message string     `json:"message"`
	// perRequest marks an entry one downstream request produced. It decides
	// what the count bound applies to and is of no interest to a reader, so it
	// stays out of the panel's copy.
	perRequest bool
}

type eventLog struct {
	mu      sync.Mutex
	entries []Event
}

func (l *eventLog) add(event Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(event.At)
	l.entries = append(l.entries, event)
	if event.perRequest {
		l.capRequestsLocked()
	}
}

// capRequestsLocked drops the oldest request entries until they are back within
// their bound, leaving every operational entry where it is.
func (l *eventLog) capRequestsLocked() {
	surplus := -MaxRequestEvents
	for _, entry := range l.entries {
		if entry.perRequest {
			surplus++
		}
	}
	if surplus <= 0 {
		return
	}
	kept := l.entries[:0]
	for _, entry := range l.entries {
		if entry.perRequest && surplus > 0 {
			surplus--
			continue
		}
		kept = append(kept, entry)
	}
	l.entries = kept
}

// snapshot returns the log newest first, which is the order it is read in. It
// prunes as it reads so an idle plugin still ages its log out.
func (l *eventLog) snapshot(now time.Time) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	events := make([]Event, 0, len(l.entries))
	for i := len(l.entries) - 1; i >= 0; i-- {
		events = append(events, l.entries[i])
	}
	return events
}

func (l *eventLog) clear() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := len(l.entries)
	l.entries = nil
	return count
}

func (l *eventLog) pruneLocked(now time.Time) {
	cutoff := now.Add(-EventRetention)
	keep := 0
	for keep < len(l.entries) && l.entries[keep].At.Before(cutoff) {
		keep++
	}
	if keep > 0 {
		l.entries = append(l.entries[:0], l.entries[keep:]...)
	}
}

// Event tolerates a nil store because the panic handler reports through it; a
// diagnostics sink that can itself fail is worse than no diagnostics.
func (s *Store) Event(level EventLevel, format string, args ...any) {
	if s == nil {
		return
	}
	s.events.add(Event{At: s.Now(), Level: level, Message: fmt.Sprintf(format, args...)})
}

// requestEvent records what became of one downstream request. These arrive with
// the traffic rather than with an operator's actions, which is what bounds them
// by count as well as by age.
func (s *Store) requestEvent(level EventLevel, format string, args ...any) {
	if s == nil {
		return
	}
	s.events.add(Event{
		At: s.Now(), Level: level, Message: fmt.Sprintf(format, args...), perRequest: true,
	})
}

func (s *Store) Events() []Event {
	return s.events.snapshot(s.Now())
}

func (s *Store) ClearEvents() int {
	return s.events.clear()
}

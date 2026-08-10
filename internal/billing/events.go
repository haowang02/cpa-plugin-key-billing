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

func (s *Store) Events() []Event {
	return s.events.snapshot(s.Now())
}

func (s *Store) ClearEvents() int {
	return s.events.clear()
}

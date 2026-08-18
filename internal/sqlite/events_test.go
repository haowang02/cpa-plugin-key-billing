package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func mustAppendEvent(t *testing.T, database *DB, at time.Time, level billing.EventLevel, message string, cutoff time.Time) {
	t.Helper()
	event := billing.Event{At: at, Level: level, Message: message}
	if errAppend := database.AppendEvent(event, cutoff); errAppend != nil {
		t.Fatalf("AppendEvent error = %v", errAppend)
	}
}

// A zero cutoff reads the log whole, which is what a test asking what was
// stored wants.
func mustEvents(t *testing.T, database *DB, since time.Time) []billing.Event {
	t.Helper()
	events, errEvents := database.Events(since)
	if errEvents != nil {
		t.Fatalf("Events error = %v", errEvents)
	}
	return events
}

// The plugin log outlives the process, which is the whole of why it is stored:
// what an operator needs after a restart is what was written before it.
func TestEventsSurviveAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)
	mustAppendEvent(t, database, logStart, billing.EventError, "保存计费数据失败", time.Time{})
	mustAppendEvent(t, database, logStart.Add(time.Minute), billing.EventInfo, "已加载计费数据库", time.Time{})
	if errClose := database.Close(); errClose != nil {
		t.Fatalf("Close error = %v", errClose)
	}

	events := mustEvents(t, openDatabase(t, path), time.Time{})
	if len(events) != 2 {
		t.Fatalf("events = %+v, want both lines read back", events)
	}
	if events[0].Message != "已加载计费数据库" || events[0].Level != billing.EventInfo {
		t.Fatalf("newest = %+v, want the last line written, with its level", events[0])
	}
	if events[1].Level != billing.EventError || !events[1].At.Equal(logStart) {
		t.Fatalf("oldest = %+v, want its level and instant kept", events[1])
	}
}

// Appending is the moment the log grows, and opening it is the moment a log
// that stopped growing is looked at again.
func TestEventsAreDroppedPastRetention(t *testing.T) {
	database := openTestDB(t)
	cutoff := logStart.Add(-billing.LogRetention)
	mustAppendEvent(t, database, cutoff.Add(-time.Hour), billing.EventInfo, "过期", time.Time{})
	mustAppendEvent(t, database, logStart, billing.EventInfo, "保留", cutoff)

	events := mustEvents(t, database, time.Time{})
	if len(events) != 1 || events[0].Message != "保留" {
		t.Fatalf("events = %+v, want the expired line dropped on append", events)
	}

	if _, errLoad := database.Load(logStart.Add(time.Hour)); errLoad != nil {
		t.Fatalf("Load error = %v", errLoad)
	}
	if events := mustEvents(t, database, time.Time{}); len(events) != 0 {
		t.Fatalf("events = %+v, want the window emptied by opening it", events)
	}
}

func TestClearEvents(t *testing.T) {
	database := openTestDB(t)
	mustAppendEvent(t, database, logStart, billing.EventInfo, "事件", time.Time{})

	cleared, errClear := database.ClearEvents()
	if cleared != 1 || errClear != nil {
		t.Fatalf("ClearEvents = %d, %v; want 1", cleared, errClear)
	}
	if events := mustEvents(t, database, time.Time{}); len(events) != 0 {
		t.Fatalf("events = %+v, want the log emptied", events)
	}
}

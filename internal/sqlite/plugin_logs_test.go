package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func mustAppendPluginLog(t *testing.T, database *DB, at time.Time, level billing.PluginLogLevel, message string, cutoff time.Time) {
	t.Helper()
	entry := billing.PluginLog{At: at, Level: level, Message: message}
	if errAppend := database.AppendPluginLog(entry, cutoff); errAppend != nil {
		t.Fatalf("AppendPluginLog error = %v", errAppend)
	}
}

func mustPluginLogs(t *testing.T, database *DB, since time.Time) []billing.PluginLog {
	t.Helper()
	page, errEvents := database.PluginLogsPage(billing.PluginLogQuery{Since: since, Limit: 500})
	if errEvents != nil {
		t.Fatalf("PluginLogsPage error = %v", errEvents)
	}
	return page.Entries
}

// The plugin log outlives the process, which is the whole of why it is stored:
// what an operator needs after a restart is what was written before it.
func TestPluginLogsSurviveAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)
	mustAppendPluginLog(t, database, eventStart, billing.PluginLogError, "保存计费数据失败", time.Time{})
	mustAppendPluginLog(t, database, eventStart.Add(time.Minute), billing.PluginLogInfo, "已加载计费数据库", time.Time{})
	if errClose := database.Close(); errClose != nil {
		t.Fatalf("Close error = %v", errClose)
	}

	events := mustPluginLogs(t, openDatabase(t, path), time.Time{})
	if len(events) != 2 {
		t.Fatalf("events = %+v, want both lines read back", events)
	}
	if events[0].Message != "已加载计费数据库" || events[0].Level != billing.PluginLogInfo {
		t.Fatalf("newest = %+v, want the last line written, with its level", events[0])
	}
	if events[1].Level != billing.PluginLogError || !events[1].At.Equal(eventStart) {
		t.Fatalf("oldest = %+v, want its level and instant kept", events[1])
	}
}

// Appending is the moment the log grows, and opening it is the moment a log
// that stopped growing is looked at again.
func TestPluginLogsAreDroppedPastRetention(t *testing.T) {
	database := openTestDB(t)
	cutoff := eventStart.Add(-billing.PluginLogRetention)
	mustAppendPluginLog(t, database, cutoff.Add(-time.Hour), billing.PluginLogInfo, "过期", time.Time{})
	mustAppendPluginLog(t, database, eventStart, billing.PluginLogInfo, "保留", cutoff)

	events := mustPluginLogs(t, database, time.Time{})
	if len(events) != 1 || events[0].Message != "保留" {
		t.Fatalf("events = %+v, want the expired line dropped on append", events)
	}

	if _, errLoad := database.Load(time.Time{}, eventStart.Add(time.Hour)); errLoad != nil {
		t.Fatalf("Load error = %v", errLoad)
	}
	if events := mustPluginLogs(t, database, time.Time{}); len(events) != 0 {
		t.Fatalf("events = %+v, want the window emptied by opening it", events)
	}
}

func TestClearPluginLogs(t *testing.T) {
	database := openTestDB(t)
	mustAppendPluginLog(t, database, eventStart, billing.PluginLogInfo, "事件", time.Time{})

	cleared, errClear := database.ClearPluginLogs()
	if cleared != 1 || errClear != nil {
		t.Fatalf("ClearPluginLogs = %d, %v; want 1", cleared, errClear)
	}
	if events := mustPluginLogs(t, database, time.Time{}); len(events) != 0 {
		t.Fatalf("events = %+v, want the log emptied", events)
	}
}

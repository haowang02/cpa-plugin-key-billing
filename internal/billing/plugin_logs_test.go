package billing

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func mustPluginLogs(t *testing.T, store *Store) []PluginLog {
	t.Helper()
	events, errEvents := store.PluginLogs()
	if errEvents != nil {
		t.Fatalf("PluginLogs error = %v", errEvents)
	}
	return events
}

// The store stamps each line with its own clock and reads back the window that
// clock says is current; the order and the storage of them belong to the
// repository and are exercised against a real database.
func TestPluginLogsKeepTheRetentionWindow(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	store, _ := newStoreWithRepository(t)
	// Configuring the store already reported the database it loaded.
	if _, errClear := store.ClearPluginLogs(); errClear != nil {
		t.Fatalf("ClearPluginLogs error = %v", errClear)
	}
	store.now = func() time.Time { return now.Add(-PluginLogRetention - time.Hour) }
	store.AddPluginLog(PluginLogInfo, "过期事件")
	store.now = func() time.Time { return now }
	store.AddPluginLog(PluginLogInfo, "事件")

	events := mustPluginLogs(t, store)
	if len(events) != 1 || events[0].Message != "事件" {
		t.Fatalf("events = %+v, want only the one inside the window", events)
	}

	store.now = func() time.Time { return now.Add(PluginLogRetention + time.Hour) }
	if events := mustPluginLogs(t, store); len(events) != 0 {
		t.Fatalf("len = %d, want the window emptied by age alone", len(events))
	}
}

// A database that cannot be written is invisible without the plugin log: the
// billing record keeps updating in memory while nothing reaches disk.
func TestUnwritableDatabaseIsReportedOnceAndOnRecovery(t *testing.T) {
	store, repo := newStoreWithRepository(t)
	repo.fail = errors.New("disk full")

	for range 3 {
		store.ReplaceAll(func(state *State) { state.Keys["scope-a"] = &KeyState{Label: "Alice"} })
	}
	reported := 0
	for _, event := range mustPluginLogs(t, store) {
		if event.Level == PluginLogError && strings.Contains(event.Message, "保存计费数据失败") {
			reported++
		}
	}
	if reported != 1 {
		t.Fatalf("reported the same failure %d times, want once", reported)
	}

	repo.fail = nil
	store.ReplaceAll(func(state *State) { state.Keys["scope-a"].Label = "Recovered" })
	if events := mustPluginLogs(t, store); events[0].Message != "计费数据库恢复写入。" {
		t.Fatalf("events = %+v, want the recovery reported", events[0])
	}
}

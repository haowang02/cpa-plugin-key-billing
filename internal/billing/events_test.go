package billing

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func mustEvents(t *testing.T, store *Store) []Event {
	t.Helper()
	events, errEvents := store.Events()
	if errEvents != nil {
		t.Fatalf("Events error = %v", errEvents)
	}
	return events
}

// The store stamps each line with its own clock and reads back the window that
// clock says is current; the order and the storage of them belong to the
// repository and are exercised against a real database.
func TestEventsKeepTheRetentionWindow(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	store, _ := newStoreWithRepository(t)
	// Configuring the store already reported the database it loaded.
	if _, errClear := store.ClearEvents(); errClear != nil {
		t.Fatalf("ClearEvents error = %v", errClear)
	}
	store.now = func() time.Time { return now.Add(-LogRetention - time.Hour) }
	store.Event(EventInfo, "过期事件")
	store.now = func() time.Time { return now }
	store.Event(EventInfo, "事件")

	events := mustEvents(t, store)
	if len(events) != 1 || events[0].Message != "事件" {
		t.Fatalf("events = %+v, want only the one inside the window", events)
	}

	store.now = func() time.Time { return now.Add(LogRetention + time.Hour) }
	if events := mustEvents(t, store); len(events) != 0 {
		t.Fatalf("len = %d, want the window emptied by age alone", len(events))
	}
}

// A database that cannot be written is invisible without the plugin log: the
// billing record keeps updating in memory while nothing reaches disk.
func TestUnwritableDatabaseIsReportedOnceAndOnRecovery(t *testing.T) {
	store, repo := newStoreWithRepository(t)
	repo.fail = errors.New("disk full")

	for range 3 {
		store.ReplaceAll(func(state *State) { state.Keys["scope-a"] = &KeyState{Lifetime: Totals{Requests: 1}} })
	}
	reported := 0
	for _, event := range mustEvents(t, store) {
		if event.Level == EventError && strings.Contains(event.Message, "保存计费数据失败") {
			reported++
		}
	}
	if reported != 1 {
		t.Fatalf("reported the same failure %d times, want once", reported)
	}

	repo.fail = nil
	store.ReplaceAll(func(state *State) { state.Keys["scope-a"].Lifetime.Requests = 2 })
	if events := mustEvents(t, store); events[0].Message != "计费数据库恢复写入。" {
		t.Fatalf("events = %+v, want the recovery reported", events[0])
	}
}

func TestRecoveredWriteIncludesLogsThatFailedEarlier(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	store, repo := newAccountStoreWithRepository(t, now)
	repo.fail = errors.New("disk full")
	store.RecordUsage(subsetEvent("scope-a", now))
	store.RecordUsage(subsetEvent("scope-a", now.Add(time.Minute)))
	if len(repo.log) != 0 {
		t.Fatalf("log entries = %d while writes fail", len(repo.log))
	}

	repo.fail = nil
	if errLabel := store.SetLabel("scope-a", "Alice"); errLabel != nil {
		t.Fatal(errLabel)
	}
	if len(repo.log) != 2 {
		t.Fatalf("recovered log entries = %d, want 2", len(repo.log))
	}
}

func TestFailedWritesBoundPendingLogs(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	store, repo := newAccountStoreWithRepository(t, now)
	repo.fail = errors.New("disk full")
	for i := range maxPendingLogEntries + 25 {
		store.RecordUsage(subsetEvent("scope-a", now.Add(time.Duration(i)*time.Second)))
	}
	if len(store.dirty.Log) != maxPendingLogEntries {
		t.Fatalf("pending logs = %d, want %d", len(store.dirty.Log), maxPendingLogEntries)
	}
	if want := now.Add(25 * time.Second); !store.dirty.Log[0].At.Equal(want) {
		t.Fatalf("oldest pending log = %v, want %v", store.dirty.Log[0].At, want)
	}
}

func TestClearLogsDropsEntriesWaitingForRecovery(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	store, repo := newAccountStoreWithRepository(t, now)
	repo.fail = errors.New("disk full")
	store.RecordUsage(subsetEvent("scope-a", now))

	if _, errClear := store.ClearLogs(); errClear != nil {
		t.Fatal(errClear)
	}
	repo.fail = nil
	if errLabel := store.SetLabel("scope-a", "Alice"); errLabel != nil {
		t.Fatal(errLabel)
	}
	if len(repo.log) != 0 {
		t.Fatalf("cleared log recovered %d stale entries", len(repo.log))
	}
}

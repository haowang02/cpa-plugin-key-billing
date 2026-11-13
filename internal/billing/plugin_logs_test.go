package billing

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func mustPluginLogs(t *testing.T, store *Store) []PluginLog {
	t.Helper()
	page, errEvents := store.PluginLogsPage(PluginLogQuery{Limit: 500})
	if errEvents != nil {
		t.Fatalf("PluginLogsPage error = %v", errEvents)
	}
	return page.Entries
}

func TestDebugPluginLogsFollowConfig(t *testing.T) {
	repo := &memoryRepository{}
	cfg := testConfig(t)
	cfg.Debug = false
	store := NewStore(func(string) (Repository, error) { return repo, nil }, nil)
	if err := store.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.ClearPluginLogs(); err != nil {
		t.Fatal(err)
	}
	store.AddPluginLog(PluginLogDebug, "disabled")
	store.AddPluginLog(PluginLogInfo, "info")
	store.AddPluginLog(PluginLogError, "error")
	events := mustPluginLogs(t, store)
	if len(events) != 2 || events[0].Level != PluginLogError || events[1].Level != PluginLogInfo {
		t.Fatalf("debug-disabled logs = %+v", events)
	}

	cfg.Debug = true
	if err := store.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClearPluginLogs(); err != nil {
		t.Fatal(err)
	}
	store.AddPluginLog(PluginLogDebug, "enabled")
	events = mustPluginLogs(t, store)
	if len(events) != 1 || events[0].Level != PluginLogDebug || events[0].Message != "enabled" {
		t.Fatalf("debug-enabled logs = %+v", events)
	}
}

func TestPluginLogsKeepTheRetentionWindow(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	store, _ := newStoreWithRepository(t)
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

func TestUnwritableDatabaseIsReportedOnceAndOnRecovery(t *testing.T) {
	store, repo := newStoreWithRepository(t)
	repo.fail = errors.New("disk full")

	for range 3 {
		store.ReplaceAll(func(state *State) { state.Keys["scope-a"] = &KeyState{Label: "Alice"} })
	}
	reported := 0
	for _, event := range mustPluginLogs(t, store) {
		if event.Level == PluginLogError && strings.Contains(event.Message, "Failed to save billing data") {
			reported++
		}
	}
	if reported != 1 {
		t.Fatalf("reported the same failure %d times, want once", reported)
	}

	repo.fail = nil
	store.ReplaceAll(func(state *State) { state.Keys["scope-a"].Label = "Recovered" })
	if events := mustPluginLogs(t, store); events[0].Message != "Billing database writes have recovered" {
		t.Fatalf("events = %+v, want the recovery reported", events[0])
	}
}

package billing

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEventsAreNewestFirstAndKeptByAge(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	// The plugin log is held in memory and owes nothing to storage.
	store := NewStore(nil)
	store.now = func() time.Time { return now.Add(-EventRetention - time.Hour) }
	store.Event(EventInfo, "过期事件")
	store.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		store.Event(EventInfo, "事件 %d", i)
	}

	events := store.Events()
	if len(events) != 3 {
		t.Fatalf("len = %d, want every event inside the window kept", len(events))
	}
	if events[0].Message != "事件 2" || events[len(events)-1].Message != "事件 0" {
		t.Fatalf("events = %q … %q, want the newest first", events[0].Message, events[len(events)-1].Message)
	}

	store.now = func() time.Time { return now.Add(EventRetention + time.Hour) }
	if events := store.Events(); len(events) != 0 {
		t.Fatalf("len = %d, want the window emptied by age alone", len(events))
	}
}

// A database that cannot be written is invisible without the plugin log: the
// billing record keeps updating in memory while nothing reaches disk.
func TestUnwritableDatabaseIsReportedOnceAndOnRecovery(t *testing.T) {
	store, repo := newStoreWithRepository(t)
	repo.fail = errors.New("disk full")

	for i := 0; i < 3; i++ {
		store.Update(func(state *State) { state.Keys["scope-a"] = &KeyState{Lifetime: Totals{Requests: 1}} })
	}
	reported := 0
	for _, event := range store.Events() {
		if event.Level == EventError && strings.Contains(event.Message, "保存计费数据失败") {
			reported++
		}
	}
	if reported != 1 {
		t.Fatalf("reported the same failure %d times, want once", reported)
	}

	repo.fail = nil
	store.Update(func(state *State) { state.Keys["scope-a"].Lifetime.Requests = 2 })
	if store.Events()[0].Message != "计费数据库恢复写入。" {
		t.Fatalf("events = %+v, want the recovery reported", store.Events()[0])
	}
}

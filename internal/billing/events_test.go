package billing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEventsAreNewestFirstAndKeptByAge(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	store := NewStore()
	t.Cleanup(store.Close)
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

// A state file that cannot be written is invisible without the plugin log: the
// billing record keeps updating in memory while nothing reaches disk.
func TestUnwritableStateFileIsReportedOnceAndOnRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store := NewStore()
	t.Cleanup(store.Close)
	if errConfigure := store.Configure(Config{Enabled: true, StateFile: path}); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	if errChmod := os.Chmod(dir, 0o500); errChmod != nil {
		t.Fatalf("chmod: %v", errChmod)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	for i := 0; i < 3; i++ {
		store.Update(func(state *State) { state.Keys["scope-a"] = &KeyState{Lifetime: Totals{Requests: 1}} })
		if errFlush := store.Flush(); errFlush == nil {
			t.Skip("the filesystem allows writing into a read-only directory")
		}
	}
	errors := 0
	for _, event := range store.Events() {
		if event.Level == EventError && strings.Contains(event.Message, "保存状态文件失败") {
			errors++
		}
	}
	if errors != 1 {
		t.Fatalf("reported the same failure %d times, want once", errors)
	}

	if errChmod := os.Chmod(dir, 0o700); errChmod != nil {
		t.Fatalf("chmod: %v", errChmod)
	}
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush error = %v", errFlush)
	}
	if store.Events()[0].Message != "状态文件恢复写入。" {
		t.Fatalf("events = %+v, want the recovery reported", store.Events()[0])
	}
}

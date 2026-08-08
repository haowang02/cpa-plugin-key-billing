package billing

import (
	"testing"
	"time"
)

func newLogStore(t *testing.T, now time.Time, retention int) *Store {
	t.Helper()
	store := newAccountStore(t, now)
	cfg := store.cfg
	cfg.LogEntries = retention
	if err := store.Configure(cfg); err != nil {
		t.Fatalf("Configure error = %v", err)
	}
	return store
}

func TestLogRecordsTheComputedBill(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store := newLogStore(t, now, 10)
	store.RecordUsage(subsetEvent("scope-a", now))
	if err := store.SetLabel("scope-a", "Alice"); err != nil {
		t.Fatalf("SetLabel error = %v", err)
	}

	view := store.Logs(0)
	if len(view.Entries) != 1 || view.Retained != 1 || view.Limit != 10 {
		t.Fatalf("view = %+v", view)
	}
	entry := view.Entries[0]
	if entry.Scope != "scope-a" || entry.Label != "Alice" || entry.Preview != "sk-tes…0001" ||
		entry.Model != "gpt-5.5" || entry.PriceSource != PriceSourceOverride ||
		entry.ClientProtocol != "claude" || entry.Provider != "codex" || entry.ExecutorType != "CodexExecutor" ||
		entry.AccountingQuality != TokenAccountingComplete {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.Cost.UncachedInputTokens != 500 || entry.Cost.CacheReadTokens != 400 ||
		entry.Cost.CacheWriteTokens != 100 || entry.Cost.BilledOutputTokens != 500 {
		t.Fatalf("Cost = %+v", entry.Cost)
	}
	assertClose(t, "TotalUSD", entry.Cost.TotalUSD, wantSubsetCost)
}

func TestLogRetentionAndDisable(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store := newLogStore(t, start, 3)
	for i := 0; i < 5; i++ {
		store.RecordUsage(subsetEvent("scope-a", start.Add(time.Duration(i)*time.Minute)))
	}
	view := store.Logs(0)
	if len(view.Entries) != 3 || !view.Entries[0].At.Equal(start.Add(4*time.Minute)) {
		t.Fatalf("entries = %+v, want the newest three", view.Entries)
	}

	cfg := store.cfg
	cfg.LogEntries = 0
	if err := store.Configure(cfg); err != nil {
		t.Fatalf("Configure error = %v", err)
	}
	store.RecordUsage(subsetEvent("scope-a", start))
	if view = store.Logs(0); len(view.Entries) != 0 || view.Limit != 0 {
		t.Fatalf("view = %+v, want logging disabled", view)
	}
}

func TestForgettingAKeyDropsItsLog(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store := newLogStore(t, now, 10)
	store.RecordUsage(subsetEvent("scope-a", now))
	store.RecordUsage(subsetEvent("scope-b", now))
	if _, err := store.ForgetKeys([]string{"scope-a"}); err != nil {
		t.Fatalf("ForgetKeys error = %v", err)
	}
	entries := store.Logs(0).Entries
	if len(entries) != 1 || entries[0].Scope != "scope-b" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestClearLogsOnlyClearsLogs(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store := newLogStore(t, now, 10)
	store.RecordUsage(subsetEvent("scope-a", now))
	if cleared := store.ClearLogs(); cleared != 1 {
		t.Fatalf("ClearLogs = %d, want 1", cleared)
	}
	if entries := store.Logs(0).Entries; len(entries) != 0 {
		t.Fatalf("entries = %+v", entries)
	}
	store.Read(func(state *State) {
		if state.Keys["scope-a"] == nil || state.Keys["scope-a"].Lifetime.Requests != 1 {
			t.Fatalf("usage was cleared with logs: %+v", state.Keys)
		}
	})
}

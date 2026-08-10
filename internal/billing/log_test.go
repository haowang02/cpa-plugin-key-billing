package billing

import (
	"testing"
	"time"
)

func TestLogRecordsTheComputedBill(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	store.Update(func(state *State) {
		state.ensureKey("scope-a").Preview = "sk-tes…0001"
	})
	store.LearnCredential("auth-codex", "codex", "oauth", "ops@example.com")
	store.RecordUsage(subsetEvent("scope-a", now))
	if err := store.SetLabel("scope-a", "Alice"); err != nil {
		t.Fatalf("SetLabel error = %v", err)
	}

	view := store.Logs(0)
	if len(view.Entries) != 1 || view.Total != 1 {
		t.Fatalf("view = %+v", view)
	}
	entry := view.Entries[0]
	if entry.Scope != "scope-a" || entry.Label != "Alice" || entry.Preview != "sk-tes…0001" ||
		entry.UpstreamModel != "gpt-5.5" || entry.BillingModel != "gpt-5.5" || entry.PriceSource != PriceSourceOverride ||
		entry.Endpoint != "/v1/messages" || entry.Source != "codex · ops@example.com" ||
		entry.AccountingQuality != TokenAccountingComplete {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.Cost.UncachedInputTokens != 500 || entry.Cost.CacheReadTokens != 400 ||
		entry.Cost.CacheWriteTokens != 100 || entry.Cost.BilledOutputTokens != 500 {
		t.Fatalf("Cost = %+v", entry.Cost)
	}
	assertClose(t, "TotalUSD", entry.Cost.TotalUSD, wantSubsetCost)
}

// The log holds a window of time rather than a number of entries, so a busy
// period is kept whole and a quiet one does not push it out.
func TestLogKeepsTheRetentionWindow(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, start)
	for i := 0; i < 3; i++ {
		store.RecordUsage(subsetEvent("scope-a", start.Add(time.Duration(i)*time.Minute)))
	}
	view := store.Logs(0)
	if len(view.Entries) != 3 || view.Total != 3 {
		t.Fatalf("view total = %d, want every entry inside the window", view.Total)
	}
	if !view.Entries[0].At.Equal(start.Add(2 * time.Minute)) {
		t.Fatalf("entries = %+v, want the newest first", view.Entries[0])
	}

	store.now = func() time.Time { return start.Add(LogRetention + 24*time.Hour) }
	if view = store.Logs(0); view.Total != 0 || len(view.Entries) != 0 {
		t.Fatalf("view = %+v, want the window emptied by age", view)
	}
	store.RecordUsage(subsetEvent("scope-a", store.Now()))
	store.Read(func(state *State) {
		if len(state.Log) != 1 {
			t.Fatalf("state.Log = %d entries, want the stale ones dropped on append", len(state.Log))
		}
	})
}

func TestClearLogsOnlyClearsLogs(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
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

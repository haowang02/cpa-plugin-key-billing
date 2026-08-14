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

	view := store.Logs(LogQuery{})
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
	view := store.Logs(LogQuery{})
	if len(view.Entries) != 3 || view.Total != 3 {
		t.Fatalf("view total = %d, want every entry inside the window", view.Total)
	}
	if !view.Entries[0].At.Equal(start.Add(2 * time.Minute)) {
		t.Fatalf("entries = %+v, want the newest first", view.Entries[0])
	}

	store.now = func() time.Time { return start.Add(LogRetention + 24*time.Hour) }
	if view = store.Logs(LogQuery{}); view.Total != 0 || len(view.Entries) != 0 {
		t.Fatalf("view = %+v, want the window emptied by age", view)
	}
	store.RecordUsage(subsetEvent("scope-a", store.Now()))
	store.Read(func(state *State) {
		if len(state.Log) != 1 {
			t.Fatalf("state.Log = %d entries, want the stale ones dropped on append", len(state.Log))
		}
	})
}

func loggedStore(t *testing.T, start time.Time) *Store {
	t.Helper()
	store := newAccountStore(t, start)
	store.Update(func(state *State) {
		state.ensureKey("scope-a").Label = "Alice"
		state.ensureKey("scope-b").Label = "Bob"
	})
	store.LearnCredential("auth-codex", "codex", "oauth", "ops@example.com")
	for i := 0; i < 4; i++ {
		store.RecordUsage(subsetEvent("scope-a", start.Add(time.Duration(i)*time.Minute)))
	}
	canceled := subsetEvent("scope-b", start.Add(4*time.Minute))
	canceled.Outcome = OutcomeCanceled
	store.RecordUsage(canceled)
	failed := subsetEvent("scope-b", start.Add(5*time.Minute))
	failed.Outcome = OutcomeFailed
	failed.Record.Responded = true
	store.RecordUsage(failed)
	return store
}

// Only one page travels to the panel, so the view has to describe the whole
// match: how many entries stand behind the page, and what each status filter
// would return for the same search.
func TestLogsPageOverTheWholeMatch(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store := loggedStore(t, start)

	view := store.Logs(LogQuery{Limit: 2})
	if len(view.Entries) != 2 || view.Total != 6 || !view.Entries[0].At.Equal(start.Add(5*time.Minute)) {
		t.Fatalf("view = %+v of %d, want the newest 2 of 6", view.Entries, view.Total)
	}
	if view.Outcomes != (LogOutcomeCounts{All: 6, Succeeded: 4, Failed: 1, Canceled: 1}) {
		t.Fatalf("outcomes = %+v", view.Outcomes)
	}
	if view = store.Logs(LogQuery{Offset: 4, Limit: 2}); len(view.Entries) != 2 ||
		!view.Entries[1].At.Equal(start) {
		t.Fatalf("last page = %+v, want the two oldest entries", view.Entries)
	}
	// A page past the end still reports the total the pager needs to recover.
	if view = store.Logs(LogQuery{Offset: 20, Limit: 2}); len(view.Entries) != 0 || view.Total != 6 {
		t.Fatalf("view = %+v, want an empty page over a counted log", view)
	}
}

// The counts ignore the chosen status, so picking one does not zero the others
// and strand the operator on a single filter.
func TestLogFiltersCountWhatTheyHide(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store := loggedStore(t, start)

	// Succeeded is the absence of an outcome rather than a stored value.
	if view := store.Logs(LogQuery{Outcome: OutcomeSucceeded}); view.Total != 4 || view.Outcomes.All != 6 {
		t.Fatalf("view = %d entries, outcomes %+v", view.Total, view.Outcomes)
	}
	// The search covers identity the entry does not carry: the label here, the
	// credential name below.
	if view := store.Logs(LogQuery{Search: "bOb"}); view.Total != 2 ||
		view.Outcomes != (LogOutcomeCounts{All: 2, Failed: 1, Canceled: 1}) {
		t.Fatalf("view = %d entries, outcomes %+v; want only Bob's requests", view.Total, view.Outcomes)
	}
	if view := store.Logs(LogQuery{Search: "ops@example.com"}); view.Total != 6 {
		t.Fatalf("total = %d, want every request matched by its credential", view.Total)
	}
	if view := store.Logs(LogQuery{Search: "bob", Outcome: OutcomeSucceeded}); view.Total != 0 ||
		view.Outcomes.All != 2 {
		t.Fatalf("view = %+v, want no rows but the search still counted", view)
	}
}

func TestClearLogsOnlyClearsLogs(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	store.RecordUsage(subsetEvent("scope-a", now))
	if cleared := store.ClearLogs(); cleared != 1 {
		t.Fatalf("ClearLogs = %d, want 1", cleared)
	}
	if entries := store.Logs(LogQuery{}).Entries; len(entries) != 0 {
		t.Fatalf("entries = %+v", entries)
	}
	store.Read(func(state *State) {
		if state.Keys["scope-a"] == nil || state.Keys["scope-a"].Lifetime.Requests != 1 {
			t.Fatalf("usage was cleared with logs: %+v", state.Keys)
		}
	})
}

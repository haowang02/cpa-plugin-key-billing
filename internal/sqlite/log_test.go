package sqlite

import (
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

var logStart = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

func logEntry(scope string, at time.Time, failed bool) billing.LogEntry {
	return billing.LogEntry{
		At: at, Scope: scope, AuthIndex: "auth-codex", ExecutorType: "CodexExecutor",
		ReasoningEffort: "high", ServiceTier: "auto",
		UpstreamModel: "gpt-5.5", BillingModel: "gpt-5.5",
		Failed: failed, LatencyMS: 1500, TTFTMS: 250,
		AccountingQuality: billing.TokenAccountingComplete, PriceSource: billing.PriceSourceOverride,
		Cost: billing.Cost{TotalUSD: 0.5, UncachedInputTokens: 500, BilledOutputTokens: 500},
	}
}

// loggedDatabase holds four normal events for Alice and two failed events for
// Bob, all served by the same credential.
func loggedDatabase(t *testing.T) (*DB, *billing.State) {
	t.Helper()
	database := openTestDB(t)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Preview: "sk-tes…0001", Label: "Alice"}
	state.Keys["scope-b"] = &billing.KeyState{Preview: "sk-tes…0002", Label: "Bob"}
	state.Credentials["auth-codex"] = billing.Credential{Provider: "codex", Account: "ops@example.com"}

	entries := make([]billing.LogEntry, 0, 6)
	for i := range 4 {
		entries = append(entries, logEntry("scope-a", logStart.Add(time.Duration(i)*time.Minute), false))
	}
	entries = append(entries,
		logEntry("scope-b", logStart.Add(4*time.Minute), true),
		logEntry("scope-b", logStart.Add(5*time.Minute), true))
	mustSave(t, database, state, billing.Changes{AllKeys: true, Credentials: true, Log: entries})
	return database, state
}

func mustQuery(t *testing.T, database *DB, query billing.LogQuery) billing.LogView {
	t.Helper()
	view, errLogs := database.Logs(query, logStart.Add(-billing.LogRetention))
	if errLogs != nil {
		t.Fatalf("Logs error = %v", errLogs)
	}
	return view
}

func TestLogsPageOverTheWholeMatch(t *testing.T) {
	database, _ := loggedDatabase(t)

	view := mustQuery(t, database, billing.LogQuery{Limit: 2})
	if len(view.Entries) != 2 || view.Total != 6 || !view.Entries[0].At.Equal(logStart.Add(5*time.Minute)) {
		t.Fatalf("view = %+v of %d, want the newest 2 of 6", view.Entries, view.Total)
	}
	if view.Statuses != (billing.LogStatusCounts{All: 6, Normal: 4, Failed: 2}) {
		t.Fatalf("statuses = %+v", view.Statuses)
	}
	if view = mustQuery(t, database, billing.LogQuery{Offset: 4, Limit: 2}); len(view.Entries) != 2 ||
		!view.Entries[1].At.Equal(logStart) {
		t.Fatalf("last page = %+v, want the two oldest entries", view.Entries)
	}
	if view = mustQuery(t, database, billing.LogQuery{Offset: 20, Limit: 2}); len(view.Entries) != 0 || view.Total != 6 {
		t.Fatalf("view = %+v, want an empty page over a counted log", view)
	}
}

func TestLogStatusCountsWhatItHides(t *testing.T) {
	database, _ := loggedDatabase(t)

	if view := mustQuery(t, database, billing.LogQuery{Status: billing.UsageStatusNormal}); view.Total != 4 ||
		len(view.Entries) != 4 || view.Statuses.All != 6 {
		t.Fatalf("view = %d entries, statuses %+v", view.Total, view.Statuses)
	}
	if view := mustQuery(t, database, billing.LogQuery{Status: billing.UsageStatusFailed}); view.Total != 2 ||
		len(view.Entries) != 2 {
		t.Fatalf("view = %+v, want the two failed events", view.Entries)
	}
}

func TestLogFieldAndTimeFiltersShareOneQuery(t *testing.T) {
	database, state := loggedDatabase(t)
	state.Credentials["auth-xai"] = billing.Credential{Provider: "xai", Account: "ops-xai@example.com"}
	entry := logEntry("scope-b", logStart.Add(6*time.Minute), false)
	entry.AuthIndex = "auth-xai"
	entry.UpstreamModel = "gpt-5.6-sol"
	entry.BillingModel = "team/gpt-5.6-sol"
	mustSave(t, database, state, billing.Changes{Credentials: true, Log: []billing.LogEntry{entry}})

	query := billing.LogQuery{
		KeyScope: "scope-b", Model: "team/gpt-5.6-sol", Source: "xai · ops-xai@example.com",
		From: logStart.Add(6 * time.Minute), To: logStart.Add(7 * time.Minute), IncludeFilters: true,
	}
	view := mustQuery(t, database, query)
	if view.Total != 1 || len(view.Entries) != 1 || !view.Entries[0].At.Equal(entry.At) {
		t.Fatalf("view = %+v, want the one exact field and time match", view.Entries)
	}
	if view.Filters == nil || len(view.Filters.APIKeys) != 1 || len(view.Filters.Models) != 1 ||
		len(view.Filters.Sources) != 1 {
		t.Fatalf("filter options = %+v", view.Filters)
	}

	if outside := mustQuery(t, database, billing.LogQuery{
		From: entry.At.Add(time.Nanosecond), To: entry.At.Add(time.Minute),
	}); outside.Total != 0 {
		t.Fatalf("outside range total = %d, want 0", outside.Total)
	}
}

func TestLogScopeConstrainsRowsAndCounts(t *testing.T) {
	database, _ := loggedDatabase(t)

	view := mustQuery(t, database, billing.LogQuery{Scope: "scope-a", Limit: 10, IncludeFilters: true})
	if view.Total != 4 || len(view.Entries) != 4 ||
		view.Statuses != (billing.LogStatusCounts{All: 4, Normal: 4}) {
		t.Fatalf("scope-a view = %+v, statuses %+v", view.Entries, view.Statuses)
	}
	if view.Filters == nil || len(view.Filters.APIKeys) != 1 || view.Filters.APIKeys[0].Scope != "scope-a" {
		t.Fatalf("scope-a filter options = %+v", view.Filters)
	}
	for _, entry := range view.Entries {
		if entry.Scope != "scope-a" {
			t.Fatalf("scope-a query returned %+v", entry)
		}
	}

	view = mustQuery(t, database, billing.LogQuery{Scope: "scope-b", Status: billing.UsageStatusFailed, Limit: 10})
	if view.Total != 2 || len(view.Entries) != 2 ||
		view.Statuses != (billing.LogStatusCounts{All: 2, Failed: 2}) {
		t.Fatalf("scope-b view = %+v, statuses %+v", view.Entries, view.Statuses)
	}
}

func TestLogRowsFollowTheKeyTheyName(t *testing.T) {
	database, state := loggedDatabase(t)
	state.Keys["scope-a"].Label = "Alice Cooper"
	mustSave(t, database, state, billing.Changes{Keys: []string{"scope-a"}})

	view := mustQuery(t, database, billing.LogQuery{KeyScope: "scope-a"})
	if view.Total != 4 {
		t.Fatalf("total = %d, want the renamed key's whole history", view.Total)
	}
	if entry := view.Entries[0]; entry.Label != "Alice Cooper" || entry.Preview != "sk-tes…0001" ||
		entry.ExecutorType != "CodexExecutor" ||
		entry.ReasoningEffort != "high" || entry.ServiceTier != "auto" ||
		entry.Source != "codex · ops@example.com" || entry.Provider != "codex" ||
		entry.LatencyMS != 1500 || entry.TTFTMS != 250 {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestLogKeepsTheRetentionWindow(t *testing.T) {
	database, state := loggedDatabase(t)
	later := logStart.Add(billing.LogRetention + time.Hour)

	mustSave(t, database, state, billing.Changes{
		Log:       []billing.LogEntry{logEntry("scope-a", later, false)},
		LogCutoff: later.Add(-billing.LogRetention),
	})
	view, errLogs := database.Logs(billing.LogQuery{}, later.Add(-billing.LogRetention))
	if errLogs != nil {
		t.Fatalf("Logs error = %v", errLogs)
	}
	if view.Total != 1 || !view.Entries[0].At.Equal(later) {
		t.Fatalf("view = %+v, want only the entry inside the window", view.Entries)
	}
}

func TestLoadDropsEntriesPastRetention(t *testing.T) {
	database, _ := loggedDatabase(t)

	snapshot, errLoad := database.Load(logStart.Add(3 * time.Minute))
	if errLoad != nil {
		t.Fatalf("Load error = %v", errLoad)
	}
	if snapshot.LogEntries != 3 {
		t.Fatalf("LogEntries = %d, want the three entries inside the window", snapshot.LogEntries)
	}
	if view := mustQuery(t, database, billing.LogQuery{}); view.Total != 3 {
		t.Fatalf("total = %d, want the aged entries gone from the database", view.Total)
	}
}

func TestClearLogsAndLoggedScopes(t *testing.T) {
	database, _ := loggedDatabase(t)

	scopes, errScopes := database.LoggedScopes(logStart.Add(-billing.LogRetention))
	if errScopes != nil {
		t.Fatalf("LoggedScopes error = %v", errScopes)
	}
	if len(scopes) != 2 {
		t.Fatalf("scopes = %+v, want both keys the log still names", scopes)
	}

	cleared, errClear := database.ClearLogs()
	if errClear != nil || cleared != 6 {
		t.Fatalf("ClearLogs = %d, %v; want 6", cleared, errClear)
	}
	if view := mustQuery(t, database, billing.LogQuery{}); view.Total != 0 || len(view.Entries) != 0 {
		t.Fatalf("view = %+v, want an empty log", view)
	}
	if keys := mustLoad(t, database).State.Keys; len(keys) != 2 {
		t.Fatalf("keys = %+v, want the records kept", keys)
	}
}

package sqlite

import (
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

var logStart = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

func logEntry(scope string, at time.Time, outcome billing.RequestOutcome) billing.LogEntry {
	return billing.LogEntry{
		At: at, Scope: scope, RequestID: "req-1", Endpoint: "/v1/messages", AuthIndex: "auth-codex",
		UpstreamModel: "gpt-5.5", BillingModel: "gpt-5.5", Outcome: outcome,
		AccountingQuality: billing.TokenAccountingComplete, PriceSource: billing.PriceSourceOverride,
		Cost: billing.Cost{TotalUSD: 0.5, UncachedInputTokens: 500, BilledOutputTokens: 500},
	}
}

// loggedDatabase holds four succeeded requests for Alice and one canceled plus
// one failed for Bob, all served by the same credential.
func loggedDatabase(t *testing.T) (*DB, *billing.State) {
	t.Helper()
	database := openTestDB(t)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Preview: "sk-tes…0001", Label: "Alice"}
	state.Keys["scope-b"] = &billing.KeyState{Preview: "sk-tes…0002", Label: "Bob"}
	state.Credentials["auth-codex"] = billing.Credential{Provider: "codex", Account: "ops@example.com"}

	entries := make([]billing.LogEntry, 0, 6)
	for i := range 4 {
		entries = append(entries, logEntry("scope-a", logStart.Add(time.Duration(i)*time.Minute), ""))
	}
	entries = append(entries,
		logEntry("scope-b", logStart.Add(4*time.Minute), billing.OutcomeCanceled),
		logEntry("scope-b", logStart.Add(5*time.Minute), billing.OutcomeFailed))
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

// Only one page travels to the panel, so the view has to describe the whole
// match: how many entries stand behind the page, and what each status filter
// would return for the same search.
func TestLogsPageOverTheWholeMatch(t *testing.T) {
	database, _ := loggedDatabase(t)

	view := mustQuery(t, database, billing.LogQuery{Limit: 2})
	if len(view.Entries) != 2 || view.Total != 6 || !view.Entries[0].At.Equal(logStart.Add(5*time.Minute)) {
		t.Fatalf("view = %+v of %d, want the newest 2 of 6", view.Entries, view.Total)
	}
	if view.Outcomes != (billing.LogOutcomeCounts{All: 6, Succeeded: 4, Failed: 1, Canceled: 1}) {
		t.Fatalf("outcomes = %+v", view.Outcomes)
	}
	if view = mustQuery(t, database, billing.LogQuery{Offset: 4, Limit: 2}); len(view.Entries) != 2 ||
		!view.Entries[1].At.Equal(logStart) {
		t.Fatalf("last page = %+v, want the two oldest entries", view.Entries)
	}
	// A page past the end still reports the total the pager needs to recover.
	if view = mustQuery(t, database, billing.LogQuery{Offset: 20, Limit: 2}); len(view.Entries) != 0 || view.Total != 6 {
		t.Fatalf("view = %+v, want an empty page over a counted log", view)
	}
}

// The counts ignore the chosen status, so picking one does not zero the others
// and strand the operator on a single filter.
func TestLogFiltersCountWhatTheyHide(t *testing.T) {
	database, _ := loggedDatabase(t)

	// Succeeded is the absence of an outcome rather than a stored value.
	if view := mustQuery(t, database, billing.LogQuery{Outcome: billing.OutcomeSucceeded}); view.Total != 4 ||
		len(view.Entries) != 4 || view.Outcomes.All != 6 {
		t.Fatalf("view = %d entries, outcomes %+v", view.Total, view.Outcomes)
	}
	if view := mustQuery(t, database, billing.LogQuery{Outcome: string(billing.OutcomeFailed)}); view.Total != 1 ||
		len(view.Entries) != 1 {
		t.Fatalf("view = %+v, want the one failed request", view.Entries)
	}
	// The search covers identity the entry does not carry: the label here, the
	// credential name below.
	if view := mustQuery(t, database, billing.LogQuery{Search: "bOb"}); view.Total != 2 ||
		view.Outcomes != (billing.LogOutcomeCounts{All: 2, Failed: 1, Canceled: 1}) {
		t.Fatalf("view = %d entries, outcomes %+v; want only Bob's requests", view.Total, view.Outcomes)
	}
	if view := mustQuery(t, database, billing.LogQuery{Search: "ops@example.com"}); view.Total != 6 {
		t.Fatalf("total = %d, want every request matched by its credential", view.Total)
	}
	if view := mustQuery(t, database, billing.LogQuery{Search: "bob", Outcome: billing.OutcomeSucceeded}); view.Total != 0 ||
		view.Outcomes.All != 2 {
		t.Fatalf("view = %+v, want no rows but the search still counted", view)
	}
}

// The display identity is joined rather than copied into the entry, so renaming
// a key renames its history instead of leaving it under the old name.
func TestLogRowsFollowTheKeyTheyName(t *testing.T) {
	database, state := loggedDatabase(t)
	state.Keys["scope-a"].Label = "Alice Cooper"
	mustSave(t, database, state, billing.Changes{Keys: []string{"scope-a"}})

	view := mustQuery(t, database, billing.LogQuery{Search: "alice cooper"})
	if view.Total != 4 {
		t.Fatalf("total = %d, want the renamed key's whole history", view.Total)
	}
	if entry := view.Entries[0]; entry.Label != "Alice Cooper" || entry.Preview != "sk-tes…0001" ||
		entry.Source != "codex · ops@example.com" {
		t.Fatalf("entry = %+v", entry)
	}
}

// The log holds a window of time rather than a number of entries. Appending is
// the only moment it grows, so it is also where the window is trimmed.
func TestLogKeepsTheRetentionWindow(t *testing.T) {
	database, state := loggedDatabase(t)
	later := logStart.Add(billing.LogRetention + time.Hour)

	mustSave(t, database, state, billing.Changes{
		Log:       []billing.LogEntry{logEntry("scope-a", later, "")},
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

// Appending is not the only moment the window has to be trimmed: a deployment
// whose traffic stopped never appends again, and its aged entries would sit on
// disk for as long as the database lives.
func TestLoadDropsEntriesPastRetention(t *testing.T) {
	database, _ := loggedDatabase(t)

	snapshot, errLoad := database.Load(logStart.Add(3 * time.Minute))
	if errLoad != nil {
		t.Fatalf("Load error = %v", errLoad)
	}
	// The count is what startup reports, so it has to be the number the log will
	// actually return rather than the number of rows that survived until now.
	if snapshot.LogEntries != 3 {
		t.Fatalf("LogEntries = %d, want the three entries inside the window", snapshot.LogEntries)
	}
	if view := mustQuery(t, database, billing.LogQuery{}); view.Total != 3 {
		t.Fatalf("total = %d, want the aged entries gone from the database", view.Total)
	}
}

// SQLite's own lower() folds ASCII and leaves every other alphabet alone. Labels
// and remarks are free operator text, so a search has to fold the way the rest
// of the plugin does.
func TestLogSearchFoldsBeyondASCII(t *testing.T) {
	database, state := loggedDatabase(t)
	state.Keys["scope-a"].Label = "ÉQUIPE PARIS"
	mustSave(t, database, state, billing.Changes{Keys: []string{"scope-a"}})

	if view := mustQuery(t, database, billing.LogQuery{Search: "équipe"}); view.Total != 4 {
		t.Fatalf("total = %d, want the key matched by its label in the other case", view.Total)
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
	// The keys the log named are still there; only the log went.
	if keys := mustLoad(t, database).State.Keys; len(keys) != 2 {
		t.Fatalf("keys = %+v, want the records kept", keys)
	}
}

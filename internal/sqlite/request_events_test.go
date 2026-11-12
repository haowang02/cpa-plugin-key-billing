package sqlite

import (
	"maps"
	"reflect"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

var eventStart = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

func requestEvent(scope string, at time.Time) billing.RequestEvent {
	outboundTier := "default"
	return billing.RequestEvent{
		At: at, Scope: scope, AuthIndex: "auth-codex", ExecutorType: "CodexExecutor",
		Provider: "codex", Account: "ops@example.com",
		ReasoningEffort: "high", ServiceTier: "auto", EffectiveServiceTier: &outboundTier, ResponseServiceTier: "priority",
		UpstreamModel: "gpt-5.5", ResponseModel: "gpt-5.6-luna", BillingModel: "gpt-5.5",
		LatencyMS: 1500, TTFTMS: 250,
		AccountingQuality: billing.TokenAccountingComplete, PriceSource: billing.PriceSourceCustom,
		Cost: billing.Cost{
			Total: 0.5, UncachedInput: 0.2, Output: 0.3,
			UncachedInputTokens: 500, BilledOutputTokens: 500,
		},
	}
}

// requestEventDatabase holds four normal events for Alice and two failed events
// for Bob.
func requestEventDatabase(t *testing.T) (*DB, *billing.State) {
	t.Helper()
	database := openTestDB(t)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Preview: "sk-tes…0001", Label: "Alice"}
	state.Keys["scope-b"] = &billing.KeyState{Preview: "sk-tes…0002", Label: "Bob"}

	events := make([]billing.RequestEvent, 0, 4)
	for i := range 4 {
		events = append(events, requestEvent("scope-a", eventStart.Add(time.Duration(i)*time.Minute)))
	}
	errors := []billing.RequestErrorEvent{
		{Event: requestEvent("scope-b", eventStart.Add(4*time.Minute))},
		{Event: requestEvent("scope-b", eventStart.Add(5*time.Minute))},
	}
	mustSave(t, database, state, billing.Changes{AllKeys: true, NormalRequestEvents: events, RequestErrorEvents: errors})
	return database, state
}

func mustQueryRequestEvents(t *testing.T, database *DB, query billing.RequestEventQuery) billing.RequestEventView {
	t.Helper()
	view, err := database.RequestEvents(query, eventStart.Add(-365*24*time.Hour))
	if err != nil {
		t.Fatalf("RequestEvents error = %v", err)
	}
	return view
}

func TestRequestEventsPageOverTheWholeMatch(t *testing.T) {
	database, state := requestEventDatabase(t)
	original := mustQueryRequestEvents(t, database, billing.RequestEventQuery{})

	view := mustQueryRequestEvents(t, database, billing.RequestEventQuery{Limit: 2})
	if len(view.Entries) != 2 || view.Total != 6 || !view.Entries[0].At.Equal(eventStart.Add(5*time.Minute)) {
		t.Fatalf("view = %+v of %d, want the newest 2 of 6", view.Entries, view.Total)
	}
	if view.Statuses != (billing.RequestEventStatusCounts{All: 6, Normal: 4, Failed: 2}) {
		t.Fatalf("statuses = %+v", view.Statuses)
	}
	snapshot := view.SnapshotID
	late := requestEvent("scope-a", eventStart.Add(5*time.Minute+30*time.Second))
	late.BillingModel = "late-model"
	mustSave(t, database, state, billing.Changes{NormalRequestEvents: []billing.RequestEvent{late}})
	page := mustQueryRequestEvents(t, database, billing.RequestEventQuery{SnapshotID: &snapshot, Offset: 2, IncludeFilters: true})
	if !reflect.DeepEqual(page.Entries, original.Entries[2:]) || page.Statuses != original.Statuses ||
		page.Total != original.Total || len(page.Filters.Models) != 1 {
		t.Fatalf("late completion changed the snapshot: %+v", page)
	}
	if fresh := mustQueryRequestEvents(t, database, billing.RequestEventQuery{}); fresh.Total != 7 || fresh.Entries[0].BillingModel != "late-model" {
		t.Fatalf("refresh did not include the late completion: %+v", fresh)
	}
	if view = mustQueryRequestEvents(t, database, billing.RequestEventQuery{SnapshotID: &snapshot, Offset: 4, Limit: 2}); len(view.Entries) != 2 ||
		!view.Entries[1].At.Equal(eventStart) {
		t.Fatalf("last page = %+v, want the two oldest entries", view.Entries)
	}
	if view = mustQueryRequestEvents(t, database, billing.RequestEventQuery{SnapshotID: &snapshot, Offset: 20, Limit: 2}); len(view.Entries) != 0 || view.Total != 6 {
		t.Fatalf("view = %+v, want an empty page over the counted events", view)
	}
}

func TestRequestEventFailedFilterKeepsOverallCounts(t *testing.T) {
	database, _ := requestEventDatabase(t)
	normal, failed := false, true

	if view := mustQueryRequestEvents(t, database, billing.RequestEventQuery{Failed: &normal}); view.Total != 4 ||
		len(view.Entries) != 4 || view.Statuses.All != 6 {
		t.Fatalf("view = %d entries, statuses %+v", view.Total, view.Statuses)
	}
	if view := mustQueryRequestEvents(t, database, billing.RequestEventQuery{Failed: &failed}); view.Total != 2 ||
		len(view.Entries) != 2 {
		t.Fatalf("view = %+v, want the two failed events", view.Entries)
	}
}

func TestRequestEventFieldAndTimeFilters(t *testing.T) {
	database, state := requestEventDatabase(t)
	// A credential that is not loaded has no auth index, so it names no event.
	for id, credential := range map[string]billing.ConfigCredentialDetails{
		"dummy-xai": {Provider: "xai", AuthIndex: "auth-xai", Name: "xAI Team"}, "dummy-disabled": {Name: "Legacy", Disabled: true},
	} {
		state.ConfigCredentials[billing.CredentialFingerprint(id)] = billing.ConfigCredential{ConfigCredentialDetails: credential}
	}
	entry := requestEvent("scope-b", eventStart.Add(6*time.Minute))
	entry.AuthIndex = "auth-xai"
	entry.Provider = "xai"
	entry.Account = "xai-du…0001"
	entry.UpstreamModel = "gpt-5.6-sol"
	entry.BillingModel = "team/gpt-5.6-sol"
	mustSave(t, database, state, billing.Changes{ConfigCredentials: true, NormalRequestEvents: []billing.RequestEvent{entry}})

	query := billing.RequestEventQuery{
		KeyScope: "scope-b", Model: "team/gpt-5.6-sol", Credential: "xAI Team · xai-du…0001",
		From: eventStart.Add(6 * time.Minute), To: eventStart.Add(7 * time.Minute), IncludeFilters: true,
	}
	view := mustQueryRequestEvents(t, database, query)
	if view.Total != 1 || len(view.Entries) != 1 || !view.Entries[0].At.Equal(entry.At) {
		t.Fatalf("view = %+v, want the one exact field and time match", view.Entries)
	}
	if view.Filters == nil || len(view.Filters.Models) != 1 || len(view.Filters.Credentials) != 1 {
		t.Fatalf("filter options = %+v", view.Filters)
	}
	query.Credential = "XAI TEAM · xai-du…0001"
	if view := mustQueryRequestEvents(t, database, query); view.Total != 0 {
		t.Fatal("credential filter no longer uses exact matching")
	}

	if outside := mustQueryRequestEvents(t, database, billing.RequestEventQuery{
		From: entry.At.Add(time.Nanosecond), To: entry.At.Add(time.Minute),
	}); outside.Total != 0 {
		t.Fatalf("outside range total = %d, want 0", outside.Total)
	}

	entry.At = entry.At.Add(time.Minute)
	entry.AuthIndex, entry.BillingModel = "", ""
	mustSave(t, database, state, billing.Changes{NormalRequestEvents: []billing.RequestEvent{entry}})
	view = mustQueryRequestEvents(t, database, billing.RequestEventQuery{
		Model: entry.UpstreamModel, From: entry.At, To: entry.At.Add(time.Minute), IncludeFilters: true,
	})
	if view.Total != 1 || len(view.Entries) != 1 || len(view.Filters.Models) != 1 || view.Filters.Models[0] != entry.UpstreamModel ||
		view.Entries[0].Credential != "xai · xai-du…0001" {
		t.Fatalf("upstream model filter = %+v", view)
	}
}

func TestRequestEventAccountChangesPreserveHistory(t *testing.T) {
	database, state := requestEventDatabase(t)
	original := mustQueryRequestEvents(t, database, billing.RequestEventQuery{})
	entry := requestEvent("scope-a", eventStart.Add(10*time.Minute))
	streaming, nonStreaming := true, false
	entry.Account = "new@example.com"
	entry.Endpoint, entry.Transport, entry.Stream = "/v1/messages", "http", &nonStreaming
	mustSave(t, database, state, billing.Changes{RequestErrorEvents: []billing.RequestErrorEvent{{Event: entry}}})
	entry.Provider = "openai-compatible-dummy"
	entry.Endpoint, entry.Transport, entry.Stream = "/v1/responses", "websocket", &streaming
	mustSave(t, database, state, billing.Changes{RequestErrorEvents: []billing.RequestErrorEvent{{Event: entry}}})
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = openDatabase(t, database.path)
	if view := mustQueryRequestEvents(t, database, billing.RequestEventQuery{SnapshotID: &original.SnapshotID}); !reflect.DeepEqual(view.Entries, original.Entries) {
		t.Fatal("later usage changed historical events")
	}
	wantCredentials := make(map[string]int64)
	for _, test := range []struct {
		credential          string
		requests, failed    int
		endpoint, transport string
		stream              *bool
	}{
		{"codex · ops@example.com", 6, 2, "", "", nil},
		{"codex · new@example.com", 1, 1, "/v1/messages", "http", &nonStreaming},
		{"dummy · new@example.com", 1, 1, "/v1/responses", "websocket", &streaming},
	} {
		wantCredentials[test.credential] = int64(test.requests)
		query := billing.RequestEventQuery{Credential: test.credential,
			IncludeFilters: true, From: eventStart, To: eventStart.Add(time.Hour)}
		view := mustQueryRequestEvents(t, database, query)
		if view.Total != test.requests || view.Statuses.Failed != test.failed || len(view.Filters.Credentials) != 3 {
			t.Fatalf("filtered events = %+v", view)
		}
		for _, row := range view.Entries {
			if row.Endpoint != test.endpoint || row.Transport != test.transport || !reflect.DeepEqual(row.Stream, test.stream) {
				t.Fatalf("request metadata changed after reopen: %+v", row)
			}
		}
		errors, err := database.RequestErrors(billing.RequestErrorQuery{Credential: test.credential}, eventStart)
		if err != nil || errors.Total != test.failed || errors.Entries[0].Credential != test.credential {
			t.Fatalf("filtered errors = %+v, err = %v", errors, err)
		}
		analysis, err := database.Analysis(query, 7, eventStart)
		if err != nil || analysis.Summary.Requests != int64(test.requests) ||
			len(analysis.UsageDistribution.Credentials) != 1 || analysis.UsageDistribution.Credentials[0].Label != test.credential {
			t.Fatalf("filtered analysis = %+v, err = %v", analysis, err)
		}
	}
	analysis, err := database.Analysis(billing.RequestEventQuery{From: eventStart, To: eventStart.Add(time.Hour)}, 7, eventStart)
	if err != nil {
		t.Fatal(err)
	}
	credentials := make(map[string]int64)
	for _, credential := range analysis.UsageDistribution.Credentials {
		credentials[credential.Label] = credential.Requests
		if credential.TotalTokens != credential.Requests*1000 || credential.CostUSD != float64(credential.Requests)*0.5 {
			t.Fatalf("credential usage = %+v", credential)
		}
	}
	if !reflect.DeepEqual(credentials, wantCredentials) {
		t.Fatalf("credential distribution = %+v, want %+v", credentials, wantCredentials)
	}
}

func TestRequestEventScopeConstrainsRowsAndCounts(t *testing.T) {
	database, _ := requestEventDatabase(t)

	view := mustQueryRequestEvents(t, database, billing.RequestEventQuery{Scope: "scope-a", Limit: 10})
	if view.Total != 4 || len(view.Entries) != 4 ||
		view.Statuses != (billing.RequestEventStatusCounts{All: 4, Normal: 4}) {
		t.Fatalf("scope-a view = %+v, statuses %+v", view.Entries, view.Statuses)
	}
	for _, entry := range view.Entries {
		if entry.Scope != "scope-a" {
			t.Fatalf("scope-a query returned %+v", entry)
		}
	}

	view = mustQueryRequestEvents(t, database, billing.RequestEventQuery{Scope: "scope-b", Limit: 10})
	if view.Total != 2 || len(view.Entries) != 2 ||
		view.Statuses != (billing.RequestEventStatusCounts{All: 2, Failed: 2}) {
		t.Fatalf("scope-b view = %+v, statuses %+v", view.Entries, view.Statuses)
	}
}

func TestRequestEventRowsFollowTheKeyTheyName(t *testing.T) {
	database, state := requestEventDatabase(t)
	state.Keys["scope-a"].Label = "Alice Cooper"
	mustSave(t, database, state, billing.Changes{Keys: []string{"scope-a"}})

	view := mustQueryRequestEvents(t, database, billing.RequestEventQuery{KeyScope: "scope-a"})
	if view.Total != 4 {
		t.Fatalf("total = %d, want the renamed key's whole history", view.Total)
	}
	if entry := view.Entries[0]; entry.Label != "Alice Cooper" || entry.Preview != "sk-tes…0001" ||
		entry.ExecutorType != "CodexExecutor" ||
		entry.ReasoningEffort != "high" || entry.ServiceTier != "auto" || *entry.EffectiveServiceTier != "default" ||
		entry.ResponseServiceTier != "priority" || entry.ResponseModel != "gpt-5.6-luna" ||
		entry.Credential != "codex · ops@example.com" || entry.Provider != "codex" ||
		entry.LatencyMS != 1500 || entry.TTFTMS != 250 {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestRequestEventsKeepTheRetentionWindow(t *testing.T) {
	database, state := requestEventDatabase(t)
	cutoff := eventStart.Add(180*24*time.Hour - 365*24*time.Hour)
	view, err := database.RequestEvents(billing.RequestEventQuery{}, cutoff)
	if err != nil || view.Total != 6 {
		t.Fatalf("six-month history: %+v, %v", view, err)
	}
	failures, err := database.RequestErrors(billing.RequestErrorQuery{}, cutoff)
	if err != nil || failures.Total != 2 {
		t.Fatalf("six-month error history: %+v, %v", failures, err)
	}
	later := eventStart.Add(366 * 24 * time.Hour)

	mustSave(t, database, state, billing.Changes{
		NormalRequestEvents: []billing.RequestEvent{requestEvent("scope-a", later)},
		RequestEventCutoff:  later.Add(-365 * 24 * time.Hour),
	})
	view, err = database.RequestEvents(billing.RequestEventQuery{}, later.Add(-365*24*time.Hour))
	if err != nil {
		t.Fatalf("RequestEvents error = %v", err)
	}
	if view.Total != 1 || !view.Entries[0].At.Equal(later) {
		t.Fatalf("view = %+v, want only the entry inside the window", view.Entries)
	}
	var errorCount int
	if err := database.db.QueryRow("SELECT count(*) FROM request_errors").Scan(&errorCount); err != nil || errorCount != 0 {
		t.Fatalf("expired error details: count=%d, err=%v", errorCount, err)
	}
}

func TestLoadDropsEntriesPastRetention(t *testing.T) {
	database, _ := requestEventDatabase(t)

	snapshot, errLoad := database.Load(eventStart.Add(3*time.Minute), time.Time{})
	if errLoad != nil {
		t.Fatalf("Load error = %v", errLoad)
	}
	if snapshot.RequestEventCount != 3 {
		t.Fatalf("RequestEventCount = %d, want three", snapshot.RequestEventCount)
	}
	if view := mustQueryRequestEvents(t, database, billing.RequestEventQuery{}); view.Total != 3 {
		t.Fatalf("total = %d, want the aged entries gone from the database", view.Total)
	}
}

func TestEventKeysFollowTimeRangeAndRetainDeletedIdentities(t *testing.T) {
	database := openTestDB(t)
	state := billing.NewState()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	state.Keys["active"] = &billing.KeyState{Preview: "sk-tes…0001", InConfig: true}
	state.Keys["deleted"] = &billing.KeyState{Preview: "sk-tes…0002", Label: "Deleted", DeletedAt: start}
	state.Keys["idle"] = &billing.KeyState{Preview: "sk-tes…0003"}
	mustSave(t, database, state, billing.Changes{AllKeys: true,
		NormalRequestEvents: []billing.RequestEvent{
			requestEvent("active", start), requestEvent("active", start.Add(time.Minute)),
			requestEvent("outside", start.Add(-time.Nanosecond)),
			requestEvent("end", start.Add(time.Hour)), requestEvent("", start),
		},
		RequestErrorEvents: []billing.RequestErrorEvent{{Event: requestEvent("deleted", start.Add(2*time.Minute)), Error: billing.RequestError{StatusCode: 502}}},
	})
	keys, err := database.EventKeys(start, start.Add(time.Hour), start.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[2] != (billing.EventKey{}) {
		t.Fatalf("keys = %+v, want distinct normal and error identities, then unassigned", keys)
	}
	found := map[string]billing.EventKey{}
	for _, key := range keys {
		found[key.Scope] = key
	}
	if found["active"].Preview != "sk-tes…0001" || found["deleted"].Label != "Deleted" || !found["deleted"].DeletedAt.Equal(start) {
		t.Fatalf("lost key identity or status: %+v", keys)
	}
	keys, err = database.EventKeys(start, start.Add(time.Hour), start.Add(2*time.Minute))
	if err != nil || len(keys) != 1 || keys[0].Scope != "deleted" {
		t.Fatalf("retention cutoff ignored: %+v, %v", keys, err)
	}
	keys, err = database.EventKeys(start.Add(3*time.Minute), start.Add(time.Hour), start)
	if err != nil || keys == nil || len(keys) != 0 {
		t.Fatalf("empty range = %+v, %v", keys, err)
	}
}

func TestEventKeysIncludeOrphanedZeroUsage(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.db.Exec(`WITH RECURSIVE seq(n) AS (
		VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n < 1100)
		INSERT INTO request_events(at, scope) SELECT ?, 'orphan-' || n FROM seq`, nanos(eventStart)); err != nil {
		t.Fatal(err)
	}
	keys, err := d.EventKeys(eventStart, eventStart.Add(time.Hour), eventStart)
	if err != nil || len(keys) != 1100 {
		t.Fatalf("keys=%d, err=%v", len(keys), err)
	}
	for _, key := range keys {
		if key.Preview != billing.UnknownKeyPreview {
			t.Fatalf("unexpected preview: %+v", key)
		}
	}
}

func TestEventFilterCountsFollowTheOtherSelections(t *testing.T) {
	db := openTestDB(t)
	state := billing.NewState()
	event := func(scope, model, account string, minute int) billing.RequestEvent {
		row := requestEvent(scope, eventStart.Add(time.Duration(minute)*time.Minute))
		row.BillingModel, row.Account = model, account
		return row
	}
	mustSave(t, db, state, billing.Changes{
		NormalRequestEvents: []billing.RequestEvent{
			event("a", "m1", "one", 0), event("", "m2", "two", 3),
			event("a", "outside", "one", -120),
		},
		RequestErrorEvents: []billing.RequestErrorEvent{
			{Event: event("a", "m1", "two", 1), Error: billing.RequestError{ErrorType: "limited"}},
			{Event: event("b", "m2", "one", 2), Error: billing.RequestError{ErrorType: "upstream"}},
			{Event: event("b", "m1", "one", 4), Error: billing.RequestError{ErrorType: "limited"}},
		},
	})
	snapshot := mustQueryRequestEvents(t, db, billing.RequestEventQuery{}).SnapshotID
	mustSave(t, db, state, billing.Changes{RequestErrorEvents: []billing.RequestErrorEvent{
		{Event: event("a", "late", "late", 1), Error: billing.RequestError{ErrorType: "limited"}},
	}})
	from, to, since := eventStart, eventStart.Add(time.Hour), eventStart.Add(-time.Hour)
	check := func(name string, filters billing.RequestEventFilterValues, keys, models, credentials map[string]int) {
		t.Helper()
		if !maps.Equal(filters.KeyCounts, keys) || !maps.Equal(filters.ModelCounts, models) ||
			!maps.Equal(filters.CredentialCounts, credentials) {
			t.Fatalf("%s counts = %+v", name, filters)
		}
	}

	failed := true
	for _, test := range []struct {
		name                      string
		query                     billing.RequestEventQuery
		keys, models, credentials map[string]int
	}{
		{"selected", billing.RequestEventQuery{KeyScope: "a", Model: "m1", Failed: &failed},
			map[string]int{"a": 1, "b": 1}, map[string]int{"m1": 1}, map[string]int{"codex · two": 1}},
		{"account", billing.RequestEventQuery{Scope: "a"}, nil,
			map[string]int{"m1": 2}, map[string]int{"codex · one": 1, "codex · two": 1}},
	} {
		query := test.query
		query.From, query.To, query.SnapshotID, query.IncludeFilters = from, to, &snapshot, true
		view, err := db.RequestEvents(query, since)
		if err != nil {
			t.Fatal(err)
		}
		check(test.name, *view.Filters, test.keys, test.models, test.credentials)
	}

	errors, err := db.RequestErrors(billing.RequestErrorQuery{
		KeyScope: "b", ErrorType: "limited", From: from, To: to, SnapshotID: &snapshot, IncludeFilters: true,
	}, since)
	if err != nil {
		t.Fatal(err)
	}
	check("error", errors.Filters.RequestEventFilterValues,
		map[string]int{"a": 1, "b": 1}, map[string]int{"m1": 1}, map[string]int{"codex · one": 1})
}

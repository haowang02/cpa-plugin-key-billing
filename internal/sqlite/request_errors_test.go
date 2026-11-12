package sqlite

import (
	"maps"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func requestErrorDatabase(t *testing.T) *DB {
	t.Helper()
	database := openTestDB(t)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Preview: "sk-aaa…0001", Label: "Alice"}
	state.Keys["scope-b"] = &billing.KeyState{Preview: "sk-bbb…0002", Label: "Bob"}
	events := []billing.RequestEvent{requestEvent("scope-a", eventStart)}
	errors := []billing.RequestErrorEvent{
		{Event: requestEvent("scope-a", eventStart.Add(time.Minute)), Error: billing.RequestError{
			StatusCode: 429, ErrorType: "rate_limit", Body: `{"error":"limited"}`,
		}},
		{Event: requestEvent("scope-b", eventStart.Add(2*time.Minute)), Error: billing.RequestError{
			StatusCode: 502, ErrorType: "upstream_error", Body: `{"error":"bad gateway"}`,
		}},
	}
	mustSave(t, database, state, billing.Changes{AllKeys: true, NormalRequestEvents: events, RequestErrorEvents: errors})
	return database
}

func TestRequestErrorsAreIndependentFilteredViews(t *testing.T) {
	database := requestErrorDatabase(t)
	view, err := database.RequestErrors(billing.RequestErrorQuery{Scope: "scope-a", StatusCode: 429, ErrorType: "rate_limit", Limit: 10, IncludeFilters: true}, eventStart.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if view.Total != 1 || len(view.Entries) != 1 {
		t.Fatalf("view = %+v", view)
	}
	entry := view.Entries[0]
	if entry.Label != "Alice" || entry.Provider != "codex" || entry.Credential != "codex · ops@example.com" ||
		entry.StatusCode != 429 || entry.ErrorType != "rate_limit" {
		t.Fatalf("entry = %+v", entry)
	}
	if view.Filters == nil || len(view.Filters.StatusCodes) != 1 || view.Filters.StatusCodes[0] != 429 ||
		len(view.Filters.ErrorTypes) != 1 || view.Filters.ErrorTypes[0] != "rate_limit" {
		t.Fatalf("filters = %+v", view.Filters)
	}
}

func TestEveryFailedRequestHasAnErrorEventEvenWithoutDetails(t *testing.T) {
	database, _ := requestEventDatabase(t)
	view, err := database.RequestErrors(billing.RequestErrorQuery{Limit: 10}, eventStart.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if view.Total != 2 || len(view.Entries) != 2 {
		t.Fatalf("errors = %+v, want one error event for each failed request", view)
	}
	for _, entry := range view.Entries {
		if entry.StatusCode != 0 || entry.ErrorType != "" || entry.Body != "" {
			t.Fatalf("empty failure details were invented: %+v", entry)
		}
	}
	failed := true
	requests := mustQueryRequestEvents(t, database, billing.RequestEventQuery{Failed: &failed, Limit: 10})
	if requests.Total != 2 {
		t.Fatalf("failed request events = %+v", requests)
	}
}

func TestRequestErrorsPaginationExcludesLateCompletions(t *testing.T) {
	database, state := requestEventDatabase(t)
	query := billing.RequestErrorQuery{Scope: "scope-b", Limit: 1, To: eventStart.Add(time.Hour)}
	first, err := database.RequestErrors(query, eventStart.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	mustSave(t, database, state, billing.Changes{RequestErrorEvents: []billing.RequestErrorEvent{{
		Event: requestEvent("scope-b", eventStart.Add(5*time.Minute+30*time.Second)),
		Error: billing.RequestError{ErrorType: "late-error"},
	}}})
	query.SnapshotID, query.Offset, query.IncludeFilters = &first.SnapshotID, 1, true
	second, err := database.RequestErrors(query, eventStart.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if second.Total != 2 || len(second.Entries) != 1 || !second.Entries[0].At.Equal(eventStart.Add(4*time.Minute)) ||
		!maps.Equal(second.ErrorTypeCounts, first.ErrorTypeCounts) || len(second.Filters.ErrorTypes) != 0 {
		t.Fatalf("late completion changed the snapshot: %+v", second)
	}
	query.SnapshotID, query.Offset = nil, 0
	fresh, err := database.RequestErrors(query, eventStart.Add(-time.Hour))
	if err != nil || fresh.Total != 3 || fresh.Entries[0].ErrorType != "late-error" {
		t.Fatalf("refresh = %+v, error = %v", fresh, err)
	}
}

func TestRequestErrorTypeCountsIgnoreTypeAndPagination(t *testing.T) {
	database := requestErrorDatabase(t)
	mustSave(t, database, billing.NewState(), billing.Changes{RequestErrorEvents: []billing.RequestErrorEvent{
		{Event: requestEvent("scope-a", eventStart)},
		{Event: requestEvent("scope-a", eventStart)},
	}})
	for _, test := range []struct {
		name  string
		query billing.RequestErrorQuery
		want  map[string]int
		total int
	}{
		{"type and pagination", billing.RequestErrorQuery{ErrorType: "rate_limit", Limit: 1, Offset: 1}, map[string]int{"": 2, "rate_limit": 1, "upstream_error": 1}, 1},
		{"account scope", billing.RequestErrorQuery{Scope: "scope-a"}, map[string]int{"": 2, "rate_limit": 1}, 3},
		{"unclassified", billing.RequestErrorQuery{ErrorTypeEmpty: true}, map[string]int{"": 2, "rate_limit": 1, "upstream_error": 1}, 2},
		{"key filter", billing.RequestErrorQuery{KeyScope: "scope-b"}, map[string]int{"upstream_error": 1}, 1},
		{"time filter", billing.RequestErrorQuery{From: eventStart.Add(2 * time.Minute)}, map[string]int{"upstream_error": 1}, 1},
		{"status filter", billing.RequestErrorQuery{StatusCode: 429}, map[string]int{"rate_limit": 1}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			view, err := database.RequestErrors(test.query, eventStart.Add(-time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(view.ErrorTypeCounts, test.want) {
				t.Fatalf("counts = %+v, want %+v", view.ErrorTypeCounts, test.want)
			}
			if view.Total != test.total || len(view.Entries) != test.total-test.query.Offset {
				t.Fatalf("view = %+v, want total %d", view, test.total)
			}
			for _, entry := range view.Entries {
				if test.query.ErrorTypeEmpty && entry.ErrorType != "" {
					t.Fatalf("classified entry in unclassified view: %+v", entry)
				}
			}
		})
	}
}

func TestRequestEventsCarryFailureBody(t *testing.T) {
	database := requestErrorDatabase(t)
	view, err := database.RequestEvents(billing.RequestEventQuery{Limit: 10}, eventStart.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Entries) != 3 || view.Entries[0].ErrorBody != `{"error":"bad gateway"}` ||
		view.Entries[1].ErrorBody != `{"error":"limited"}` || view.Entries[2].ErrorBody != "" {
		t.Fatalf("entries = %+v", view.Entries)
	}
}

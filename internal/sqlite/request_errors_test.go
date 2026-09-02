package sqlite

import (
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
	state.Credentials["auth-codex"] = billing.Credential{Provider: "codex", Account: "ops@example.com"}
	events := []billing.RequestEvent{requestEvent("scope-a", eventStart)}
	errors := []billing.RequestErrorEvent{
		{Event: requestEvent("scope-a", eventStart.Add(time.Minute)), Error: billing.RequestError{
			StatusCode: 429, ErrorType: "rate_limit", Reason: "HTTP 429", Body: `{"error":"limited"}`,
		}},
		{Event: requestEvent("scope-b", eventStart.Add(2*time.Minute)), Error: billing.RequestError{
			StatusCode: 502, ErrorType: "upstream_error", Reason: "HTTP 502", Body: `{"error":"bad gateway"}`,
		}},
	}
	mustSave(t, database, state, billing.Changes{AllKeys: true, Credentials: true, NormalRequestEvents: events, RequestErrorEvents: errors})
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
	if entry.Label != "Alice" || entry.Provider != "codex" || entry.Source != "codex · ops@example.com" ||
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
		if entry.StatusCode != 0 || entry.ErrorType != "" || entry.Reason != "" || entry.Body != "" {
			t.Fatalf("empty failure details were invented: %+v", entry)
		}
	}
	requests := mustQueryRequestEvents(t, database, billing.RequestEventQuery{Status: billing.RequestEventStatusFailed, Limit: 10})
	if requests.Total != 2 {
		t.Fatalf("failed request events = %+v", requests)
	}
}

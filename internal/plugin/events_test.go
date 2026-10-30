package plugin

import (
	"cpa-key-billing/internal/billing"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestRequestEventQueryReachesTheStore(t *testing.T) {
	app := newConfiguredApp(t)
	const apiKey = "sk-paged-000000000001"
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{apiKey}}, http.StatusOK, nil)
	for i := 0; i < 3; i++ {
		billOneRequest(t, app, apiKey, int64(100*(i+1)))
	}

	var events billing.RequestEventView
	callOK(t, app, http.MethodGet, routeEvents, url.Values{"offset": {"2"}, "limit": {"2"}}, nil, http.StatusOK, &events)
	if len(events.Entries) != 1 || events.Total != 3 || events.Statuses.Normal != 3 || events.Filters != nil {
		t.Fatalf("events = %d entries, total %d, statuses %+v", len(events.Entries), events.Total, events.Statuses)
	}
	from := events.Entries[0].At.Add(-time.Second).Format(time.RFC3339Nano)
	to := app.store.Now().Add(time.Second).Format(time.RFC3339Nano)
	sourceToken := sourceFilterToken("", events.Entries[0].Source)
	callOK(t, app, http.MethodGet, routeEvents, url.Values{
		"api_key": {billing.CallerScope(apiKey)}, "model": {"gpt-5.5"},
		"source": {sourceToken},
		"failed": {"false"}, "from": {from}, "to": {to},
	}, nil, http.StatusOK, &events)
	if events.Total != 3 || events.Filters == nil || len(events.Filters.SourceOptions) != 1 ||
		events.Filters.SourceOptions[0].Value != sourceToken {
		t.Fatalf("field and time filtered events = %+v", events)
	}

	for _, query := range []url.Values{
		{"failed": {"unknown"}}, {"offset": {"-1"}}, {"limit": {"0"}},
		{"snapshot_id": {"-1"}}, {"snapshot_id": {"9223372036854775808"}},
		{"limit": {"1001"}}, {"limit": {"one page"}}, {"from": {"yesterday"}},
		{"from": {"2026-09-01T02:00:00Z"}, "to": {"2026-09-01T01:00:00Z"}},
	} {
		if resp := callManagement(t, app, http.MethodGet, routeEvents, query, nil); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%v status = %d, want 400 (body=%s)", query, resp.StatusCode, resp.Body)
		}
	}
}

func TestRequestEventCleanupDeletesAssociatedErrors(t *testing.T) {
	app := newConfiguredApp(t)
	now := app.store.Now()
	for i := range 3 {
		event := billing.UsageEvent{At: now.Add(time.Duration(i-3) * 24 * time.Hour), Scope: "scope-cleanup", UpstreamModel: "gpt-5.5"}
		app.store.RecordUsageError(event, billing.RequestError{StatusCode: 502})
	}
	for _, body := range []any{
		map[string]any{"older_than_days": -1}, map[string]any{"older_than_days": 1.5},
		map[string]any{"older_than_days": 106752}, map[string]any{"older_than_day": 1},
	} {
		if response := callManagement(t, app, http.MethodDelete, routeEvents, nil, body); response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid cleanup body %v: %d", body, response.StatusCode)
		}
	}
	var result struct {
		Cleared int `json:"cleared"`
	}
	callOK(t, app, http.MethodDelete, routeEvents, nil, map[string]any{"older_than_days": 2}, http.StatusOK, &result)
	if result.Cleared != 2 {
		t.Fatalf("cleared = %d, want only events older than two days", result.Cleared)
	}
	var requests billing.RequestEventView
	var failures billing.RequestErrorView
	callOK(t, app, http.MethodGet, routeEvents, nil, nil, http.StatusOK, &requests)
	callOK(t, app, http.MethodGet, routeErrors, nil, nil, http.StatusOK, &failures)
	if requests.Total != 1 || failures.Total != 1 {
		t.Fatalf("requests = %d, errors = %d", requests.Total, failures.Total)
	}
	callOK(t, app, http.MethodDelete, routeEvents, nil, map[string]any{"older_than_days": 0}, http.StatusOK, &result)
	if result.Cleared != 1 {
		t.Fatalf("cleared all = %d", result.Cleared)
	}
	callOK(t, app, http.MethodGet, routeErrors, nil, nil, http.StatusOK, &failures)
	if failures.Total != 0 {
		t.Fatalf("orphaned error events = %d", failures.Total)
	}
	resource := app.routeResource(ManagementRequest{Method: http.MethodDelete, Path: resourceBase + routeEvents, Headers: http.Header{"Authorization": {"Bearer sk-demo-cleanup"}}}, routeEvents)
	if resource.StatusCode != http.StatusNotFound {
		t.Fatalf("API key cleanup allowed: %d", resource.StatusCode)
	}
}

func TestRequestEventQueryUsesABoundedDefaultPage(t *testing.T) {
	app := newConfiguredApp(t)
	for range defaultEventPageSize + 1 {
		billOneRequest(t, app, testAPIKey, 1)
	}

	var events billing.RequestEventView
	callOK(t, app, http.MethodGet, routeEvents, nil, nil, http.StatusOK, &events)
	if len(events.Entries) != defaultEventPageSize || events.Total != defaultEventPageSize+1 {
		t.Fatalf("events = %d entries of %d, want the default page of %d", len(events.Entries), events.Total, defaultEventPageSize)
	}
}

func TestEventKeyOptionsIgnorePageFilters(t *testing.T) {
	app := newConfiguredApp(t)
	const active, deleted, idle = "sk-demo-active-0001", "sk-demo-deleted-0002", "sk-demo-idle-0003"
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{active, deleted, idle}}, http.StatusOK, nil)
	billOneRequest(t, app, active, 100)
	billOneRequest(t, app, deleted, 100)
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{active, idle}}, http.StatusOK, nil)
	var keys []billing.EventKey
	callOK(t, app, http.MethodGet, routeEventKeys, url.Values{
		"api_key": {billing.CallerScope(active)}, "model": {"missing"}, "error_type": {"missing"},
	}, nil, http.StatusOK, &keys)
	if len(keys) != 2 {
		t.Fatalf("event keys = %+v", keys)
	}
	for _, key := range keys {
		if key.Scope == billing.CallerScope(deleted) && key.DeletedAt.IsZero() {
			t.Fatal("deleted identity lost its status")
		}
	}
	callOK(t, app, http.MethodGet, routeEventKeys, url.Values{
		"from": {"2026-09-02T00:00:00Z"}, "to": {"2026-09-01T00:00:00Z"},
	}, nil, http.StatusBadRequest, nil)
}

func TestUnassignedKeyFilter(t *testing.T) {
	app := newConfiguredApp(t)
	const apiKey = "sk-demo-keyed-0001"
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{apiKey}}, http.StatusOK, nil)
	billOneRequest(t, app, apiKey, 100)
	now := app.store.Now()
	publishUsageRecord(t, app, UsageRecord{Provider: "openai", Model: "gpt-5.5", Generate: true, RequestedAt: now, Failed: true})
	from, to := now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)
	filter := url.Values{"api_key_empty": {"true"}, "from": {from}, "to": {to}}
	var events billing.RequestEventView
	var failures billing.RequestErrorView
	var analysis billing.AnalysisView
	callOK(t, app, http.MethodGet, routeEvents, filter, nil, http.StatusOK, &events)
	callOK(t, app, http.MethodGet, routeErrors, filter, nil, http.StatusOK, &failures)
	callOK(t, app, http.MethodGet, routeAnalysis, filter, nil, http.StatusOK, &analysis)
	if events.Total != 1 || events.Entries[0].Scope != "" || failures.Total != 1 || failures.Entries[0].Scope != "" ||
		analysis.Summary.Requests != 1 || len(analysis.UsageDistribution.APIKeys) != 0 {
		t.Fatalf("events = %+v, errors = %+v, analysis = %+v", events, failures, analysis.Summary)
	}
}

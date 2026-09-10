package plugin

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func callManagement(t *testing.T, app *App, method, suffix string, query url.Values, body any) ManagementResponse {
	t.Helper()
	req := ManagementRequest{Method: method, Path: managementBase + suffix, Query: query}
	switch typed := body.(type) {
	case nil:
	case string:
		req.Body = []byte(typed)
	default:
		req.Body = mustMarshal(t, typed)
	}
	raw, errHandle := app.HandleMethod(MethodManagementHandle, mustMarshal(t, req))
	if errHandle != nil {
		t.Fatalf("management.handle %s %s error = %v", method, suffix, errHandle)
	}
	var resp ManagementResponse
	decodeResult(t, raw, &resp)
	return resp
}

func callOK(t *testing.T, app *App, method, suffix string, query url.Values, body any, wantStatus int, target any) {
	t.Helper()
	resp := callManagement(t, app, method, suffix, query, body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s status = %d, want %d (body=%s)", method, suffix, resp.StatusCode, wantStatus, resp.Body)
	}
	if contentType := resp.Headers.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("%s %s Content-Type = %q, want JSON", method, suffix, contentType)
	}
	if target != nil {
		if errUnmarshal := json.Unmarshal(resp.Body, target); errUnmarshal != nil {
			t.Fatalf("decode %s %s body: %v (raw=%s)", method, suffix, errUnmarshal, resp.Body)
		}
	}
}

func readKeys(t *testing.T, app *App) []billing.KeyView {
	t.Helper()
	var response struct {
		Keys []billing.KeyView `json:"keys"`
	}
	callOK(t, app, http.MethodGet, routeKeys, nil, nil, http.StatusOK, &response)
	return response.Keys
}

func readPrices(t *testing.T, app *App, models ...string) []billing.PriceRow {
	t.Helper()
	var prices []billing.PriceRow
	callOK(t, app, http.MethodGet, routePrices, url.Values{"model": models}, nil, http.StatusOK, &prices)
	return prices
}

func TestManagementListsDeferCredentialDiscovery(t *testing.T) {
	app := newConfiguredApp(t)
	calls := 0
	app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
		calls++
		if method != hostAuthList {
			t.Fatalf("unexpected host call %s", method)
		}
		return json.RawMessage(`{"files":[]}`), nil
	})
	for _, path := range []string{routeKeys, routePlans, routeRoutes} {
		callOK(t, app, http.MethodGet, path, nil, nil, http.StatusOK, nil)
	}
	if calls != 0 {
		t.Fatalf("table reads discovered credentials %d times", calls)
	}
	callOK(t, app, http.MethodGet, routeCredentials, nil, nil, http.StatusOK, nil)
	if calls != 1 {
		t.Fatalf("credential options made %d host calls", calls)
	}
}

func TestPricesRoundTripThroughTheManagementAPI(t *testing.T) {
	app := newConfiguredApp(t)

	models := []string{"gpt-4o", "house-model-x"}

	prices := readPrices(t, app, models...)
	if len(prices) != 2 || prices[0].ModelID != "gpt-4o" || prices[0].Source != billing.PriceSourceReference ||
		prices[1].Source != billing.PriceSourceNone {
		t.Fatalf("prices = %+v, want the reference price and an unpriced row", prices)
	}

	callOK(t, app, http.MethodPut, routePrices, nil, map[string]any{
		"model_id":          "house-model-x",
		"input_per_1m":      1.25,
		"output_per_1m":     10,
		"cache_read_per_1m": 0.125,
	}, http.StatusOK, nil)
	if prices = readPrices(t, app, models...); prices[1].InputPer1M != 1.25 ||
		prices[1].Source != billing.PriceSourceCustom {
		t.Fatalf("row = %+v, want the edit recorded as custom", prices[1])
	}

	callOK(t, app, http.MethodDelete, routePrices, url.Values{"model_id": {"house-model-x"}}, nil, http.StatusOK, nil)
	if prices = readPrices(t, app, models...); len(prices) != 2 || prices[1].InputPer1M != 0 {
		t.Fatalf("prices = %+v, want the rows kept and the edit dropped", prices)
	}
}

func TestPlansCRUDThroughTheManagementAPI(t *testing.T) {
	app := newConfiguredApp(t)
	anchor := time.Now().UTC().Truncate(time.Second).Add(30 * time.Minute)

	var created struct {
		Plan billing.Plan `json:"plan"`
	}
	callOK(t, app, http.MethodPost, routePlans, nil, map[string]any{
		"name":    "Team Monthly",
		"windows": []billing.QuotaWindow{{Name: "额度", AmountUSD: 20, PeriodSeconds: 2592000, CycleAnchorAt: anchor}},
	}, http.StatusCreated, &created)
	if created.Plan.ID != "team-monthly" || created.Plan.Windows[0].PeriodSeconds != 2592000 || !created.Plan.Windows[0].CycleAnchorAt.Equal(anchor) {
		t.Fatalf("plan = %+v", created.Plan)
	}

	if resp := callManagement(t, app, http.MethodPost, routePlans, nil, map[string]any{"id": created.Plan.ID, "windows": []billing.QuotaWindow{{Name: "额度", AmountUSD: 20, PeriodSeconds: 3600}}}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate plan status = %d", resp.StatusCode)
	}

	var patched struct {
		Plan billing.Plan `json:"plan"`
	}
	callOK(t, app, http.MethodPatch, routePlans, nil, map[string]any{
		"id":      "team-monthly",
		"windows": []billing.QuotaWindow{{ID: created.Plan.Windows[0].ID, Name: "额度", AmountUSD: 50, PeriodSeconds: 3600, CycleAnchorAt: anchor}},
	}, http.StatusOK, &patched)
	if patched.Plan.Windows[0].AmountUSD != 50 || patched.Plan.Windows[0].PeriodSeconds != 3600 || patched.Plan.Name != "Team Monthly" {
		t.Fatalf("plan = %+v", patched.Plan)
	}

	callOK(t, app, http.MethodPatch, routePlans, nil, map[string]any{"id": created.Plan.ID, "name": "Renamed"}, http.StatusOK, &patched)
	if !patched.Plan.Windows[0].CycleAnchorAt.Equal(anchor) {
		t.Fatal("name edit removed schedule")
	}
	for _, windows := range [][]billing.QuotaWindow{
		{{ID: created.Plan.Windows[0].ID, Name: "额度", AmountUSD: 50, PeriodSeconds: 3600, CycleAnchorAt: time.Now().Add(-time.Hour)}},
		{{ID: created.Plan.Windows[0].ID, Name: "额度", AmountUSD: 50, PeriodSeconds: 3600, CycleAnchorAt: anchor}, {Name: "独立", AmountUSD: 1, PeriodSeconds: 7200}},
	} {
		if resp := callManagement(t, app, http.MethodPatch, routePlans, nil, map[string]any{"id": created.Plan.ID, "windows": windows}); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid schedule status: %d", resp.StatusCode)
		}
	}
	var listed struct {
		Plans []billing.Plan `json:"plans"`
	}
	callOK(t, app, http.MethodGet, routePlans, nil, nil, http.StatusOK, &listed)
	if plans := listed.Plans; len(plans) != 1 {
		t.Fatalf("plans = %+v", plans)
	}

	callOK(t, app, http.MethodDelete, routePlans, url.Values{"id": {"team-monthly"}}, nil, http.StatusOK, nil)
	if resp := callManagement(t, app, http.MethodDelete, routePlans, url.Values{"id": {"team-monthly"}}, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", resp.StatusCode)
	}
}

func TestKeyResetAcceptsScopeList(t *testing.T) {
	for _, unified := range []bool{false, true} {
		t.Run(fmt.Sprint(unified), func(t *testing.T) {
			app := newConfiguredApp(t)
			anchor := time.Time{}
			if unified {
				anchor = time.Now().UTC().Truncate(time.Second).Add(time.Hour)
			}
			keys := []string{"sk-reset-first-000001", "sk-reset-second-00002"}
			scopes := []string{billing.CallerScope(keys[0]), billing.CallerScope(keys[1])}
			callOK(t, app, http.MethodPost, routeKeysSync, nil,
				map[string]any{"keys": keys}, http.StatusOK, nil)
			callOK(t, app, http.MethodPost, routePlans, nil, map[string]any{
				"id": "daily", "windows": []billing.QuotaWindow{{Name: "额度", AmountUSD: 10, PeriodSeconds: 86400, CycleAnchorAt: anchor}},
				"scopes": scopes,
			}, http.StatusCreated, nil)
			for _, scope := range scopes {
				if decision := app.store.Authorize(scope, time.Now()); !decision.Allowed {
					t.Fatalf("Authorize(%q) = %+v", scope, decision)
				}
			}

			var result billing.ResetResult
			callOK(t, app, http.MethodPost, routeKeysReset, nil, billing.ResetRequest{Mode: "all", Scopes: scopes}, http.StatusOK, &result)
			if result.Keys != 2 {
				t.Fatalf("reset = %d, want 2", result.Keys)
			}
			byScope := keysByScope(t, app)
			for _, scope := range scopes {
				if byScope[scope].Windows[0].Started != unified || unified && !byScope[scope].Windows[0].EndAt.Equal(anchor) {
					t.Fatalf("reset changed schedule for %q: %+v", scope, byScope[scope])
				}
			}

		})
	}
}

func TestKeyConcurrencyRoundTrips(t *testing.T) {
	app := newConfiguredApp(t)
	const apiKey = "sk-concurrency-000001"
	scope := billing.CallerScope(apiKey)
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{apiKey}}, http.StatusOK, nil)
	callOK(t, app, http.MethodPost, routeKeysConcurrency, nil, map[string]any{
		"scope": scope, "concurrency_limit": 5,
	}, http.StatusOK, nil)

	view := keysByScope(t, app)[scope]
	if view.ConcurrencyLimit != 5 || view.CurrentConcurrency != 0 {
		t.Fatalf("view = %+v, want a five-slot limit", view)
	}
	if decision := app.store.AcquireSlot(scope, "active-access-request"); !decision.Allowed {
		t.Fatalf("AcquireSlot = %+v, want an active request", decision)
	}
	if active := keysByScope(t, app)[scope].CurrentConcurrency; active != 1 {
		t.Fatalf("CurrentConcurrency = %d, want 1", active)
	}
	app.store.ReleaseSlot("active-access-request")
	if resp := callManagement(t, app, http.MethodPost, routeKeysConcurrency, nil, map[string]any{
		"scope": scope, "concurrency_limit": billing.MaxConcurrencyLimit + 1,
	}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid limit status = %d, want 400 (body=%s)", resp.StatusCode, resp.Body)
	}
}

func keysByScope(t *testing.T, app *App) map[string]billing.KeyView {
	t.Helper()
	byScope := map[string]billing.KeyView{}
	for _, key := range readKeys(t, app) {
		byScope[key.Scope] = key
	}
	return byScope
}

func TestManagementErrorsMapToStatusCodes(t *testing.T) {
	app := newConfiguredApp(t)
	cases := []struct {
		name       string
		method     string
		suffix     string
		body       any
		wantStatus int
	}{
		{"zero plan amount", http.MethodPost, routePlans, map[string]any{"id": "x", "windows": []billing.QuotaWindow{{Name: "额度", AmountUSD: 0, PeriodSeconds: 86400}}}, http.StatusBadRequest},
		{"invalid plan period", http.MethodPost, routePlans, map[string]any{"id": "x", "windows": []billing.QuotaWindow{{Name: "额度", AmountUSD: 1, PeriodSeconds: -1}}}, http.StatusBadRequest},
		{"unknown plan", http.MethodPatch, routePlans, map[string]any{"id": "ghost", "name": "Missing"}, http.StatusNotFound},
		{"bind to unknown plan", http.MethodPost, routeKeysBind, map[string]any{"scope": "abc", "plan_id": "ghost"}, http.StatusNotFound},
		{"no scope", http.MethodPost, routeKeysUnbind, map[string]any{}, http.StatusBadRequest},
		{"malformed body", http.MethodPost, routePlans, "{not json", http.StatusBadRequest},
		{"trailing body", http.MethodPost, routePlans, `{"id":"x"}{"id":"y"}`, http.StatusBadRequest},
		{"unknown field", http.MethodPost, routeKeysBind, map[string]any{"scope": "abc", "plan": "ghost"}, http.StatusBadRequest},
		{"invalid concurrency", http.MethodPost, routeKeysConcurrency, map[string]any{"scope": "abc", "concurrency_limit": -1}, http.StatusBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resp := callManagement(t, app, testCase.method, testCase.suffix, nil, testCase.body)
			if resp.StatusCode != testCase.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", resp.StatusCode, testCase.wantStatus, resp.Body)
			}
		})
	}
}

// The plaintext keys the panel pushes are hashed into caller scopes and
// dropped; what comes back must name them by mask alone.
func TestSyncKeysStoresOnlyMaskedKeys(t *testing.T) {
	app := newConfiguredApp(t)

	var result billing.SyncResult
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{
		"keys": []string{"sk-alpha-000000001", "sk-beta-0000000002"},
	}, http.StatusOK, &result)
	if result.Added != 2 {
		t.Fatalf("result = %+v", result)
	}

	keys := readKeys(t, app)
	if len(keys) != 2 {
		t.Fatalf("keys = %+v", keys)
	}
	for _, view := range keys {
		if !view.InConfig || view.Preview == "" {
			t.Fatalf("view = %+v, want it marked as present in the config with a preview", view)
		}
		if strings.Contains(view.Preview, "alpha") || strings.Contains(view.Preview, "beta") {
			t.Fatalf("Preview = %q leaks the key body", view.Preview)
		}
	}

	// Clearing the synchronized list requires allow_empty.
	if resp := callManagement(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", resp.StatusCode, resp.Body)
	}
	callOK(t, app, http.MethodPost, routeKeysSync, nil,
		map[string]any{"keys": []string{}, "allow_empty": true}, http.StatusOK, &result)
	if result.Deleted != 2 {
		t.Fatalf("empty authoritative sync = %+v, want both configured keys deleted", result)
	}
}

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
	callOK(t, app, http.MethodGet, routeEvents, url.Values{
		"api_key": {billing.CallerScope(apiKey)}, "model": {"gpt-5.5"}, "source": {events.Entries[0].Source},
		"failed": {"false"}, "from": {from}, "to": {to},
	}, nil, http.StatusOK, &events)
	if events.Total != 3 || events.Filters == nil {
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

func TestManagementAnalysisOmitsTheSelectedKeyWindow(t *testing.T) {
	app := newConfiguredApp(t)
	const apiKey = "sk-analysis-0000000001"
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{apiKey}}, http.StatusOK, nil)
	billOneRequest(t, app, apiKey, 20)

	var view billing.AnalysisView
	callOK(t, app, http.MethodGet, routeAnalysis, url.Values{
		"api_key": {billing.CallerScope(apiKey)},
	}, nil, http.StatusOK, &view)
	if len(view.UsageDistribution.APIKeys) != 0 || len(view.UsageDistribution.Models) != 1 ||
		view.UsageDistribution.Models[0].Requests != 1 {
		t.Fatalf("analysis = %+v", view)
	}
	response := callManagement(t, app, http.MethodGet, routeAnalysis, url.Values{
		"from": {"2026-09-01T02:00:00Z"}, "to": {"2026-09-01T01:00:00Z"},
	}, nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid analysis range status = %d, want 400", response.StatusCode)
	}
	response = callManagement(t, app, http.MethodGet, routeAnalysis, url.Values{
		"timezone": {"not/a-timezone"},
	}, nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid timezone status = %d, want 400", response.StatusCode)
	}
}

func TestManagementRoutesWorkWhileDisabled(t *testing.T) {
	app := newAppWithPrice(t, false)
	if prices := readPrices(t, app); len(prices) != 1 {
		t.Fatalf("prices = %+v", prices)
	}
	callOK(t, app, http.MethodPost, routePlans, nil, map[string]any{
		"id": "daily", "windows": []billing.QuotaWindow{{Name: "额度", AmountUSD: 1, PeriodSeconds: 86400}},
	}, http.StatusCreated, nil)
}

func TestRoutesRoundTripThroughTheManagementAPI(t *testing.T) {
	app := newConfiguredApp(t)
	const apiKey = "sk-models-first-00001"
	scope := billing.CallerScope(apiKey)
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{apiKey}}, http.StatusOK, nil)

	var created struct {
		Route billing.Route `json:"route"`
	}
	callOK(t, app, http.MethodPost, routeRoutes, nil, map[string]any{
		"name": "Fast models", "rule": map[string]any{"models": []string{"gpt-4o", "chat/fast"}, "credential_ids": []string{}, "credential_providers": []any{}},
	}, http.StatusCreated, &created)
	if created.Route.ID != "fast-models" || len(created.Route.Rule.Models) != 2 {
		t.Fatalf("route = %+v", created.Route)
	}

	if key := keysByScope(t, app)[scope]; len(key.RouteBindings.RouteIDs)+len(key.RouteBindings.Models)+len(key.RouteBindings.CredentialIDs)+len(key.RouteBindings.CredentialProviders) != 0 {
		t.Fatalf("key = %+v, want it unrestricted to begin with", key)
	}

	callOK(t, app, http.MethodPut, routeKeysRoutes, nil, map[string]any{
		"scope": scope, "bindings": map[string]any{"route_ids": []string{"fast-models"}, "models": []string{"claude-sonnet-4-5"}, "credential_ids": []string{}, "credential_providers": []map[string]string{{"source": "auth-files", "provider": "codex"}}},
	}, http.StatusOK, nil)
	key := keysByScope(t, app)[scope]
	if len(key.RouteBindings.RouteIDs) != 1 || len(key.RouteBindings.Models) != 1 || !slices.Contains(key.RouteBindings.CredentialProviders, billing.CredentialProviderSelector{Source: "auth-files", Provider: "codex"}) {
		t.Fatalf("key = %+v, want the selection recorded", key)
	}

	callOK(t, app, http.MethodPut, routeKeysRoutes, nil, map[string]any{
		"scope": scope, "bindings": map[string]any{},
	}, http.StatusOK, nil)
	if key = keysByScope(t, app)[scope]; len(key.RouteBindings.RouteIDs)+len(key.RouteBindings.Models)+len(key.RouteBindings.CredentialIDs)+len(key.RouteBindings.CredentialProviders) != 0 {
		t.Fatalf("key = %+v, want empty bindings to clear the restrictions", key)
	}

	name := "Renamed"
	callOK(t, app, http.MethodPatch, routeRoutes, nil, map[string]any{
		"id": "fast-models", "name": name,
	}, http.StatusOK, nil)
	var listed struct {
		Routes []routeRow `json:"routes"`
	}
	callOK(t, app, http.MethodGet, routeRoutes, nil, nil, http.StatusOK, &listed)
	routes := listed.Routes
	if len(routes) != 1 || routes[0].Name != name || len(routes[0].Rule.Models) != 2 {
		t.Fatalf("routes = %+v, want only the name changed", routes)
	}

	callOK(t, app, http.MethodDelete, routeRoutes, url.Values{"id": {"fast-models"}}, nil, http.StatusOK, nil)
	if resp := callManagement(t, app, http.MethodDelete, routeRoutes, url.Values{"id": {"fast-models"}}, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", resp.StatusCode)
	}
}

func TestRouteManagementWritesKeyMembershipAtomically(t *testing.T) {
	app := newConfiguredApp(t)
	keys := []string{"sk-route-member-a-0001", "sk-route-member-b-0002"}
	scopeA, scopeB := billing.CallerScope(keys[0]), billing.CallerScope(keys[1])
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": keys}, http.StatusOK, nil)
	var created struct {
		Route billing.Route `json:"route"`
	}
	callOK(t, app, http.MethodPost, routeRoutes, nil, map[string]any{
		"name": "Codex", "rule": map[string]any{"models": []string{"gpt-5.6-sol"}}, "scopes": []string{scopeA},
	}, http.StatusCreated, &created)
	if !slices.Contains(keysByScope(t, app)[scopeA].RouteBindings.RouteIDs, created.Route.ID) {
		t.Fatal("create did not bind selected API Key")
	}
	callOK(t, app, http.MethodPut, routeKeysRoutes, nil, map[string]any{
		"scope": scopeB, "bindings": billing.RouteBindings{RouteRule: billing.RouteRule{Models: []string{"gpt-5.5"}}},
	}, http.StatusOK, nil)
	callOK(t, app, http.MethodPatch, routeRoutes, nil, map[string]any{
		"id": created.Route.ID, "scopes": []string{scopeB},
	}, http.StatusOK, nil)
	views := keysByScope(t, app)
	if slices.Contains(views[scopeA].RouteBindings.RouteIDs, created.Route.ID) {
		t.Fatal("update retained unchecked API Key")
	}
	if !slices.Contains(views[scopeB].RouteBindings.RouteIDs, created.Route.ID) || !slices.Contains(views[scopeB].RouteBindings.Models, "gpt-5.5") {
		t.Fatalf("update replaced unrelated bindings: %+v", views[scopeB].RouteBindings)
	}
}

func TestRouteMutationRefreshesInventoryAndNeverReturnsRawCredentialID(t *testing.T) {
	for _, field := range []string{"credential_ids", "denied_credential_ids"} {
		t.Run(field, func(t *testing.T) {
			app := newConfiguredApp(t)
			const rawID = "raw-upstream-auth-secret"
			app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
				if method != hostAuthList {
					t.Fatalf("host method=%q", method)
				}
				return json.RawMessage(`{"files":[{"id":"raw-upstream-auth-secret","provider":"codex","source":"file","path":"/auth/codex.json","name":"codex.json"}]}`), nil
			})
			ref := billing.CredentialFingerprint(rawID)
			response := callManagement(t, app, http.MethodPost, routeRoutes, nil, map[string]any{
				"name": "Exact credential",
				"rule": map[string]any{"models": []string{}, field: []string{ref}, "credential_providers": []any{}},
			})
			if response.StatusCode != http.StatusCreated {
				t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
			}
			if strings.Contains(string(response.Body), rawID) {
				t.Fatalf("route response leaked raw credential ID: %s", response.Body)
			}
			unknown := billing.CredentialFingerprint("unknown")
			response = callManagement(t, app, http.MethodPost, routeRoutes, nil, map[string]any{
				"name": "Missing credential",
				"rule": map[string]any{"models": []string{}, field: []string{unknown}, "credential_providers": []any{}},
			})
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("unknown credential status=%d body=%s", response.StatusCode, response.Body)
			}
			const key = "sk-route-case-check-0001"
			callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{key}}, http.StatusOK, nil)
			callOK(t, app, http.MethodPut, routeKeysRoutes, nil, map[string]any{
				"scope":    billing.CallerScope(key),
				"bindings": map[string]any{field: []string{ref}},
			}, http.StatusOK, nil)
			response = callManagement(t, app, http.MethodPut, routeKeysRoutes, nil, map[string]any{
				"scope":    billing.CallerScope(key),
				"bindings": map[string]any{field: []string{unknown}},
			})
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("normalized unknown credential status=%d body=%s", response.StatusCode, response.Body)
			}
			route := app.store.RouteViews()[0]
			if app.routeRows()[0].CredentialLabels[ref] == "" {
				t.Fatal("credential label missing")
			}
			app.SetHostCaller(func(string, any) (json.RawMessage, error) {
				t.Fatal("retained reference unexpectedly rediscovered")
				return nil, nil
			})
			callOK(t, app, http.MethodPatch, routeRoutes, nil, map[string]any{"id": route.ID, "rule": map[string]any{field: []string{ref}, "denied_models": []string{"gpt"}}}, http.StatusOK, nil)
			if refs := app.store.RouteViews()[0].Rule.CredentialRefs(); len(refs) != 1 || refs[0] != ref {
				t.Fatal("retired reference lost on edit")
			}
			callOK(t, app, http.MethodPut, routeKeysRoutes, nil, map[string]any{
				"scope":    billing.CallerScope(key),
				"bindings": map[string]any{field: []string{ref}, "denied_models": []string{"gpt"}},
			}, http.StatusOK, nil)
			response = callManagement(t, app, http.MethodGet, routeKeys, nil, nil)
			if !strings.Contains(string(response.Body), "denied_models") || strings.Contains(string(response.Body), key) || strings.Contains(string(response.Body), rawID) {
				t.Fatalf("unsafe or incomplete routing view: %s", response.Body)
			}
		})
	}
}

func TestConfigCredentialSyncSurvivesRestartAndRollsBack(t *testing.T) {
	app, path := newAppWithPriceAndState(t, true)
	configuration := mustMarshal(t, LifecycleRequest{ConfigYAML: []byte(fmt.Sprintf("state_file: %q\n", path))})
	const rawKey = "sk-dummy-upstream-secret-1234"
	ref := billing.CredentialFingerprint("dummy-config")
	removed := billing.CredentialFingerprint("dummy-removed")
	if _, err := app.store.SyncKeys([]string{accountTestKeyA}, false); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetKeyRoutes(billing.CallerScope(accountTestKeyA), billing.RouteBindings{RouteRule: billing.RouteRule{CredentialIDs: []string{ref}}}); err != nil {
		t.Fatal(err)
	}
	response := callManagement(t, app, http.MethodPost, routeCredentialsSync, nil, map[string]any{"credentials": []map[string]any{
		{"ref": ref, "provider": "codex", "display_name": rawKey},
		{"ref": removed, "provider": "codex", "display_name": "未配置 API Key", "disabled": true},
	}})
	if response.StatusCode != http.StatusOK || strings.Contains(string(response.Body), rawKey) {
		t.Fatalf("sync response = %+v", response)
	}
	app.observeCandidates([]SchedulerAuthCandidate{{ID: "dummy-runtime", Provider: "codex", Attributes: map[string]string{"source_backend": "config"}}})
	restart := func() {
		app.Shutdown()
		app = newTestApp(t)
		t.Cleanup(app.Shutdown)
		if err := app.configure(configuration); err != nil {
			t.Fatal(err)
		}
		app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
			if method != hostAuthList {
				t.Fatalf("host method = %q", method)
			}
			return json.RawMessage(`{"files":[{"id":"dummy-auth-file","provider":"codex","source":"file","email":"live@example.com"}]}`), nil
		})
	}
	restart()
	response = callAccount(t, app, routeRouting, accountTestKeyA, nil)
	var routing accountRoutingResponse
	if err := json.Unmarshal(response.Body, &routing); err != nil || response.StatusCode != http.StatusOK ||
		!routing.RoutingValid || len(routing.Credentials) != 1 || routing.Credentials[0].Name != billing.PreviewKey(rawKey) ||
		routing.Credentials[0].Status != "active" || len(routing.Warnings) != 0 {
		t.Fatalf("routing after restart = %+v, response = %+v, err = %v", routing, response, err)
	}
	if snapshot := app.store.ConfigCredentials(); len(snapshot) != 2 || snapshot[removed].KeyPreview != "" || !snapshot[removed].Disabled {
		t.Fatalf("persisted config snapshot = %+v", snapshot)
	}
	if inventory := app.credentialInventory(); len(inventory) != 3 {
		t.Fatalf("host files and config snapshot were not merged: %+v", inventory)
	}
	app.observeCandidates([]SchedulerAuthCandidate{{ID: "dummy-config", Provider: "codex", Status: "cooldown", Attributes: map[string]string{"source_backend": "config"}}})
	before := app.credentialInventory()
	if err := app.configure(configuration); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(app.credentialInventory(), before) {
		t.Fatal("reconfiguration replaced live credential state with the snapshot")
	}

	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("CREATE TRIGGER reject_config BEFORE INSERT ON config_credentials BEGIN SELECT RAISE(ABORT, 'dummy failure'); END"); err != nil {
		t.Fatal(err)
	}
	update := map[string]any{"credentials": []map[string]any{
		{"ref": ref, "provider": "codex", "display_name": "sk-new…5678", "disabled": true},
	}}
	if response := callManagement(t, app, http.MethodPost, routeCredentialsSync, nil, update); response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("failed sync response = %+v", response)
	}
	if !reflect.DeepEqual(app.credentialInventory(), before) || app.store.ConfigCredentials()[ref].KeyPreview != billing.PreviewKey(rawKey) {
		t.Fatal("failed sync changed the live snapshot")
	}
	var stored int
	if err := raw.QueryRow("SELECT count(*) FROM config_credentials").Scan(&stored); err != nil || stored != 2 {
		t.Fatal("failed replacement lost persisted credentials", stored, err)
	}
	var preview string
	if err := raw.QueryRow("SELECT key_preview FROM config_credentials WHERE ref = ?", ref).Scan(&preview); err != nil || preview != billing.PreviewKey(rawKey) {
		t.Fatal("failed replacement changed the persisted preview", preview, err)
	}
	if _, err := raw.Exec("DROP TRIGGER reject_config"); err != nil {
		t.Fatal(err)
	}
	callOK(t, app, http.MethodPost, routeCredentialsSync, nil, update, http.StatusOK, nil)
	restart()
	if inventory := app.credentialInventory(); len(inventory) != 1 || inventory[0].Ref != ref ||
		inventory[0].DisplayName != "sk-new…5678" || !inventory[0].Disabled || inventory[0].Status != "disabled" {
		t.Fatalf("replacement after restart = %+v", inventory)
	}
	if err := app.configure(mustMarshal(t, LifecycleRequest{ConfigYAML: testConfigYAML(t, true)})); err != nil {
		t.Fatal(err)
	}
	if len(app.credentialInventory()) != 0 || len(app.store.ConfigCredentials()) != 0 {
		t.Fatal("old config credentials survived a database switch")
	}
	if err := app.configure(configuration); err != nil {
		t.Fatal(err)
	}
	if inventory := app.credentialInventory(); len(inventory) != 1 || inventory[0].Ref != ref || !inventory[0].Disabled {
		t.Fatalf("config snapshot was not restored after switching back: %+v", inventory)
	}
	if err := app.refreshCredentialInventory(); err != nil {
		t.Fatal(err)
	}
	callOK(t, app, http.MethodPost, routeCredentialsSync, nil, map[string]any{"credentials": []any{}}, http.StatusOK, nil)
	if inventory := app.credentialInventory(); len(inventory) != 1 || inventory[0].Source != billing.CredentialSourceAuthFiles {
		t.Fatalf("empty sync did not remove only config credentials: %+v", inventory)
	}
	restart()
	if inventory := app.credentialInventory(); len(inventory) != 0 || len(app.store.ConfigCredentials()) != 0 {
		t.Fatalf("cleared credentials reappeared after restart: %+v", inventory)
	}
}

func TestPluginLogReportsStartupAndFailures(t *testing.T) {
	app := newConfiguredApp(t)

	var loaded struct {
		Entries []billing.PluginLog `json:"entries"`
	}
	callOK(t, app, http.MethodGet, routePluginLogs, nil, nil, http.StatusOK, &loaded)
	if len(loaded.Entries) != 3 || loaded.Entries[0].Level != billing.PluginLogInfo ||
		!strings.Contains(loaded.Entries[0].Message, "参考价已更新") ||
		!strings.Contains(loaded.Entries[2].Message, "已加载计费数据库") {
		t.Fatalf("plugin logs = %+v, want the loaded database reported", loaded.Entries)
	}

	if _, errHandle := app.HandleMethod(MethodPluginReconfigure, mustMarshal(t, LifecycleRequest{
		ConfigYAML: []byte("enabled: [not, a, boolean]\n"),
	})); errHandle == nil {
		t.Fatal("plugin.reconfigure accepted a malformed config")
	}
	callOK(t, app, http.MethodGet, routePluginLogs, nil, nil, http.StatusOK, &loaded)
	if len(loaded.Entries) != 4 || loaded.Entries[0].Level != billing.PluginLogError ||
		!strings.Contains(loaded.Entries[0].Message, "应用插件配置失败") {
		t.Fatalf("plugin logs = %+v, want the rejected config reported first", loaded.Entries)
	}
}

func TestPluginLogManagementPaginatesAndFiltersDebugRows(t *testing.T) {
	app := newConfiguredApp(t)
	if _, err := app.store.ClearPluginLogs(); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"first", "second", "third"} {
		app.store.AddPluginLog(billing.PluginLogDebug, "%s", message)
	}
	app.store.AddPluginLog(billing.PluginLogInfo, "info row")
	app.store.AddPluginLog(billing.PluginLogError, "error row")
	var first billing.PluginLogPage
	callOK(t, app, http.MethodGet, routePluginLogs, url.Values{"level": {"debug"}, "limit": {"2"}}, nil, http.StatusOK, &first)
	if len(first.Entries) != 2 || first.NextBeforeID == 0 || first.Entries[0].Message != "third" || first.Entries[1].Message != "second" {
		t.Fatalf("first page=%+v", first)
	}
	var second billing.PluginLogPage
	callOK(t, app, http.MethodGet, routePluginLogs, url.Values{"level": {"debug"}, "limit": {"2"}, "before_id": {strconv.FormatInt(first.NextBeforeID, 10)}}, nil, http.StatusOK, &second)
	if len(second.Entries) != 1 || second.NextBeforeID != 0 || second.Entries[0].Message != "first" {
		t.Fatalf("second page=%+v", second)
	}
	var info billing.PluginLogPage
	callOK(t, app, http.MethodGet, routePluginLogs, url.Values{"level": {"info"}}, nil, http.StatusOK, &info)
	if len(info.Entries) != 1 || info.Entries[0].Level != billing.PluginLogInfo {
		t.Fatalf("info page=%+v", info)
	}
	for _, page := range []billing.PluginLogPage{first, second, info} {
		if page.LevelCounts[billing.PluginLogDebug] != 3 || page.LevelCounts[billing.PluginLogInfo] != 1 || page.LevelCounts[billing.PluginLogError] != 1 {
			t.Fatalf("counts must ignore level and pagination: %+v", page.LevelCounts)
		}
	}
	var future billing.PluginLogPage
	callOK(t, app, http.MethodGet, routePluginLogs, url.Values{"since": {app.store.Now().Add(time.Hour).Format(time.RFC3339)}}, nil, http.StatusOK, &future)
	if len(future.Entries) != 0 || len(future.LevelCounts) != 0 {
		t.Fatalf("counts must respect time range: %+v", future)
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

func TestManagementWriteFailureReturnsError(t *testing.T) {
	app, path := newAppWithPriceAndState(t, true)
	const apiKey = "sk-storage-test-0001"
	scope := billing.CallerScope(apiKey)
	if _, err := app.store.SyncKeys([]string{apiKey}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.CreatePlanWithBindings(billing.Plan{ID: "p", Windows: []billing.QuotaWindow{{Name: "额度", AmountUSD: 10, PeriodSeconds: 3600}}}, []string{scope}); err != nil {
		t.Fatal(err)
	}
	app.store.Authorize(scope, app.store.Now())
	before, _ := app.store.KeyViewForScope(scope)
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TRIGGER reject_key_write BEFORE UPDATE ON api_keys
        BEGIN SELECT RAISE(ABORT, 'simulated write failure'); END`); err != nil {
		t.Fatal(err)
	}
	response := callManagement(t, app, http.MethodPost, routeKeysReset, nil, billing.ResetRequest{Mode: "all", Scopes: []string{scope}})
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("reset status = %d, body = %s", response.StatusCode, response.Body)
	}
	if _, err := database.Exec("DROP TRIGGER reject_key_write"); err != nil {
		t.Fatal(err)
	}
	app.Shutdown()
	cfg := billing.DefaultConfig()
	cfg.StateFile = path
	if err := app.store.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	after, _ := app.store.KeyViewForScope(scope)
	if after.PlanID != before.PlanID || !reflect.DeepEqual(after.Windows[0].Dimensions, before.Windows[0].Dimensions) {
		t.Fatalf("failed reset changed state: %+v", after)
	}
	reset, err := app.store.ResetQuota(billing.ResetRequest{Mode: "all", Scopes: []string{scope}})
	if err != nil || reset.Keys != 1 {
		t.Fatalf("cycle was reset despite write failure: reset=%+v, err=%v", reset, err)
	}
}

func TestLargeRouteBindingsSurviveReload(t *testing.T) {
	app, path := newAppWithPriceAndState(t, true)
	const apiKey = "sk-large-route-test-0001"
	scope := billing.CallerScope(apiKey)
	if _, err := app.store.SyncKeys([]string{apiKey}, false); err != nil {
		t.Fatal(err)
	}
	models := make([]string, 2048)
	for i := range models {
		models[i] = fmt.Sprintf("model-%d", i)
	}
	if err := app.store.SetKeyRoutes(scope, billing.RouteBindings{RouteRule: billing.RouteRule{Models: models}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"created", "edited"} {
		var selected []string
		if id == "created" {
			selected = []string{scope}
		}
		if _, err := app.store.CreateRoute(billing.Route{ID: id, Name: id, Rule: billing.RouteRule{Models: models}}, selected); err != nil {
			t.Fatal(err)
		}
	}
	scopes := []string{scope}
	if _, err := app.store.UpdateRoute(billing.RoutePatch{ID: "edited"}, &scopes); err != nil {
		t.Fatal(err)
	}
	app.Shutdown()
	cfg := billing.DefaultConfig()
	cfg.StateFile = path
	if err := app.store.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	view, ok := app.store.KeyViewForScope(scope)
	if !ok || len(view.RouteBindings.Models) != len(models) || len(view.RouteBindings.RouteIDs) != 2 {
		t.Fatalf("bindings were lost after reload: %+v", view.RouteBindings)
	}
	route, ok := app.store.Route("edited")
	if !ok || len(route.Rule.Models) != len(models) {
		t.Fatal("route models were lost after reload")
	}
}

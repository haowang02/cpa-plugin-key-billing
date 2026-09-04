package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
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

func readAccess(t *testing.T, app *App) accessResponse {
	t.Helper()
	var access accessResponse
	callOK(t, app, http.MethodGet, routeAccess, nil, nil, http.StatusOK, &access)
	return access
}

func readPrices(t *testing.T, app *App) []billing.PriceRow {
	t.Helper()
	var prices []billing.PriceRow
	callOK(t, app, http.MethodGet, routePrices, nil, nil, http.StatusOK, &prices)
	return prices
}

func TestPricesRoundTripThroughTheManagementAPI(t *testing.T) {
	app := newConfiguredApp(t)

	var synced billing.PriceCatalogSyncResult
	callOK(t, app, http.MethodPost, routePricesSync, nil, map[string]any{
		"models": []string{"gpt-4o", "house-model-x"},
	}, http.StatusOK, &synced)
	if synced.Added != 2 || synced.Priced != 1 {
		t.Fatalf("result = %+v, want two rows with one priced from the catalog", synced)
	}

	prices := readPrices(t, app)
	if len(prices) != 2 || prices[0].Pattern != "gpt-4o" || prices[0].Source != billing.PriceSourceBuiltin ||
		prices[1].Source != billing.PriceSourceNone {
		t.Fatalf("prices = %+v, want the catalog price and an unpriced row", prices)
	}

	callOK(t, app, http.MethodPut, routePrices, nil, map[string]any{
		"pattern":           "house-model-x",
		"input_per_1m":      1.25,
		"output_per_1m":     10,
		"cache_read_per_1m": 0.125,
	}, http.StatusOK, nil)
	if prices = readPrices(t, app); prices[1].InputPer1M != 1.25 ||
		prices[1].Source != billing.PriceSourceCustom {
		t.Fatalf("row = %+v, want the edit recorded as custom", prices[1])
	}

	var reset struct {
		Restored int `json:"restored"`
	}
	callOK(t, app, http.MethodPost, routePricesReset, nil, nil, http.StatusOK, &reset)
	if reset.Restored != 1 {
		t.Fatalf("restored = %d, want 1", reset.Restored)
	}
	if prices = readPrices(t, app); len(prices) != 2 || prices[1].InputPer1M != 0 {
		t.Fatalf("prices = %+v, want the rows kept and the edit dropped", prices)
	}

	if resp := callManagement(t, app, http.MethodPost, routePricesSync, nil, map[string]any{"models": []string{}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", resp.StatusCode, resp.Body)
	}
}

func TestPlansCRUDThroughTheManagementAPI(t *testing.T) {
	app := newConfiguredApp(t)

	var created struct {
		Plan billing.Plan `json:"plan"`
	}
	callOK(t, app, http.MethodPost, routePlans, nil, map[string]any{
		"name":           "Team Monthly",
		"amount_usd":     20,
		"period_seconds": 2592000,
	}, http.StatusCreated, &created)
	if created.Plan.ID != "team-monthly" || created.Plan.PeriodSeconds != 2592000 {
		t.Fatalf("plan = %+v", created.Plan)
	}

	var patched struct {
		Plan billing.Plan `json:"plan"`
	}
	callOK(t, app, http.MethodPatch, routePlans, nil, map[string]any{
		"id":             "team-monthly",
		"amount_usd":     50,
		"period_seconds": 0,
	}, http.StatusOK, &patched)
	if patched.Plan.AmountUSD != 50 || patched.Plan.PeriodSeconds != 0 || patched.Plan.Name != "Team Monthly" {
		t.Fatalf("plan = %+v", patched.Plan)
	}

	if plans := readAccess(t, app).Plans; len(plans) != 1 {
		t.Fatalf("plans = %+v", plans)
	}

	callOK(t, app, http.MethodDelete, routePlans, url.Values{"id": {"team-monthly"}}, nil, http.StatusOK, nil)
	if resp := callManagement(t, app, http.MethodDelete, routePlans, url.Values{"id": {"team-monthly"}}, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", resp.StatusCode)
	}
}

func TestPlanBindingsRoundTripThroughTheManagementAPI(t *testing.T) {
	app := newConfiguredApp(t)
	const firstKey = "sk-plan-first-000001"
	const secondKey = "sk-plan-second-00002"
	firstScope := billing.CallerScope(firstKey)
	secondScope := billing.CallerScope(secondKey)
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{firstKey, secondKey}}, http.StatusOK, nil)

	callOK(t, app, http.MethodPost, routePlans, nil, map[string]any{
		"id": "team", "name": "Team", "amount_usd": 10,
		"period_seconds": 0, "scopes": []string{firstScope},
	}, http.StatusCreated, nil)
	byScope := keysByScope(t, app)
	if len(byScope) != 2 || byScope[firstScope].PlanID != "team" || byScope[secondScope].PlanID != "" {
		t.Fatalf("keys after create = %+v", byScope)
	}

	callOK(t, app, http.MethodPatch, routePlans, nil, map[string]any{
		"id": "team", "scopes": []string{secondScope},
	}, http.StatusOK, nil)
	if byScope = keysByScope(t, app); byScope[firstScope].PlanID != "" ||
		byScope[secondScope].PlanID != "team" {
		t.Fatalf("keys after edit = %+v", byScope)
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
	for _, key := range readAccess(t, app).Keys {
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
		{"zero plan amount", http.MethodPost, routePlans, map[string]any{"id": "x", "amount_usd": 0, "period_seconds": 86400}, http.StatusBadRequest},
		{"invalid plan period", http.MethodPost, routePlans, map[string]any{"id": "x", "amount_usd": 1, "period_seconds": -1}, http.StatusBadRequest},
		{"unknown plan", http.MethodPatch, routePlans, map[string]any{"id": "ghost", "amount_usd": 1}, http.StatusNotFound},
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

func TestDuplicatePlanReportsConflict(t *testing.T) {
	app := newConfiguredApp(t)
	body := map[string]any{"id": "daily", "name": "Daily", "amount_usd": 1, "period_seconds": 86400}
	callOK(t, app, http.MethodPost, routePlans, nil, body, http.StatusCreated, nil)
	if resp := callManagement(t, app, http.MethodPost, routePlans, nil, body); resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", resp.StatusCode, resp.Body)
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

	keys := readAccess(t, app).Keys
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

	// An empty push is refused, so a failed fetch in the browser cannot be
	// mistaken for "every key was deleted".
	if resp := callManagement(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", resp.StatusCode, resp.Body)
	}
	callOK(t, app, http.MethodPost, routeKeysSync, nil,
		map[string]any{"keys": []string{}, "allow_empty": true}, http.StatusOK, &result)
	if result.Removed != 2 {
		t.Fatalf("empty authoritative sync = %+v, want both configured keys removed", result)
	}
}

func TestSyncRetiresKeysDeletedFromCPA(t *testing.T) {
	app := newConfiguredApp(t)
	const kept, removed = "sk-kept-00000000001", "sk-removed-00000001"

	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{kept, removed}}, http.StatusOK, nil)
	billOneRequest(t, app, removed, 1000)

	var result billing.SyncResult
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{kept}}, http.StatusOK, &result)
	if result.Removed != 1 || result.Added != 0 {
		t.Fatalf("result = %+v", result)
	}

	keys := readAccess(t, app).Keys
	if len(keys) != 2 {
		t.Fatalf("keys = %+v, want the deleted key kept alongside the live one", keys)
	}
	for _, view := range keys {
		wantDeleted := view.Scope == billing.CallerScope(removed)
		if view.DeletedAt.IsZero() == wantDeleted || view.Preview == "" {
			t.Fatalf("view = %+v, want it marked deleted=%v with its identity kept", view, wantDeleted)
		}
	}

	var events billing.RequestEventView
	callOK(t, app, http.MethodGet, routeEvents, nil, nil, http.StatusOK, &events)
	if len(events.Entries) != 1 || events.Entries[0].Preview == "" {
		t.Fatalf("events = %+v, want the deleted key's history still readable", events.Entries)
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
		"status": {"normal"}, "from": {from}, "to": {to},
	}, nil, http.StatusOK, &events)
	if events.Total != 3 || events.Filters == nil {
		t.Fatalf("field and time filtered events = %+v", events)
	}

	for _, query := range []url.Values{
		{"status": {"unknown"}}, {"offset": {"-1"}}, {"limit": {"0"}},
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

func TestManagementAnalysisOmitsTheSelectedKeyDimension(t *testing.T) {
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
		"id": "daily", "amount_usd": 1, "period_seconds": 86400,
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

	if key := keysByScope(t, app)[scope]; len(key.RouteBindings) != 0 {
		t.Fatalf("key = %+v, want it unrestricted to begin with", key)
	}

	callOK(t, app, http.MethodPut, routeKeysRoutes, nil, map[string]any{
		"scope": scope, "bindings": []map[string]string{{"kind": "route", "value": "fast-models"}, {"kind": "model", "value": "claude-sonnet-4-5"}, {"kind": "credential_provider", "value": "auth-files\x00codex"}},
	}, http.StatusOK, nil)
	key := keysByScope(t, app)[scope]
	if len(key.RouteBindings) != 3 || !slices.Contains(key.RouteBindings, billing.RouteBinding{Kind: billing.RouteBindingCredentialProvider, Value: "auth-files\x00codex"}) {
		t.Fatalf("key = %+v, want the selection recorded", key)
	}

	callOK(t, app, http.MethodPut, routeKeysRoutes, nil, map[string]any{
		"scope": scope, "bindings": []map[string]string{{"kind": "route", "value": billing.SystemAllRouteID}},
	}, http.StatusOK, nil)
	if key = keysByScope(t, app)[scope]; len(key.RouteBindings) != 0 {
		t.Fatalf("key = %+v, want the default route to clear the rest", key)
	}

	name := "Renamed"
	callOK(t, app, http.MethodPatch, routeRoutes, nil, map[string]any{
		"id": "fast-models", "name": name,
	}, http.StatusOK, nil)
	routes := readAccess(t, app).Routes
	if len(routes) != 2 || routes[1].Name != name || len(routes[1].Rule.Models) != 2 {
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
	binding := billing.RouteBinding{Kind: billing.RouteBindingRoute, Value: created.Route.ID}
	if !slices.Contains(keysByScope(t, app)[scopeA].RouteBindings, binding) {
		t.Fatal("create did not bind selected API Key")
	}
	callOK(t, app, http.MethodPut, routeKeysRoutes, nil, map[string]any{
		"scope": scopeB, "bindings": []billing.RouteBinding{{Kind: billing.RouteBindingModel, Value: "gpt-5.5"}},
	}, http.StatusOK, nil)
	callOK(t, app, http.MethodPatch, routeRoutes, nil, map[string]any{
		"id": created.Route.ID, "scopes": []string{scopeB},
	}, http.StatusOK, nil)
	views := keysByScope(t, app)
	if slices.Contains(views[scopeA].RouteBindings, binding) {
		t.Fatal("update retained unchecked API Key")
	}
	if !slices.Contains(views[scopeB].RouteBindings, binding) || !slices.Contains(views[scopeB].RouteBindings, billing.RouteBinding{Kind: billing.RouteBindingModel, Value: "gpt-5.5"}) {
		t.Fatalf("update replaced unrelated bindings: %+v", views[scopeB].RouteBindings)
	}
}

func TestRouteMutationRefreshesInventoryAndNeverReturnsRawCredentialID(t *testing.T) {
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
		"rule": map[string]any{"models": []string{}, "credential_ids": []string{ref}, "credential_providers": []any{}},
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
		"rule": map[string]any{"models": []string{}, "credential_ids": []string{unknown}, "credential_providers": []any{}},
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown credential status=%d body=%s", response.StatusCode, response.Body)
	}
	const key = "sk-route-case-check-0001"
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{key}}, http.StatusOK, nil)
	response = callManagement(t, app, http.MethodPut, routeKeysRoutes, nil, map[string]any{
		"scope":    billing.CallerScope(key),
		"bindings": []map[string]string{{"kind": "Credential", "value": unknown}},
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("normalized unknown credential status=%d body=%s", response.StatusCode, response.Body)
	}
}

func TestConfiguredCredentialSyncMakesExactAPIKeysRoutable(t *testing.T) {
	app := newConfiguredApp(t)
	const rawID = "codex:apikey:0123456789ab"
	const rawKey = "sk-dummy-upstream-secret-1234"
	var synced struct {
		Credentials []credentialView `json:"credentials"`
	}
	callOK(t, app, http.MethodPost, routeCredentialsSync, nil, map[string]any{
		"credentials": []map[string]any{{
			"ref": billing.CredentialFingerprint(rawID), "provider": "codex", "display_name": rawKey,
		}},
	}, http.StatusOK, &synced)
	ref := billing.CredentialFingerprint(rawID)
	if len(synced.Credentials) != 1 || synced.Credentials[0].Ref != ref ||
		synced.Credentials[0].DisplayName != billing.PreviewKey(rawKey) {
		t.Fatalf("credentials=%+v", synced.Credentials)
	}
	if strings.Contains(string(mustMarshal(t, synced)), rawKey) {
		t.Fatalf("sync response leaked upstream key: %+v", synced)
	}
	if !candidateAllowed(SchedulerAuthCandidate{ID: rawID, Provider: "codex"}, billing.RoutingDecision{CredentialIDs: []string{ref}}) {
		t.Fatal("synced credential did not match its scheduler candidate")
	}

	callOK(t, app, http.MethodPost, routeCredentialsSync, nil, map[string]any{"credentials": []any{}}, http.StatusOK, &synced)
	if len(synced.Credentials) != 0 {
		t.Fatalf("credentials after empty sync=%+v", synced.Credentials)
	}
}

func TestPluginLogReportsStartupAndFailures(t *testing.T) {
	app := newConfiguredApp(t)

	var loaded struct {
		Entries []billing.PluginLog `json:"entries"`
	}
	callOK(t, app, http.MethodGet, routePluginLogs, nil, nil, http.StatusOK, &loaded)
	if len(loaded.Entries) != 1 || loaded.Entries[0].Level != billing.PluginLogInfo ||
		!strings.Contains(loaded.Entries[0].Message, "已加载计费数据库") {
		t.Fatalf("plugin logs = %+v, want the loaded database reported", loaded.Entries)
	}

	if _, errHandle := app.HandleMethod(MethodPluginReconfigure, mustMarshal(t, LifecycleRequest{
		ConfigYAML: []byte("enabled: [not, a, boolean]\n"),
	})); errHandle == nil {
		t.Fatal("plugin.reconfigure accepted a malformed config")
	}
	callOK(t, app, http.MethodGet, routePluginLogs, nil, nil, http.StatusOK, &loaded)
	if len(loaded.Entries) != 2 || loaded.Entries[0].Level != billing.PluginLogError ||
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
}

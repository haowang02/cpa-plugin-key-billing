package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

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

// Everything the panel reads outside the two logs comes back from this one
// route, so it is what the round-trip tests read their edits back through.
func readOverview(t *testing.T, app *App) overviewResponse {
	t.Helper()
	var overview overviewResponse
	callOK(t, app, http.MethodGet, routeOverview, nil, nil, http.StatusOK, &overview)
	return overview
}

func TestEveryDeclaredRouteIsDispatchable(t *testing.T) {
	app := newConfiguredApp(t)
	for _, route := range managementRegistration().Routes {
		suffix := strings.TrimPrefix(route.Path, managementBase)
		resp := callManagement(t, app, route.Method, suffix, url.Values{}, nil)
		if resp.StatusCode == http.StatusNotFound {
			t.Fatalf("declared route %s %s is not dispatched (body=%s)", route.Method, route.Path, resp.Body)
		}
	}
}

func TestPricesRoundTripThroughTheManagementAPI(t *testing.T) {
	app := newConfiguredApp(t)

	var synced billing.ModelSyncResult
	callOK(t, app, http.MethodPost, routePricesSync, nil, map[string]any{
		"models": []string{"gpt-4o", "house-model-x"},
	}, http.StatusOK, &synced)
	if synced.Added != 2 || synced.Priced != 1 {
		t.Fatalf("result = %+v, want two rows with one priced from the catalog", synced)
	}

	prices := readOverview(t, app).Prices
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
	if prices = readOverview(t, app).Prices; prices[1].InputPer1M != 1.25 ||
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
	if prices = readOverview(t, app).Prices; len(prices) != 2 || prices[1].InputPer1M != 0 {
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
		"name":       "Team Monthly",
		"amount_usd": 20,
		"period":     map[string]any{"kind": "monthly"},
	}, http.StatusCreated, &created)
	if created.Plan.ID != "team-monthly" {
		t.Fatalf("plan = %+v", created.Plan)
	}

	var patched struct {
		Plan billing.Plan `json:"plan"`
	}
	callOK(t, app, http.MethodPatch, routePlans, nil, map[string]any{
		"id":         "team-monthly",
		"amount_usd": 50,
	}, http.StatusOK, &patched)
	if patched.Plan.AmountUSD != 50 || patched.Plan.Name != "Team Monthly" {
		t.Fatalf("plan = %+v, want only the amount changed", patched.Plan)
	}

	if plans := readOverview(t, app).Plans; len(plans) != 1 {
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
		"period": map[string]any{"kind": "never"}, "scopes": []string{firstScope},
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

func keysByScope(t *testing.T, app *App) map[string]billing.KeyView {
	t.Helper()
	byScope := map[string]billing.KeyView{}
	for _, key := range readOverview(t, app).Keys {
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
		{"zero plan amount", http.MethodPost, routePlans, map[string]any{"id": "x", "amount_usd": 0, "period": map[string]any{"kind": "daily"}}, http.StatusBadRequest},
		{"invalid plan period", http.MethodPost, routePlans, map[string]any{"id": "x", "amount_usd": 1, "period": map[string]any{"kind": "custom"}}, http.StatusBadRequest},
		{"unknown plan", http.MethodPatch, routePlans, map[string]any{"id": "ghost", "amount_usd": 1}, http.StatusNotFound},
		{"bind to unknown plan", http.MethodPost, routeKeysBind, map[string]any{"scope": "abc", "plan_id": "ghost"}, http.StatusNotFound},
		{"no scope", http.MethodPost, routeKeysUnbind, map[string]any{}, http.StatusBadRequest},
		{"malformed body", http.MethodPost, routePlans, "{not json", http.StatusBadRequest},
		{"unknown field", http.MethodPost, routeKeysBind, map[string]any{"scope": "abc", "plan": "ghost"}, http.StatusBadRequest},
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
	body := map[string]any{"id": "daily", "name": "Daily", "amount_usd": 1, "period": map[string]any{"kind": "daily"}}
	callOK(t, app, http.MethodPost, routePlans, nil, body, http.StatusCreated, nil)
	if resp := callManagement(t, app, http.MethodPost, routePlans, nil, body); resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", resp.StatusCode, resp.Body)
	}
}

// TestSyncAcceptsTheCPAKeyListVerbatim matters because the admin UI reads
// GET /v0/management/api-keys on the same origin and forwards that response
// straight through; requiring it to be reshaped first would be a needless
// source of bugs.
func TestSyncAcceptsTheCPAKeyListVerbatim(t *testing.T) {
	app := newConfiguredApp(t)

	var result billing.SyncResult
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{
		"api-keys": []string{"sk-alpha-000000001", "sk-beta-0000000002"},
	}, http.StatusOK, &result)
	if result.Added != 2 {
		t.Fatalf("result = %+v", result)
	}

	keys := readOverview(t, app).Keys
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

// A key deleted from CPA leaves the key list but keeps its billing history,
// which the log can only render while the record is there to name it.
func TestSyncRetiresKeysDeletedFromCPA(t *testing.T) {
	app := newConfiguredApp(t)
	const kept, removed = "sk-kept-00000000001", "sk-removed-00000001"

	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{kept, removed}}, http.StatusOK, nil)
	billOneRequest(t, app, removed, 1000)

	var result billing.SyncResult
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{kept}}, http.StatusOK, &result)
	if result.Removed != 1 || result.Matched != 1 {
		t.Fatalf("result = %+v", result)
	}

	keys := readOverview(t, app).Keys
	if len(keys) != 2 {
		t.Fatalf("keys = %+v, want the deleted key kept alongside the live one", keys)
	}
	for _, view := range keys {
		wantDeleted := view.Scope == billing.CallerScope(removed)
		if view.DeletedAt.IsZero() == wantDeleted || view.Preview == "" {
			t.Fatalf("view = %+v, want it marked deleted=%v with its identity kept", view, wantDeleted)
		}
	}

	var logs billing.LogView
	callOK(t, app, http.MethodGet, routeLogs, nil, nil, http.StatusOK, &logs)
	if len(logs.Entries) != 1 || logs.Entries[0].Preview == "" {
		t.Fatalf("logs = %+v, want the deleted key's history still readable", logs.Entries)
	}
}

// Only the query string is the handler's to get right; the paging behind it
// belongs to the store and is covered there.
func TestLogQueryReachesTheStore(t *testing.T) {
	app := newConfiguredApp(t)
	const apiKey = "sk-paged-000000000001"
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{apiKey}}, http.StatusOK, nil)
	for i := 0; i < 3; i++ {
		billOneRequest(t, app, apiKey, int64(100*(i+1)))
	}

	var logs billing.LogView
	callOK(t, app, http.MethodGet, routeLogs, url.Values{"offset": {"2"}, "limit": {"2"}}, nil, http.StatusOK, &logs)
	if len(logs.Entries) != 1 || logs.Total != 3 || logs.Outcomes.Succeeded != 3 {
		t.Fatalf("logs = %d entries, total %d, outcomes %+v", len(logs.Entries), logs.Total, logs.Outcomes)
	}
	callOK(t, app, http.MethodGet, routeLogs,
		url.Values{"q": {"gpt-5.5"}, "outcome": {"failed"}}, nil, http.StatusOK, &logs)
	if logs.Total != 0 || logs.Outcomes.All != 3 {
		t.Fatalf("logs = %+v, want the search counted and no failures shown", logs)
	}

	for _, query := range []url.Values{
		{"outcome": {"succeded"}}, {"offset": {"-1"}}, {"limit": {"one page"}},
	} {
		if resp := callManagement(t, app, http.MethodGet, routeLogs, query, nil); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%v status = %d, want 400 (body=%s)", query, resp.StatusCode, resp.Body)
		}
	}
}

// TestManagementRoutesWorkWhileDisabled matters for diagnosis: an operator who
// turned the plugin off still needs to read and fix its configuration.
func TestManagementRoutesWorkWhileDisabled(t *testing.T) {
	app := newAppWithPrice(t, false)
	if prices := readOverview(t, app).Prices; len(prices) != 1 {
		t.Fatalf("prices = %+v", prices)
	}
	callOK(t, app, http.MethodPost, routePlans, nil, map[string]any{
		"id": "daily", "amount_usd": 1, "period": map[string]any{"kind": "daily"},
	}, http.StatusCreated, nil)
}

// The plugin log is where an operator sees what the plugin itself did, which is
// otherwise invisible: a c-shared plugin has no console of its own.
func TestPluginLogReportsStartupAndFailures(t *testing.T) {
	app := newConfiguredApp(t)

	var loaded struct {
		Events []billing.Event `json:"events"`
	}
	callOK(t, app, http.MethodGet, routeEvents, nil, nil, http.StatusOK, &loaded)
	if len(loaded.Events) != 1 || loaded.Events[0].Level != billing.EventInfo ||
		!strings.Contains(loaded.Events[0].Message, "已加载状态文件") {
		t.Fatalf("events = %+v, want the loaded state file reported", loaded.Events)
	}

	if _, errHandle := app.HandleMethod(MethodPluginReconfigure, mustMarshal(t, LifecycleRequest{
		ConfigYAML: []byte("enabled: [not, a, boolean]\n"),
	})); errHandle == nil {
		t.Fatal("plugin.reconfigure accepted a malformed config")
	}
	callOK(t, app, http.MethodGet, routeEvents, nil, nil, http.StatusOK, &loaded)
	if len(loaded.Events) != 2 || loaded.Events[0].Level != billing.EventError ||
		!strings.Contains(loaded.Events[0].Message, "应用插件配置失败") {
		t.Fatalf("events = %+v, want the rejected config reported first", loaded.Events)
	}
}

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

// TestEveryDeclaredRouteIsDispatchable is the guard against a route that is
// advertised to the host but falls through to the 404 branch, which would only
// surface once an operator clicked it.
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

	var table billing.PriceTable
	callOK(t, app, http.MethodGet, routePrices, nil, nil, http.StatusOK, &table)
	if len(table.Models) != 2 || table.CatalogVersion == "" {
		t.Fatalf("table = %+v", table)
	}
	if table.Models[0].Pattern != "gpt-4o" || table.Models[0].Source != billing.PriceSourceBuiltin {
		t.Fatalf("row = %+v, want the catalog price", table.Models[0])
	}
	if table.Models[1].Source != billing.PriceSourceNone {
		t.Fatalf("row = %+v, want an unpriced row", table.Models[1])
	}

	callOK(t, app, http.MethodPut, routePrices, nil, map[string]any{
		"pattern":           "house-model-x",
		"input_per_1m":      1.25,
		"output_per_1m":     10,
		"cache_read_per_1m": 0.125,
	}, http.StatusOK, nil)
	callOK(t, app, http.MethodGet, routePrices, nil, nil, http.StatusOK, &table)
	if table.Models[1].InputPer1M != 1.25 || table.Models[1].Source != billing.PriceSourceCustom {
		t.Fatalf("row = %+v, want the edit recorded as custom", table.Models[1])
	}

	var reset struct {
		Restored int `json:"restored"`
	}
	callOK(t, app, http.MethodPost, routePricesReset, nil, nil, http.StatusOK, &reset)
	if reset.Restored != 1 {
		t.Fatalf("restored = %d, want 1", reset.Restored)
	}
	callOK(t, app, http.MethodGet, routePrices, nil, nil, http.StatusOK, &table)
	if len(table.Models) != 2 || table.Models[1].InputPer1M != 0 {
		t.Fatalf("models = %+v, want the rows kept and the edit dropped", table.Models)
	}

	if resp := callManagement(t, app, http.MethodPost, routePricesSync, nil, map[string]any{"models": []string{}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", resp.StatusCode, resp.Body)
	}
}

func TestPriceCatalogSearchThroughTheManagementAPI(t *testing.T) {
	app := newConfiguredApp(t)
	var result struct {
		Models []billing.PriceRule `json:"models"`
	}
	callOK(t, app, http.MethodGet, routePriceCatalog, url.Values{
		"q":     {"gpt-4o"},
		"limit": {"5"},
	}, nil, http.StatusOK, &result)
	if len(result.Models) == 0 || len(result.Models) > 5 || result.Models[0].Pattern != "gpt-4o" {
		t.Fatalf("models = %+v", result.Models)
	}

	resp := callManagement(t, app, http.MethodGet, routePriceCatalog, url.Values{"limit": {"500"}}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid limit status = %d, want 400", resp.StatusCode)
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

	var listed struct {
		Plans []billing.PlanView `json:"plans"`
	}
	callOK(t, app, http.MethodGet, routePlans, nil, nil, http.StatusOK, &listed)
	if len(listed.Plans) != 1 || listed.Plans[0].BoundKeys != 0 {
		t.Fatalf("plans = %+v", listed.Plans)
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
	var directory billing.KeyDirectory
	callOK(t, app, http.MethodGet, routeKeys, nil, nil, http.StatusOK, &directory)
	if len(directory.Keys) != 2 {
		t.Fatalf("keys = %+v", directory.Keys)
	}
	byScope := map[string]billing.KeyView{}
	for _, key := range directory.Keys {
		byScope[key.Scope] = key
	}
	if byScope[firstScope].PlanID != "team" || !byScope[firstScope].CycleStartAt.IsZero() || byScope[secondScope].PlanID != "" {
		t.Fatalf("keys after create = %+v", byScope)
	}

	callOK(t, app, http.MethodPatch, routePlans, nil, map[string]any{
		"id": "team", "scopes": []string{secondScope},
	}, http.StatusOK, nil)
	directory = billing.KeyDirectory{}
	callOK(t, app, http.MethodGet, routeKeys, nil, nil, http.StatusOK, &directory)
	byScope = map[string]billing.KeyView{}
	for _, key := range directory.Keys {
		byScope[key.Scope] = key
	}
	if byScope[firstScope].PlanID != "" || byScope[secondScope].PlanID != "team" || !byScope[secondScope].CycleStartAt.IsZero() {
		t.Fatalf("keys after edit = %+v", byScope)
	}
}

func TestLogClearThroughTheManagementAPI(t *testing.T) {
	app := newConfiguredApp(t)
	now := app.store.Now()
	app.store.Update(func(state *billing.State) {
		state.Log = []billing.LogEntry{{Scope: "scope-a", At: now}}
	})

	var cleared struct {
		Cleared int `json:"cleared"`
	}
	callOK(t, app, http.MethodDelete, routeLogs, nil, nil, http.StatusOK, &cleared)
	if cleared.Cleared != 1 || len(app.store.Logs(0).Entries) != 0 {
		t.Fatalf("cleared = %+v logs=%+v", cleared, app.store.Logs(0))
	}
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
	if result.Added != 2 || result.Received != 2 {
		t.Fatalf("result = %+v", result)
	}

	var directory billing.KeyDirectory
	callOK(t, app, http.MethodGet, routeKeys, nil, nil, http.StatusOK, &directory)
	if len(directory.Keys) != 2 {
		t.Fatalf("keys = %+v", directory.Keys)
	}
	for _, view := range directory.Keys {
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

func TestAdminAPIDrivesEnforcementEndToEnd(t *testing.T) {
	app := newConfiguredApp(t)
	const apiKey = "sk-billing-test-000001"
	scope := billing.CallerScope(apiKey)

	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{apiKey}}, http.StatusOK, nil)
	callOK(t, app, http.MethodPut, routePrices, nil, map[string]any{
		"pattern": "gpt-5.5", "input_per_1m": 1, "output_per_1m": 2,
	}, http.StatusOK, nil)
	callOK(t, app, http.MethodPost, routePlans, nil, map[string]any{
		"id": "tiny", "name": "Tiny", "amount_usd": 0.001, "period": map[string]any{"kind": "daily"},
	}, http.StatusCreated, nil)
	callOK(t, app, http.MethodPost, routeKeysBind, nil, map[string]any{"scope": scope, "plan_id": "tiny"}, http.StatusOK, nil)
	callOK(t, app, http.MethodPost, routeKeysLabel, nil, map[string]any{"scope": scope, "label": "Alice"}, http.StatusOK, nil)

	if !app.store.Authorize(scope, app.store.Now()).Allowed {
		t.Fatal("a key with a fresh budget was blocked")
	}

	// 1M output tokens at 2.00/1M is 2.00 USD against a 0.001 USD budget.
	app.store.RecordUsage(billing.UsageEvent{
		Scope: scope,
		Records: []billing.UsageRecord{{
			Provider: "codex", BillingModel: "gpt-5.5", UpstreamModel: "gpt-5.5", Generate: true,
			Breakdown: billing.TokenBreakdown{
				SchemaVersion: billing.TokenAccountingSchemaVersion,
				Quality:       billing.TokenAccountingComplete,
				TotalTokens:   1_000_000,
				Output:        billing.TokenOutputBreakdown{TotalTokens: 1_000_000, NonReasoningTokens: 1_000_000},
			},
		}},
	})
	decision := app.store.Authorize(scope, app.store.Now())
	if decision.Allowed {
		t.Fatalf("decision = %+v, want the key blocked", decision)
	}

	var directory billing.KeyDirectory
	callOK(t, app, http.MethodGet, routeKeys, nil, nil, http.StatusOK, &directory)
	if len(directory.Keys) != 1 {
		t.Fatalf("keys = %+v", directory.Keys)
	}
	view := directory.Keys[0]
	if !view.Blocked || view.Label != "Alice" || view.RemainingUSD != 0 {
		t.Fatalf("view = %+v, want a blocked, labelled key with no budget left", view)
	}
	if len(view.ByModel) != 1 || view.ByModel[0].BillingModel != "gpt-5.5" {
		t.Fatalf("ByModel = %+v", view.ByModel)
	}

	var stats billing.StatsView
	callOK(t, app, http.MethodGet, routeStats, nil, nil, http.StatusOK, &stats)
	if stats.BlockedKeys != 1 || stats.Lifetime.CostUSD != 2 {
		t.Fatalf("stats = %+v", stats)
	}

	callOK(t, app, http.MethodPost, routeKeysReset, nil, map[string]any{"scope": scope}, http.StatusOK, nil)
	if !app.store.Authorize(scope, app.store.Now()).Allowed {
		t.Fatal("the key is still blocked after a manual reset")
	}

	callOK(t, app, http.MethodPost, routeKeysUnbind, nil, map[string]any{"scope": scope}, http.StatusOK, nil)
	callOK(t, app, http.MethodGet, routeKeys, nil, nil, http.StatusOK, &directory)
	if !directory.Keys[0].Unlimited || directory.Keys[0].Lifetime.CostUSD != 2 {
		t.Fatalf("view = %+v, want an unlimited key that kept its history", directory.Keys[0])
	}

}

func TestSyncPrunesKeysDeletedFromCPA(t *testing.T) {
	app := newConfiguredApp(t)
	const kept, removed = "sk-kept-00000000001", "sk-removed-00000001"

	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{kept, removed}}, http.StatusOK, nil)
	app.store.RecordUsage(billing.UsageEvent{
		Scope: billing.CallerScope(removed),
		Records: []billing.UsageRecord{{
			Provider: "codex", BillingModel: "gpt-5.5", UpstreamModel: "gpt-5.5", Generate: true,
			Breakdown: billing.TokenBreakdown{
				SchemaVersion: billing.TokenAccountingSchemaVersion,
				Quality:       billing.TokenAccountingComplete,
				TotalTokens:   1000,
				Output:        billing.TokenOutputBreakdown{TotalTokens: 1000, NonReasoningTokens: 1000},
			},
		}},
	})

	var result billing.SyncResult
	callOK(t, app, http.MethodPost, routeKeysSync, nil, map[string]any{"keys": []string{kept}}, http.StatusOK, &result)
	if result.Removed != 1 || result.Matched != 1 {
		t.Fatalf("result = %+v", result)
	}

	var directory billing.KeyDirectory
	callOK(t, app, http.MethodGet, routeKeys, nil, nil, http.StatusOK, &directory)
	if len(directory.Keys) != 1 || directory.Keys[0].Scope != billing.CallerScope(kept) {
		t.Fatalf("keys = %+v, want only the surviving key", directory.Keys)
	}
}

// TestManagementRoutesWorkWhileDisabled matters for diagnosis: an operator who
// turned the plugin off still needs to read and fix its configuration.
func TestManagementRoutesWorkWhileDisabled(t *testing.T) {
	app := newAppWithPrice(t, false)
	var table billing.PriceTable
	callOK(t, app, http.MethodGet, routePrices, nil, nil, http.StatusOK, &table)
	if len(table.Models) != 1 {
		t.Fatalf("models = %+v", table.Models)
	}
	callOK(t, app, http.MethodPost, routePlans, nil, map[string]any{
		"id": "daily", "amount_usd": 1, "period": map[string]any{"kind": "daily"},
	}, http.StatusCreated, nil)
}

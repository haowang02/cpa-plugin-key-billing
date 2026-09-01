package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

const (
	accountTestKeyA = "sk-account-test-key-0001"
	accountTestKeyB = "sk-account-test-key-0002"
)

func callAccount(t *testing.T, app *App, path, apiKey string, query url.Values) ManagementResponse {
	t.Helper()
	headers := http.Header{}
	if apiKey != "" {
		headers.Set("Authorization", "Bearer "+apiKey)
	}
	raw, errHandle := app.HandleMethod(MethodManagementHandle, mustMarshal(t, ManagementRequest{
		Method: http.MethodGet, Path: resourceBase + path, Headers: headers, Query: query,
	}))
	if errHandle != nil {
		t.Fatalf("account request %s error = %v", path, errHandle)
	}
	var response ManagementResponse
	decodeResult(t, raw, &response)
	return response
}

func configuredAccountApp(t *testing.T) *App {
	t.Helper()
	app := newAppWithPrice(t, true)
	if _, errSync := app.store.SyncKeys([]string{accountTestKeyA, accountTestKeyB}, false); errSync != nil {
		t.Fatal(errSync)
	}
	if errLabel := app.store.SetLabel(billing.CallerScope(accountTestKeyA), "Alice"); errLabel != nil {
		t.Fatal(errLabel)
	}
	billOneRequest(t, app, accountTestKeyA, 20)
	billOneRequest(t, app, accountTestKeyB, 30)
	return app
}

func TestAccountOverviewAuthenticatesByAPIKeyScope(t *testing.T) {
	app := configuredAccountApp(t)

	response := callAccount(t, app, resourceAccountOverviewPath, accountTestKeyA, nil)
	if response.StatusCode != http.StatusOK || response.Headers.Get("Cache-Control") != "private, no-store" ||
		response.Headers.Get("Vary") != "Authorization" {
		t.Fatalf("response = %+v", response)
	}
	var overview accountOverviewResponse
	if errDecode := json.Unmarshal(response.Body, &overview); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !overview.Tracked || overview.Identity.Label != "Alice" || len(overview.ByModel) != 1 ||
		overview.ByModel[0].OutputTokens != 20 || !overview.ModelAccess.AllModels ||
		len(overview.ModelAccess.Models) != 0 {
		t.Fatalf("overview = %+v", overview)
	}
	body := string(response.Body)
	for _, forbidden := range []string{accountTestKeyA, accountTestKeyB, billing.CallerScope(accountTestKeyA), `"plan_id"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("account response leaked %q: %s", forbidden, body)
		}
	}

	unknown := callAccount(t, app, resourceAccountOverviewPath, "sk-valid-but-untracked-0003", nil)
	if errDecode := json.Unmarshal(unknown.Body, &overview); errDecode != nil || overview.Tracked {
		t.Fatalf("unknown overview = %+v, err = %v", overview, errDecode)
	}
}

func TestAccountRoutesRejectMissingOrAmbiguousBearer(t *testing.T) {
	app := configuredAccountApp(t)
	if response := callAccount(t, app, resourceAccountOverviewPath, "", nil); response.StatusCode != http.StatusUnauthorized ||
		response.Headers.Get("WWW-Authenticate") == "" {
		t.Fatalf("missing bearer response = %+v", response)
	}

	raw, errHandle := app.HandleMethod(MethodManagementHandle, mustMarshal(t, ManagementRequest{
		Method: http.MethodGet, Path: resourceBase + resourceAccountOverviewPath,
		Headers: http.Header{"Authorization": {"Bearer " + accountTestKeyA, "Bearer " + accountTestKeyB}},
	}))
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	var response ManagementResponse
	decodeResult(t, raw, &response)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ambiguous bearer response = %+v", response)
	}
}

func TestAccountLogsCannotCrossScopesOrExposeAdminFields(t *testing.T) {
	app := configuredAccountApp(t)
	response := callAccount(t, app, resourceAccountLogsPath, accountTestKeyA, url.Values{"limit": {"10"}})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("response = %+v", response)
	}
	var view accountLogView
	if errDecode := json.Unmarshal(response.Body, &view); errDecode != nil {
		t.Fatal(errDecode)
	}
	if view.Total != 1 || len(view.Entries) != 1 || view.Entries[0].Output != 20 {
		t.Fatalf("view = %+v", view)
	}
	if view.Filters == nil || len(view.Filters.Models) != 1 || view.Filters.Models[0] != "gpt-5.5" ||
		len(view.Filters.Sources) != 0 {
		t.Fatalf("account log filter options = %+v", view.Filters)
	}
	from := view.Entries[0].At.Format(time.RFC3339Nano)
	to := view.Entries[0].At.Add(time.Second).Format(time.RFC3339Nano)
	filtered := callAccount(t, app, resourceAccountLogsPath, accountTestKeyA, url.Values{
		"model": {"gpt-5.5"}, "status": {"normal"}, "from": {from}, "to": {to},
	})
	if errDecode := json.Unmarshal(filtered.Body, &view); errDecode != nil || view.Total != 1 {
		t.Fatalf("filtered account log = %+v, err = %v", view, errDecode)
	}
	body := string(response.Body)
	for _, forbidden := range []string{accountTestKeyA, accountTestKeyB, billing.CallerScope(accountTestKeyA),
		`"scope"`, `"auth_index"`, `"price_source"`, `"applied_output_per_1m"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("account log leaked %q: %s", forbidden, body)
		}
	}
}

func TestAccountLogsUseTheAdministratorSource(t *testing.T) {
	app := newAppWithPrice(t, true)
	if _, errSync := app.store.SyncKeys([]string{accountTestKeyA}, false); errSync != nil {
		t.Fatal(errSync)
	}
	publishUsageRecord(t, app, UsageRecord{
		Provider: "codex", ExecutorType: "CodexExecutor", Model: "gpt-5.5", Alias: "gpt-5.5",
		APIKey: accountTestKeyA, AuthIndex: "auth-account-test", AuthType: "oauth",
		Source: "private@example.com", Generate: true, RequestedAt: app.store.Now(),
		Detail: UsageDetail{OutputTokens: 20, TotalTokens: 20},
	})

	response := callAccount(t, app, resourceAccountLogsPath, accountTestKeyA, nil)
	var view accountLogView
	if errDecode := json.Unmarshal(response.Body, &view); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(view.Entries) != 1 || view.Entries[0].ExecutorType != "CodexExecutor" ||
		view.Entries[0].Source != "codex · private@example.com" {
		t.Fatalf("account log = %+v", view)
	}
	if view.Filters == nil || len(view.Filters.Sources) != 1 || view.Filters.Sources[0] != "codex · private@example.com" {
		t.Fatalf("account source filters = %+v", view.Filters)
	}
	filtered := callAccount(t, app, resourceAccountLogsPath, accountTestKeyA,
		url.Values{"source": {"codex · private@example.com"}})
	if errDecode := json.Unmarshal(filtered.Body, &view); errDecode != nil || view.Total != 1 {
		t.Fatalf("source-filtered account log = %+v, err = %v", view, errDecode)
	}
}

func TestAccountModelAccessExpandsGroupsAndLimitsPrices(t *testing.T) {
	app := configuredAccountApp(t)
	scope := billing.CallerScope(accountTestKeyA)
	if _, errPrice := app.store.UpsertPrice(billing.PriceRule{Pattern: "other-model", InputPer1M: 9, OutputPer1M: 18}); errPrice != nil {
		t.Fatal(errPrice)
	}
	group, errGroup := app.store.CreateModelGroup(billing.ModelGroup{Name: "可用模型", Models: []string{"gpt-5.5", "missing-model"}})
	if errGroup != nil {
		t.Fatal(errGroup)
	}
	if errModels := app.store.SetKeyModels(scope, []string{group.ID}, nil); errModels != nil {
		t.Fatal(errModels)
	}
	response := callAccount(t, app, resourceAccountPricesPath, accountTestKeyA, nil)
	var prices []billing.PriceRule
	if errDecode := json.Unmarshal(response.Body, &prices); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(prices) != 2 || prices[0].Pattern != "gpt-5.5" || prices[1].Pattern != "missing-model" ||
		prices[0].OutputPer1M != 2 || prices[1].InputPer1M != 0 || prices[1].OutputPer1M != 0 {
		t.Fatalf("account prices = %+v", prices)
	}
	if strings.Contains(string(response.Body), "other-model") || strings.Contains(string(response.Body), `"source"`) {
		t.Fatalf("account prices leaked unavailable model: %s", response.Body)
	}
	response = callAccount(t, app, resourceAccountOverviewPath, accountTestKeyA, nil)
	var overview accountOverviewResponse
	if errDecode := json.Unmarshal(response.Body, &overview); errDecode != nil {
		t.Fatal(errDecode)
	}
	if overview.ModelAccess.AllModels || len(overview.ModelAccess.Models) != 2 ||
		overview.ModelAccess.Models[0] != "gpt-5.5" || overview.ModelAccess.Models[1] != "missing-model" {
		t.Fatalf("overview model access = %+v", overview.ModelAccess)
	}
}

func TestDeletedAccountCannotReadItsHistory(t *testing.T) {
	app := configuredAccountApp(t)
	if _, errSync := app.store.SyncKeys([]string{accountTestKeyB}, false); errSync != nil {
		t.Fatal(errSync)
	}
	response := callAccount(t, app, resourceAccountOverviewPath, accountTestKeyA, nil)
	var overview accountOverviewResponse
	if errDecode := json.Unmarshal(response.Body, &overview); errDecode != nil {
		t.Fatal(errDecode)
	}
	if overview.Tracked {
		t.Fatalf("deleted account remained readable: %+v", overview)
	}
	logs := callAccount(t, app, resourceAccountLogsPath, accountTestKeyA, nil)
	var view accountLogView
	if errDecode := json.Unmarshal(logs.Body, &view); errDecode != nil {
		t.Fatal(errDecode)
	}
	if view.Total != 0 || len(view.Entries) != 0 {
		t.Fatalf("deleted account logs = %+v", view)
	}
}

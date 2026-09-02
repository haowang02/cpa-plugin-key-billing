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

func TestAccountStatusAuthenticatesByAPIKeyScope(t *testing.T) {
	app := configuredAccountApp(t)

	response := callAccount(t, app, routeStatus, accountTestKeyA, nil)
	if response.StatusCode != http.StatusOK || response.Headers.Get("Cache-Control") != "private, no-store" ||
		response.Headers.Get("Vary") != "Authorization" {
		t.Fatalf("response = %+v", response)
	}
	var status accountStatusResponse
	if errDecode := json.Unmarshal(response.Body, &status); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !status.Tracked || status.Identity.Label != "Alice" {
		t.Fatalf("status = %+v", status)
	}
	body := string(response.Body)
	for _, forbidden := range []string{accountTestKeyA, accountTestKeyB, billing.CallerScope(accountTestKeyA), `"plan_id"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("account response leaked %q: %s", forbidden, body)
		}
	}

	unknown := callAccount(t, app, routeStatus, "sk-valid-but-untracked-0003", nil)
	if errDecode := json.Unmarshal(unknown.Body, &status); errDecode != nil || status.Tracked {
		t.Fatalf("unknown status = %+v, err = %v", status, errDecode)
	}
}

func TestAccountRoutesRejectMissingOrAmbiguousBearer(t *testing.T) {
	app := configuredAccountApp(t)
	if response := callAccount(t, app, routeStatus, "", nil); response.StatusCode != http.StatusUnauthorized ||
		response.Headers.Get("WWW-Authenticate") == "" {
		t.Fatalf("missing bearer response = %+v", response)
	}

	raw, errHandle := app.HandleMethod(MethodManagementHandle, mustMarshal(t, ManagementRequest{
		Method: http.MethodGet, Path: resourceBase + routeStatus,
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

func TestAccountRequestEventsUseSharedShapeWithoutCrossingScopes(t *testing.T) {
	app := configuredAccountApp(t)
	response := callAccount(t, app, routeEvents, accountTestKeyA, url.Values{"limit": {"10"}})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("response = %+v", response)
	}
	var view billing.RequestEventView
	if errDecode := json.Unmarshal(response.Body, &view); errDecode != nil {
		t.Fatal(errDecode)
	}
	if view.Total != 1 || len(view.Entries) != 1 || view.Entries[0].Cost.BilledOutputTokens != 20 {
		t.Fatalf("view = %+v", view)
	}
	if view.Filters == nil || len(view.Filters.Models) != 1 || view.Filters.Models[0] != "gpt-5.5" ||
		len(view.Filters.Sources) != 0 {
		t.Fatalf("account request event filter options = %+v", view.Filters)
	}
	from := view.Entries[0].At.Format(time.RFC3339Nano)
	to := view.Entries[0].At.Add(time.Second).Format(time.RFC3339Nano)
	filtered := callAccount(t, app, routeEvents, accountTestKeyA, url.Values{
		"api_key": {billing.CallerScope(accountTestKeyB)},
		"model":   {"gpt-5.5"}, "status": {"normal"}, "from": {from}, "to": {to},
	})
	if errDecode := json.Unmarshal(filtered.Body, &view); errDecode != nil || view.Total != 1 {
		t.Fatalf("filtered account request events = %+v, err = %v", view, errDecode)
	}
	body := string(response.Body)
	for _, forbidden := range []string{accountTestKeyA, accountTestKeyB,
		billing.CallerScope(accountTestKeyA), billing.CallerScope(accountTestKeyB), `"auth_index":"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("account request events leaked %q: %s", forbidden, body)
		}
	}
}

func TestAccountRequestErrorsCannotCrossScopes(t *testing.T) {
	app := configuredAccountApp(t)
	for _, apiKey := range []string{accountTestKeyA, accountTestKeyB} {
		publishUsageRecord(t, app, UsageRecord{
			Provider: "codex", Model: "gpt-5.5", Alias: "gpt-5.5", APIKey: apiKey,
			Generate: true, Failed: true, Failure: UsageFailure{StatusCode: 502,
				Body: `{"error":{"message":"bad gateway","type":"upstream_error"}}`},
		})
	}
	response := callAccount(t, app, routeErrors, accountTestKeyA, url.Values{
		"api_key":     {billing.CallerScope(accountTestKeyB)},
		"status_code": {"502"},
	})
	var view billing.RequestErrorView
	if err := json.Unmarshal(response.Body, &view); err != nil {
		t.Fatal(err)
	}
	if view.Total != 1 || len(view.Entries) != 1 || view.Entries[0].StatusCode != 502 {
		t.Fatalf("errors = %+v", view)
	}
	body := string(response.Body)
	for _, forbidden := range []string{accountTestKeyA, accountTestKeyB,
		billing.CallerScope(accountTestKeyA), billing.CallerScope(accountTestKeyB), `"auth_index"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("account error response leaked %q: %s", forbidden, body)
		}
	}
}

func TestAccountAnalysisCannotCrossScopesOrExposeScope(t *testing.T) {
	app := configuredAccountApp(t)
	response := callAccount(t, app, routeAnalysis, accountTestKeyA, url.Values{
		"api_key": {billing.CallerScope(accountTestKeyB)},
	})
	var view billing.AnalysisView
	if err := json.Unmarshal(response.Body, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.UsageDistribution.APIKeys) != 0 || len(view.UsageDistribution.Models) != 1 ||
		view.UsageDistribution.Models[0].Requests != 1 || view.UsageDistribution.Models[0].TotalTokens != 20 {
		t.Fatalf("analysis = %+v", view)
	}
	body := string(response.Body)
	for _, forbidden := range []string{billing.CallerScope(accountTestKeyA), billing.CallerScope(accountTestKeyB)} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("account analysis leaked scope %q: %s", forbidden, body)
		}
	}
}

func TestAccountRequestEventsUseTheAdministratorSource(t *testing.T) {
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

	response := callAccount(t, app, routeEvents, accountTestKeyA, nil)
	var view billing.RequestEventView
	if errDecode := json.Unmarshal(response.Body, &view); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(view.Entries) != 1 || view.Entries[0].ExecutorType != "CodexExecutor" ||
		view.Entries[0].Source != "codex · private@example.com" {
		t.Fatalf("account request event = %+v", view)
	}
	if view.Filters == nil || len(view.Filters.Sources) != 1 || view.Filters.Sources[0] != "codex · private@example.com" {
		t.Fatalf("account request event source filters = %+v", view.Filters)
	}
	filtered := callAccount(t, app, routeEvents, accountTestKeyA,
		url.Values{"source": {"codex · private@example.com"}})
	if errDecode := json.Unmarshal(filtered.Body, &view); errDecode != nil || view.Total != 1 {
		t.Fatalf("source-filtered account request events = %+v, err = %v", view, errDecode)
	}
}

func TestAccountModelAccessExpandsGroupsAndPricesReturnSharedCatalog(t *testing.T) {
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
	response := callAccount(t, app, routePrices, accountTestKeyA, nil)
	var prices []billing.PriceRow
	if errDecode := json.Unmarshal(response.Body, &prices); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(prices) != 2 || prices[0].Pattern != "gpt-5.5" || prices[1].Pattern != "other-model" ||
		prices[0].OutputPer1M != 2 || prices[1].InputPer1M != 9 || prices[1].OutputPer1M != 18 {
		t.Fatalf("account prices = %+v", prices)
	}
	if !strings.Contains(string(response.Body), `"source"`) {
		t.Fatalf("account prices did not use management response shape: %s", response.Body)
	}
	response = callAccount(t, app, routeAccess, accountTestKeyA, nil)
	var access accountModelAccess
	if errDecode := json.Unmarshal(response.Body, &access); errDecode != nil {
		t.Fatal(errDecode)
	}
	if access.AllModels || len(access.Models) != 2 ||
		access.Models[0] != "gpt-5.5" || access.Models[1] != "missing-model" {
		t.Fatalf("model access = %+v", access)
	}
}

func TestDeletedAccountCannotReadItsHistory(t *testing.T) {
	app := configuredAccountApp(t)
	if _, errSync := app.store.SyncKeys([]string{accountTestKeyB}, false); errSync != nil {
		t.Fatal(errSync)
	}
	response := callAccount(t, app, routeStatus, accountTestKeyA, nil)
	var status accountStatusResponse
	if errDecode := json.Unmarshal(response.Body, &status); errDecode != nil {
		t.Fatal(errDecode)
	}
	if status.Tracked {
		t.Fatalf("deleted account remained readable: %+v", status)
	}
	events := callAccount(t, app, routeEvents, accountTestKeyA, nil)
	var view billing.RequestEventView
	if errDecode := json.Unmarshal(events.Body, &view); errDecode != nil {
		t.Fatal(errDecode)
	}
	if view.Total != 0 || len(view.Entries) != 0 {
		t.Fatalf("deleted account request events = %+v", view)
	}
}

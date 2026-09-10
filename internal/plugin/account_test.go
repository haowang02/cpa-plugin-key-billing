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

func TestUsageCreatesKeyIdentityBeforeAnyFrontendSync(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			app, path := newAppWithPriceAndState(t, true)
			const apiKey = "sk-0001"
			publishUsageRecord(t, app, UsageRecord{
				APIKey: apiKey, Model: "gpt-5.5", Alias: "gpt-5.5", Failed: failed,
				RequestedAt: app.store.Now(), Detail: UsageDetail{InputTokens: 100, TotalTokens: 100},
			})
			app.Shutdown()
			cfg := billing.DefaultConfig()
			cfg.StateFile = path
			if err := app.store.Configure(cfg); err != nil {
				t.Fatal(err)
			}
			keys := app.store.KeyViews()
			if len(keys) != 1 || keys[0].Scope != billing.CallerScope(apiKey) || keys[0].Preview != "*******" || keys[0].InConfig {
				t.Fatalf("usage did not persist a complete key identity: %+v", keys)
			}
			result, err := app.store.SyncKeys([]string{apiKey}, false)
			if err != nil || result.Added != 1 || len(app.store.KeyViews()) != 1 {
				t.Fatalf("sync did not reuse the traffic-created key: %+v, %v", result, err)
			}
			events := requestEventEntries(t, app)
			if len(events) != 1 || events[0].Preview != "*******" || events[0].Failed != failed {
				t.Fatalf("sync changed usage history or lost its preview: %+v", events)
			}
		})
	}
}

func TestAccountProfileAuthenticatesByAPIKeyScope(t *testing.T) {
	app := configuredAccountApp(t)

	response := callAccount(t, app, routeProfile, accountTestKeyA, nil)
	if response.StatusCode != http.StatusOK || response.Headers.Get("Cache-Control") != "private, no-store" ||
		response.Headers.Get("Vary") != "Authorization" {
		t.Fatalf("response = %+v", response)
	}
	var access accountProfileResponse
	if errDecode := json.Unmarshal(response.Body, &access); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !access.Tracked || access.Identity.Label != "Alice" {
		t.Fatalf("access = %+v", access)
	}
	body := string(response.Body)
	for _, forbidden := range []string{accountTestKeyA, accountTestKeyB, billing.CallerScope(accountTestKeyA), `"plan_id"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("account response leaked %q: %s", forbidden, body)
		}
	}

	unknown := callAccount(t, app, routeProfile, "sk-valid-but-untracked-0003", nil)
	var unknownAccess accountProfileResponse
	if errDecode := json.Unmarshal(unknown.Body, &unknownAccess); errDecode != nil || unknownAccess.Tracked {
		t.Fatalf("unknown access = %+v, err = %v", unknownAccess, errDecode)
	}
}

func TestAccountRoutesRejectMissingOrAmbiguousBearer(t *testing.T) {
	app := configuredAccountApp(t)
	if response := callAccount(t, app, routeProfile, "", nil); response.StatusCode != http.StatusUnauthorized ||
		response.Headers.Get("WWW-Authenticate") == "" {
		t.Fatalf("missing bearer response = %+v", response)
	}

	raw, errHandle := app.HandleMethod(MethodManagementHandle, mustMarshal(t, ManagementRequest{
		Method: http.MethodGet, Path: resourceBase + routeProfile,
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
		len(view.Filters.Sources) != 1 || view.Filters.Sources[0] != "openai" {
		t.Fatalf("account request event filter options = %+v", view.Filters)
	}
	from := view.Entries[0].At.Format(time.RFC3339Nano)
	to := view.Entries[0].At.Add(time.Second).Format(time.RFC3339Nano)
	filtered := callAccount(t, app, routeEvents, accountTestKeyA, url.Values{
		"api_key": {billing.CallerScope(accountTestKeyB)},
		"model":   {"gpt-5.5"}, "failed": {"false"}, "from": {from}, "to": {to},
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

func TestAccountRoutingAndPricesRespectItsScope(t *testing.T) {
	app := configuredAccountApp(t)
	hostCalls := 0
	app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
		hostCalls++
		if method != hostAuthList {
			t.Fatalf("host method=%q", method)
		}
		return json.RawMessage(`{"files":[{"id":"auth-codex","provider":"codex","source":"file","path":"/auth/codex.json","email":"user@example.com"}]}`), nil
	})
	scope := billing.CallerScope(accountTestKeyA)
	if _, errPrice := app.store.UpsertPrice(billing.CustomPrice{ModelID: "other-model", PriceRates: billing.PriceRates{InputPer1M: 9, OutputPer1M: 18}}); errPrice != nil {
		t.Fatal(errPrice)
	}
	_, errRoute := app.store.CreateRoute(billing.Route{Name: "Codex", Rule: billing.RouteRule{
		Models:              []string{"gpt-5.5", "missing-model"},
		CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAuthFiles, Provider: "codex"}},
	}}, []string{scope})
	if errRoute != nil {
		t.Fatal(errRoute)
	}
	for _, path := range []string{routeProfile, routeSubscription} {
		response := callAccount(t, app, path, accountTestKeyA, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d", path, response.StatusCode)
		}
		if strings.Contains(string(response.Body), `"credentials"`) {
			t.Fatalf("%s included routing details", path)
		}
	}
	if hostCalls != 0 {
		t.Fatalf("profile/subscription made %d host calls", hostCalls)
	}
	response := callAccount(t, app, routePrices, accountTestKeyA, url.Values{"model": {"gpt-5.5"}})
	var prices []billing.PriceRow
	if errDecode := json.Unmarshal(response.Body, &prices); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(prices) != 1 || prices[0].ModelID != "gpt-5.5" || prices[0].OutputPer1M != 2 {
		t.Fatalf("account prices=%+v", prices)
	}

	if !strings.Contains(string(response.Body), `"source"`) {
		t.Fatalf("account prices did not use management response shape: %s", response.Body)
	}
	response = callAccount(t, app, routeRouting, accountTestKeyA, nil)
	if hostCalls != 1 {
		t.Fatalf("routing made %d host calls", hostCalls)
	}
	var access accountRoutingResponse
	if errDecode := json.Unmarshal(response.Body, &access); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(access.Models) != 2 ||
		access.Models[0] != "gpt-5.5" || access.Models[1] != "missing-model" {
		t.Fatalf("route access = %+v", access)
	}
	if !access.RoutingValid || len(access.Credentials) != 1 || access.Credentials[0].Name != "user@example.com" {
		t.Fatalf("credential access = %+v", access)
	}
	if strings.Contains(string(response.Body), `"bindings"`) || strings.Contains(string(response.Body), `"kind"`) {
		t.Fatalf("account access exposed route internals: %s", response.Body)
	}
	t.Run("deny-only scope", func(t *testing.T) {
		app, scope := configuredRoutingApp(t, billing.RouteRule{DeniedModels: []string{"gpt"}, DeniedCredentialIDs: []string{billing.CredentialFingerprint("dummy-denied")}, DeniedCredentialProviders: []billing.CredentialProviderSelector{{Source: "ai-providers", Provider: "claude"}}})
		app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
			if method != hostAuthList {
				t.Fatalf("unexpected method: %s", method)
			}
			return json.RawMessage(`{"files":[{"id":"dummy-denied","provider":"codex","source":"file","path":"/dummy/denied.json","email":"dummy@example.test"}]}`), nil
		})
		response := app.accountRouting(viewAccess{APIKey: true, Scope: scope, Tracked: true})
		var access accountRoutingResponse
		if err := json.Unmarshal(response.Body, &access); err != nil {
			t.Fatal(err)
		}
		if !access.RoutingValid || len(access.Models) != 0 || len(access.Credentials) != 0 || len(access.DeniedModels) != 1 || len(access.DeniedCredentials) != 2 || len(access.Warnings) != 0 {
			t.Fatalf("blacklist-only account: %+v", access)
		}
		if strings.Contains(string(response.Body), "sha256:") || strings.Contains(string(response.Body), "dummy-denied") {
			t.Fatalf("account exposed internal refs: %s", response.Body)
		}
	})
}

func TestDeletedAccountCannotReadItsHistory(t *testing.T) {
	app := configuredAccountApp(t)
	if _, errSync := app.store.SyncKeys([]string{accountTestKeyB}, false); errSync != nil {
		t.Fatal(errSync)
	}
	response := callAccount(t, app, routeProfile, accountTestKeyA, nil)
	var access accountProfileResponse
	if errDecode := json.Unmarshal(response.Body, &access); errDecode != nil {
		t.Fatal(errDecode)
	}
	if access.Tracked {
		t.Fatalf("deleted account remained readable: %+v", access)
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

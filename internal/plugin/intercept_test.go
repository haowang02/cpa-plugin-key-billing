package plugin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

const testAPIKey = "sk-test-key-0001"

func exhaustedApp(t *testing.T, resetAfter time.Duration) *App {
	t.Helper()
	app := newAppWithPrice(t, true)
	if _, errSync := app.store.SyncKeys([]string{testAPIKey}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	if _, errCreate := app.store.CreatePlanWithBindings(billing.Plan{
		ID: "plan-5", Name: "Plan 5", Windows: []billing.QuotaWindow{{Name: "额度", Amount: 5, PeriodSeconds: int64(resetAfter / time.Second)}},
	}, []string{billing.CallerScope(testAPIKey)}); errCreate != nil {
		t.Fatalf("CreatePlanWithBindings error = %v", errCreate)
	}
	requestID := admit(t, app, "openai", "/v1/chat/completions")
	billUsage(t, app, 0, 0, 0, 2_500_000, 0)
	completeRequest(t, app, requestID)
	return app
}

func callIntercept(t *testing.T, app *App, sourceFormat string) RequestInterceptResponse {
	t.Helper()
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		SourceFormat: sourceFormat,
		Model:        "gpt-5.5",
		Metadata:     map[string]any{MetadataCallerScope: billing.CallerScope(testAPIKey)},
	}))
	if err != nil {
		t.Fatalf("request.intercept_before error = %v", err)
	}
	var resp RequestInterceptResponse
	decodeResult(t, raw, &resp)
	return resp
}

func TestInterceptTerminatesAnExhaustedKey(t *testing.T) {
	app := exhaustedApp(t, 30*time.Minute)
	resp := callIntercept(t, app, "openai")
	if !resp.Terminate || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("response = %+v, want quota rejection", resp)
	}
	retryAfter, err := strconv.Atoi(resp.ResponseHeaders.Get("Retry-After"))
	if err != nil || retryAfter != 1800 {
		t.Fatalf("Retry-After = %q, want 1800", resp.ResponseHeaders.Get("Retry-After"))
	}
}

func TestInterceptUsesTheClientErrorShape(t *testing.T) {
	app := exhaustedApp(t, 12*time.Hour)
	for _, test := range []struct {
		format string
		want   map[string]string
	}{
		{"openai", map[string]string{"type": "rate_limit_error", "code": "rate_limit_exceeded"}},
		{"claude", map[string]string{"type": "rate_limit_error"}},
		{"gemini", map[string]string{"type": "rate_limit_error", "code": "rate_limit_exceeded"}},
	} {
		t.Run(test.format, func(t *testing.T) {
			resp := callIntercept(t, app, test.format)
			var payload struct {
				Error map[string]any `json:"error"`
			}
			if err := json.Unmarshal(resp.ResponseBody, &payload); err != nil {
				t.Fatalf("invalid JSON error body: %v (raw=%s)", err, resp.ResponseBody)
			}
			for field, want := range test.want {
				if got, _ := payload.Error[field].(string); got != want {
					t.Fatalf("error.%s = %q, want %q (body=%s)", field, got, want, resp.ResponseBody)
				}
			}
			if message, _ := payload.Error["message"].(string); !strings.Contains(message, "quota exhausted") {
				t.Fatalf("message = %q, want it to state the exhausted quota", message)
			}
		})
	}
}

func TestInterceptEnforcesQuotaDimensionsAfterRestart(t *testing.T) {
	app, path := newAppWithPriceAndState(t, true)
	if _, err := app.store.SyncKeys([]string{testAPIKey}, false); err != nil {
		t.Fatal(err)
	}
	_, err := app.store.CreatePlanWithBindings(billing.Plan{ID: "p", Windows: []billing.QuotaWindow{
		{Name: "请求", RequestLimit: 1, PeriodSeconds: 3600},
		{Name: "Token", TokenLimit: 1000, PeriodSeconds: 7200},
	}}, []string{flowScope()})
	if err != nil {
		t.Fatal(err)
	}
	admit(t, app, "openai", "/v1/chat/completions")
	record := UsageRecord{APIKey: testAPIKey, Model: flowModel, Provider: "openai",
		RequestedAt: app.store.Now(), Failed: true, Failure: UsageFailure{StatusCode: 502}}
	publishUsageRecord(t, app, record)
	if response := callIntercept(t, app, "openai"); response.Terminate {
		t.Fatal("failed record consumed request quota")
	}
	record.Failed = false
	publishUsageRecord(t, app, record)
	response := callIntercept(t, app, "openai")
	if response.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(response.ResponseBody), "requests 1 / 1") {
		t.Fatalf("successful record did not exhaust request quota: %+v", response)
	}
	// This usage belongs to an already admitted concurrent execution.
	record.Provider = "unknown"
	record.Detail = UsageDetail{TotalTokens: 1200}
	publishUsageRecord(t, app, record)
	app.Shutdown()
	reopened := newTestApp(t)
	defer reopened.Shutdown()
	if _, err := reopened.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{
		ConfigYAML: []byte("enabled: true\nstate_file: " + path + "\n"),
	})); err != nil {
		t.Fatal(err)
	}
	response = callIntercept(t, reopened, "openai")
	if response.StatusCode != 429 || !strings.Contains(string(response.ResponseBody), "tokens 1200 / 1000") ||
		!strings.Contains(string(response.ResponseBody), "requests 2 / 1") {
		t.Fatalf("restart did not preserve exhaustion: %+v", response)
	}
}

func restrictApp(t *testing.T, rule billing.RouteRule) *App {
	t.Helper()
	app := newAppWithPrice(t, true)
	if _, errSync := app.store.SyncKeys([]string{testAPIKey}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	_, errCreate := app.store.CreateRoute(billing.Route{Name: "基础", Rule: rule}, []string{flowScope()})
	if errCreate != nil {
		t.Fatalf("CreateRoute error = %v", errCreate)
	}
	return app
}

func interceptModel(t *testing.T, app *App, clientFormat, model string) RequestInterceptResponse {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		SourceFormat: clientFormat, Model: model, RequestedModel: model, Metadata: map[string]any{
			MetadataCallerScope: flowScope(),
			MetadataRequestPath: "/v1/chat/completions",
		},
	}))
	if errHandle != nil {
		t.Fatalf("request.intercept_before error = %v", errHandle)
	}
	var response RequestInterceptResponse
	decodeResult(t, raw, &response)
	return response
}

func TestForbiddenModelIsRefusedInEveryClientFormat(t *testing.T) {
	for _, mode := range []string{"allow", "deny"} {
		t.Run(mode, func(t *testing.T) {
			rule := billing.RouteRule{Models: []string{"chat/fast"}}
			if mode == "deny" {
				rule = billing.RouteRule{DeniedModels: []string{flowModel}}
			}
			app := restrictApp(t, rule)

			cases := []struct {
				format string
				want   map[string]string
			}{
				{"openai", map[string]string{"type": "permission_error", "code": "insufficient_quota"}},
				{"claude", map[string]string{"type": "permission_error"}},
				{"gemini-cli", map[string]string{"type": "permission_error", "code": "insufficient_quota"}},
			}
			for _, testCase := range cases {
				t.Run(testCase.format, func(t *testing.T) {
					response := interceptModel(t, app, testCase.format, flowModel)
					if !response.Terminate || response.StatusCode != http.StatusForbidden {
						t.Fatalf("response = %+v, want a terminating 403", response)
					}
					if retry := response.ResponseHeaders.Get("Retry-After"); retry != "" {
						t.Fatalf("Retry-After = %q, want none on a permanent refusal", retry)
					}
					var payload struct {
						Error map[string]any `json:"error"`
					}
					if errUnmarshal := json.Unmarshal(response.ResponseBody, &payload); errUnmarshal != nil {
						t.Fatalf("decode body: %v (raw=%s)", errUnmarshal, response.ResponseBody)
					}
					for field, want := range testCase.want {
						if got, _ := payload.Error[field].(string); got != want {
							t.Fatalf("error.%s = %q, want %q (body=%s)", field, got, want, response.ResponseBody)
						}
					}
					message, _ := payload.Error["message"].(string)
					expected := []string{flowModel, "chat/fast"}
					if mode == "deny" {
						expected[1] = "denied by a routing rule"
					}
					for _, want := range expected {
						if !strings.Contains(message, want) {
							t.Fatalf("message = %q, want it to name %q", message, want)
						}
					}
				})
			}
		})
	}
}

func TestForbiddenModelLeavesTheSubscriptionUntouched(t *testing.T) {
	for _, mode := range []string{"allow", "deny", "unserved"} {
		t.Run(mode, func(t *testing.T) {
			rule := billing.RouteRule{Models: []string{"chat/fast"}}
			switch mode {
			case "deny":
				rule = billing.RouteRule{DeniedModels: []string{flowModel}}
			case "unserved":
				rule = billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAIProviders, Provider: "codex"}}}
			}
			app := restrictApp(t, rule)
			app.SetHostCaller(hostDirectoryCaller(`[{"id":"config-codex","provider":"codex","models":["chat/fast"]}]`, "[]"))
			if _, errCreate := app.store.CreatePlanWithBindings(billing.Plan{
				ID: "daily", Name: "Daily 1", Windows: []billing.QuotaWindow{{Name: "额度", Amount: 1, PeriodSeconds: 86400}},
			}, nil); errCreate != nil {
				t.Fatalf("CreatePlanWithBindings error = %v", errCreate)
			}
			if errBind := app.store.BindKey(flowScope(), "daily"); errBind != nil {
				t.Fatalf("BindKey error = %v", errBind)
			}

			if response := interceptModel(t, app, "openai", flowModel); !response.Terminate {
				t.Fatal("a model the key may not call was admitted")
			}
			key, _ := app.store.KeyViewForScope(flowScope())
			if !key.Windows[0].EndAt.IsZero() || key.Windows[0].Dimensions[0].Used != "0" {
				t.Fatalf("key = %+v, want its cycle left inactive", key)
			}
			if key.CurrentConcurrency != 0 {
				t.Fatal("refused model occupied concurrency")
			}
			events, err := app.store.RequestEvents(billing.RequestEventQuery{})
			if err != nil || events.Total != 0 {
				t.Fatalf("refusal recorded usage: %+v, %v", events, err)
			}
		})
	}
}

func TestGrantedModelIsBilledNormally(t *testing.T) {
	app := restrictApp(t, billing.RouteRule{Models: []string{flowModel, "chat/fast"}})

	billOneRequest(t, app, testAPIKey, 1_000)
	entries := requestEventEntries(t, app)
	if len(entries) != 1 || entries[0].BillingModel != flowModel {
		t.Fatalf("request events = %+v, want the granted model billed", entries)
	}
}

func TestAfterAuthEnforcesCredentialRoutesAndRecordsResults(t *testing.T) {
	ref := billing.CredentialFingerprint
	files := billing.CredentialProviderSelector{Source: billing.CredentialSourceAuthFiles, Provider: "codex"}
	config := billing.CredentialProviderSelector{Source: billing.CredentialSourceAIProviders, Provider: "codex"}
	// memory-A is a runtime-only connection; the host reports config-A as claude although its synced entry says codex.
	const credentials = `[{"id":"A","provider":"codex","models":["gpt-5.6"]},{"id":"B","provider":"codex","models":["gpt-5.6"]},
		{"id":"file-A","provider":"codex","models":["gpt-5.6"]},{"id":"memory-A","provider":"codex","models":["gpt-5.6"]},
		{"id":"config-A","provider":"claude","models":["gpt-5.6"]}]`
	const listed = `[{"id":"A","provider":"codex","path":"/auth/a.json"},{"id":"B","provider":"codex","path":"/auth/b.json"},
		{"id":"file-A","provider":"codex","path":"/auth/file-a.json"},{"id":"memory-A","provider":"codex","runtime_only":true}]`
	for _, tc := range []struct {
		name, id, want string
		rule           billing.RouteRule
		hostFails      bool
		debugOff       bool
	}{
		{name: "exact allow", rule: billing.RouteRule{CredentialIDs: []string{ref("A")}}, id: "A", want: "allow"},
		{name: "issue 28 blacklist", rule: billing.RouteRule{CredentialIDs: []string{ref("A")}, DeniedCredentialIDs: []string{ref("B"), ref("C")}}, id: "B", want: "deny", debugOff: true},
		{name: "outside whitelist", rule: billing.RouteRule{CredentialIDs: []string{ref("A")}}, id: "B", want: "deny"},
		{name: "auth file category", rule: billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{files}}, id: "file-A", want: "allow"},
		{name: "category deny overrides exact allow", rule: billing.RouteRule{CredentialIDs: []string{ref("file-A"), ref("memory-A")}, DeniedCredentialProviders: []billing.CredentialProviderSelector{files}}, id: "file-A", want: "deny"},
		{name: "runtime-only credential is a provider connection", rule: billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{config}}, id: "memory-A", want: "allow"},
		{name: "host identity outranks synced names", rule: billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{config}}, id: "config-A", want: "deny"},
		{name: "credential the host no longer reports", rule: billing.RouteRule{DeniedCredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAIProviders, Provider: "claude"}}}, id: "gone", want: "deny"},
		{name: "host failure", rule: billing.RouteRule{CredentialIDs: []string{ref("A")}}, id: "A", hostFails: true, want: "unverified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp(t)
			t.Cleanup(app.Shutdown)
			cfg := testConfigYAML(t, true)
			if tc.debugOff {
				cfg = []byte(strings.ReplaceAll(string(cfg), "debug: true", "debug: false"))
			}
			if _, err := app.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{ConfigYAML: cfg})); err != nil {
				t.Fatal(err)
			}
			const key = "sk-issue28-dummy-key"
			scope := billing.CallerScope(key)
			if _, err := app.store.SyncKeys([]string{key}, false); err != nil {
				t.Fatal(err)
			}
			if err := app.store.SetConcurrencyLimit(scope, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := app.store.CreateRoute(billing.Route{ID: "credential-route", Name: "Credential route", Rule: tc.rule}, []string{scope}); err != nil {
				t.Fatal(err)
			}
			if err := app.store.SyncConfigCredentials(map[string]billing.ConfigCredentialDetails{
				ref("config-A"): {ConfigRef: ref("config-A-entry"), Provider: "codex", AuthIndex: "config-A"},
			}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			host := hostDirectoryCaller(credentials, listed)
			app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
				calls++
				if tc.hostFails {
					return nil, errors.New("dummy upstream secret must not reach clients")
				}
				return host(method, payload)
			})
			req := RequestInterceptRequest{RequestID: "selected-request", SourceFormat: "openai", Model: "gpt-5.6", RequestedModel: "gpt-5.6", Metadata: map[string]any{MetadataCallerScope: scope}}
			raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, req))
			if err != nil {
				t.Fatal(err)
			}
			var response RequestInterceptResponse
			decodeResult(t, raw, &response)
			if response.Terminate {
				t.Fatalf("admission refused: %+v", response)
			}
			admissionCalls := calls
			// The post-auth check ignores the executed model.
			req.Model = "actual-upstream-model"
			req.Metadata[MetadataSelectedAuth] = tc.id
			raw, err = app.HandleMethod(MethodRequestInterceptAfter, mustMarshal(t, req))
			if err != nil {
				t.Fatal(err)
			}
			decodeResult(t, raw, &response)
			rejected := tc.want != "allow"
			if response.Terminate != rejected || rejected && response.StatusCode != 503 {
				t.Fatalf("response=%+v want=%s", response, tc.want)
			}
			if !rejected && calls != admissionCalls {
				t.Fatalf("an allowed selection read the host %d more times", calls-admissionCalls)
			}
			wantInFlight := 1
			if rejected {
				wantInFlight = 0
			}
			if active := app.store.CredentialConcurrency(scope)[ref(tc.id)]; active != wantInFlight {
				t.Fatalf("credential in-flight requests = %d, want %d", active, wantInFlight)
			}
			if rejected {
				code := "routing_credential_mismatch"
				if tc.want == "unverified" {
					code = "routing_credential_unverified"
				}
				if !strings.Contains(string(response.ResponseBody), code) {
					t.Fatalf("response=%s want code %s", response.ResponseBody, code)
				}
			}
			outcome, status := "succeeded", 200
			if rejected {
				outcome, status = "rejected", 503
			}
			if _, err := app.HandleMethod(MethodRequestComplete, mustMarshal(t, RequestCompletion{RequestID: req.RequestID, Outcome: outcome, StatusCode: status})); err != nil {
				t.Fatal(err)
			}
			if current := app.store.KeyViews()[0].CurrentConcurrency; current != 0 {
				t.Fatalf("completion leaked %d concurrency slots", current)
			}
			page, err := app.store.PluginLogsPage(billing.PluginLogQuery{Levels: []billing.PluginLogLevel{billing.PluginLogDebug, billing.PluginLogError}, Limit: 10})
			if err != nil || len(page.Entries) == 0 || !strings.HasPrefix(page.Entries[0].Message, "route ") {
				t.Fatalf("entries=%+v err=%v", page.Entries, err)
			}
			var row map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(page.Entries[0].Message, "route ")), &row); err != nil {
				t.Fatal(err)
			}
			if row["credential_policy"] != "restricted" || row["credential_result"] != tc.want || row["outcome"] != outcome {
				t.Fatalf("row=%+v", row)
			}
			if tc.want == "allow" && !strings.HasPrefix(row["selected_credential"].(string), "codex · ") {
				t.Fatalf("selected credential was not named by its host identity: %+v", row)
			}
			if rejected && (page.Entries[0].Level != billing.PluginLogError || row["credential_error"] == nil) {
				t.Fatalf("rejection is not an actionable error: %+v", page.Entries[0])
			}
			if strings.Contains(page.Entries[0].Message, "dummy upstream secret") || strings.Contains(string(response.ResponseBody), "dummy upstream secret") {
				t.Fatal("host error leaked into the route log or the client response")
			}
			if len(requestEventEntries(t, app)) != 0 {
				t.Fatal("routing bookkeeping fabricated provider usage")
			}
		})
	}
}

// Admission confirms each refusal against a fresh host read.
func TestAdmissionRefusesModelsNoAllowedCredentialServes(t *testing.T) {
	app, scope := configuredRoutingApp(t, billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAIProviders, Provider: "codex"}}})
	for _, model := range []string{"agent-model", "new-model"} {
		if _, err := app.store.UpsertPrice(billing.CustomPrice{ModelID: model, PriceRates: billing.PriceRates{InputPer1M: 1, OutputPer1M: 2}}); err != nil {
			t.Fatal(err)
		}
	}
	served, calls := `"gpt-5.6"`, 0
	app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
		calls++
		return hostDirectoryCaller(`[{"id":"config-codex","provider":"codex","models":[`+served+`]},{"id":"file-codex","provider":"codex","models":["other-model"]}]`,
			`[{"id":"file-codex","provider":"codex","path":"/auth/file-codex.json"}]`)(method, payload)
	})
	admit := func(id, model string, metadata map[string]any) RequestInterceptResponse {
		t.Helper()
		metadata[MetadataCallerScope] = scope
		raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{RequestID: id, SourceFormat: "claude", Model: model, RequestedModel: model, Metadata: metadata}))
		if err != nil {
			t.Fatal(err)
		}
		var response RequestInterceptResponse
		decodeResult(t, raw, &response)
		if _, err := app.HandleMethod(MethodRequestComplete, mustMarshal(t, RequestCompletion{RequestID: id, Outcome: "succeeded", StatusCode: 200})); err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := admit("unserved", "other-model", map[string]any{})
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(response.ResponseBody, &body); err != nil || !response.Terminate || response.StatusCode != http.StatusForbidden ||
		body.Error["type"] != "permission_error" || body.Error["message"] != routeNoCredential.message("other-model") {
		t.Fatalf("response=%+v body=%s", response, response.ResponseBody)
	}
	page, err := app.store.PluginLogsPage(billing.PluginLogQuery{Limit: 1})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Level != billing.PluginLogError || !strings.Contains(page.Entries[0].Message, `"credential_result":"no_credential"`) {
		t.Fatalf("entries=%+v err=%v", page.Entries, err)
	}
	for _, test := range []struct {
		model    string
		metadata map[string]any
	}{
		{"GPT-5.6(high)", map[string]any{}},
		{"agent-model", map[string]any{MetadataAuthSelectionModel: "gpt-5.6"}},
	} {
		if response := admit(test.model, test.model, test.metadata); response.Terminate {
			t.Fatalf("%s was refused: %+v", test.model, response)
		}
	}
	served = `"gpt-5.6","new-model"`
	if response := admit("added", "new-model", map[string]any{}); response.Terminate {
		t.Fatalf("a newly served model was refused from a stale read: %+v", response)
	}
	served, calls = `"new-model"`, 0
	if response := admit("recent", "gpt-5.6", map[string]any{}); response.Terminate || calls != 0 {
		t.Fatalf("a recent read did not admit without the host: calls=%d response=%+v", calls, response)
	}
	app.directory.ReadAt = app.directory.ReadAt.Add(-credentialDirectoryTTL)
	if response := admit("expired", "gpt-5.6", map[string]any{}); !response.Terminate || calls == 0 {
		t.Fatalf("an expired read was reused: calls=%d response=%+v", calls, response)
	}
}

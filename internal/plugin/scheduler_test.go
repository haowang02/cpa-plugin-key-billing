package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func configuredRoutingApp(t *testing.T, rule billing.RouteRule) (*App, string) {
	app := newConfiguredApp(t)
	const key = "sk-route-test-00000001"
	scope := billing.CallerScope(key)
	if _, err := app.store.SyncKeys([]string{key}, false); err != nil {
		t.Fatal(err)
	}
	_, err := app.store.CreateRoute(billing.Route{ID: "route-test", Name: "Route", Rule: rule}, []string{scope})
	if err != nil {
		t.Fatal(err)
	}
	return app, scope
}

func TestRoutedRequestWritesOneCombinedDebugLog(t *testing.T) {
	app, scope := configuredRoutingApp(t, billing.RouteRule{Models: []string{"gpt-5.6"}})
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{RequestID: "request-1", SourceFormat: "openai", Model: "gpt-5.6", RequestedModel: "gpt-5.6", Metadata: map[string]any{MetadataCallerScope: scope}}))
	if err != nil {
		t.Fatal(err)
	}
	var intercept RequestInterceptResponse
	decodeResult(t, raw, &intercept)
	if intercept.Terminate {
		t.Fatalf("intercept=%+v", intercept)
	}
	if _, err = app.HandleMethod(MethodRequestComplete, mustMarshal(t, RequestCompletion{RequestID: "request-1", Outcome: "succeeded", StatusCode: 200})); err != nil {
		t.Fatal(err)
	}
	page, err := app.store.PluginLogsPage(billing.PluginLogQuery{Levels: []billing.PluginLogLevel{billing.PluginLogDebug}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || !strings.HasPrefix(page.Entries[0].Message, "route ") {
		t.Fatalf("entries=%+v", page.Entries)
	}
	var row map[string]any
	if err = json.Unmarshal([]byte(strings.TrimPrefix(page.Entries[0].Message, "route ")), &row); err != nil {
		t.Fatal(err)
	}
	if row["model_policy"] != "restricted" || row["credential_policy"] != "unrestricted" || row["outcome"] != "succeeded" {
		t.Fatalf("row=%+v", row)
	}
}

func TestUnrestrictedRouteWritesNoRoutingDebugLog(t *testing.T) {
	app, scope := configuredRoutingApp(t, billing.RouteRule{})
	if _, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{RequestID: "unrestricted", SourceFormat: "openai", Model: "gpt-5.6", RequestedModel: "gpt-5.6", Metadata: map[string]any{MetadataCallerScope: scope}})); err != nil {
		t.Fatal(err)
	}
	if _, err := app.HandleMethod(MethodRequestComplete, mustMarshal(t, RequestCompletion{RequestID: "unrestricted", Outcome: "succeeded", StatusCode: 200})); err != nil {
		t.Fatal(err)
	}
	page, err := app.store.PluginLogsPage(billing.PluginLogQuery{Levels: []billing.PluginLogLevel{billing.PluginLogDebug}, Limit: 10})
	if err != nil || len(page.Entries) != 0 {
		t.Fatalf("entries=%+v err=%v", page.Entries, err)
	}
}

func TestAfterAuthEnforcesCredentialRoutesAndRecordsResults(t *testing.T) {
	ref := billing.CredentialFingerprint
	files := billing.CredentialProviderSelector{Source: billing.CredentialSourceAuthFiles, Provider: "codex"}
	config := billing.CredentialProviderSelector{Source: billing.CredentialSourceAIProviders, Provider: "codex"}
	for _, tc := range []struct {
		name        string
		rule        billing.RouteRule
		id          string
		candidate   *SchedulerAuthCandidate
		displayOnly bool
		runtime     string
		want        string
		wantCalls   int
		debugOff    bool
	}{
		{name: "exact allow", rule: billing.RouteRule{CredentialIDs: []string{ref("A")}}, id: "A", want: "allow"},
		{name: "issue 28 blacklist", rule: billing.RouteRule{CredentialIDs: []string{ref("A")}, DeniedCredentialIDs: []string{ref("B"), ref("C")}}, id: "B", want: "deny", debugOff: true},
		{name: "outside whitelist", rule: billing.RouteRule{CredentialIDs: []string{ref("A")}}, id: "C", want: "deny"},
		{name: "missing selected identity", rule: billing.RouteRule{CredentialIDs: []string{ref("A")}}, want: "unverified"},
		{name: "exact allow needs no category lookup", rule: billing.RouteRule{CredentialIDs: []string{ref("A")}, CredentialProviders: []billing.CredentialProviderSelector{files}}, id: "A", want: "allow"},
		{name: "exact deny needs no category lookup", rule: billing.RouteRule{DeniedCredentialIDs: []string{ref("B")}, DeniedCredentialProviders: []billing.CredentialProviderSelector{files}}, id: "B", want: "deny"},
		{name: "host configuration identity survives display sync", rule: billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{config}}, id: "config-A", candidate: &SchedulerAuthCandidate{ID: "config-A", Provider: "codex", Attributes: map[string]string{"source_backend": "config"}}, displayOnly: true, want: "allow"},
		{name: "display data is not authorization", rule: billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{config}}, id: "config-A", displayOnly: true, want: "unverified", wantCalls: 1},
		{name: "current file identity overrides cache", rule: billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{files}}, id: "file-A", candidate: &SchedulerAuthCandidate{ID: "file-A", Provider: "claude", Attributes: map[string]string{"source_backend": "file"}}, runtime: `{"auth":{"id":"file-A","auth_index":"selected-index","provider":"codex","source":"file","path":"/auth/dummy.json"}}`, want: "allow", wantCalls: 1},
		{name: "category deny overrides exact allow", rule: billing.RouteRule{CredentialIDs: []string{ref("file-A")}, DeniedCredentialProviders: []billing.CredentialProviderSelector{files}}, id: "file-A", runtime: `{"auth":{"id":"file-A","auth_index":"selected-index","provider":"codex","source":"memory","path":"/auth/dummy.json"}}`, want: "deny", wantCalls: 1},
		{name: "runtime credential", rule: billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{config}}, id: "memory-A", runtime: `{"auth":{"id":"memory-A","auth_index":"selected-index","provider":"codex","runtime_only":true,"source":"memory"}}`, want: "allow", wantCalls: 1},
		{name: "runtime unavailable", rule: billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{files}}, id: "file-A", want: "unverified", wantCalls: 1},
		{name: "runtime identity mismatch", rule: billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{files}}, id: "file-A", runtime: `{"auth":{"id":"file-B","auth_index":"selected-index","provider":"codex","source":"file"}}`, want: "unverified", wantCalls: 1},
		{name: "runtime index mismatch", rule: billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{files}}, id: "file-A", runtime: `{"auth":{"id":"file-A","auth_index":"other-index","provider":"codex","source":"file"}}`, want: "unverified", wantCalls: 1},
		{name: "incomplete runtime identity", rule: billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{files}}, id: "file-A", runtime: `{"auth":{"id":"file-A","auth_index":"selected-index","source":"file"}}`, want: "unverified", wantCalls: 1},
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
			if tc.candidate != nil {
				app.observeCandidates([]SchedulerAuthCandidate{*tc.candidate})
			}
			if tc.displayOnly {
				app.routingMu.Lock()
				provider := "codex"
				if tc.candidate != nil {
					provider = "claude"
				}
				app.replaceSyncedCredentials(nil, map[string]billing.ConfigCredential{ref(tc.id): {ConfigCredentialDetails: billing.ConfigCredentialDetails{Provider: provider}}})
				app.routingMu.Unlock()
			}
			calls := 0
			app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
				calls++
				if method != hostAuthGetRuntime || payload.(map[string]string)["auth_index"] != "selected-index" {
					t.Fatalf("unexpected host call: %s %+v", method, payload)
				}
				if tc.runtime == "" {
					return nil, fmt.Errorf("dummy upstream secret must not appear in logs")
				}
				return json.RawMessage(tc.runtime), nil
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
			// The executor may receive a different model alias. Only credential
			// permissions are checked here, using the exact selected host identity.
			req.Model = "actual-upstream-model"
			req.Metadata[MetadataSelectedAuth] = tc.id
			req.Metadata[MetadataSelectedIndex] = "selected-index"
			raw, err = app.HandleMethod(MethodRequestInterceptAfter, mustMarshal(t, req))
			if err != nil {
				t.Fatal(err)
			}
			decodeResult(t, raw, &response)
			rejected := tc.want != "allow"
			if response.Terminate != rejected || rejected && response.StatusCode != 503 {
				t.Fatalf("response=%+v want=%s", response, tc.want)
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
			if calls != tc.wantCalls {
				t.Fatalf("host calls=%d want=%d", calls, tc.wantCalls)
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
			if err != nil || len(page.Entries) != 1 {
				t.Fatalf("entries=%+v err=%v", page.Entries, err)
			}
			var row map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(page.Entries[0].Message, "route ")), &row); err != nil {
				t.Fatal(err)
			}
			if row["credential_policy"] != "restricted" || row["credential_result"] != tc.want || row["outcome"] != outcome {
				t.Fatalf("row=%+v", row)
			}
			if tc.candidate != nil && tc.want == "allow" && !strings.HasPrefix(row["selected_credential"].(string), "codex · ") {
				t.Fatalf("selected credential used a stale or UI-supplied provider: %+v", row)
			}
			if rejected && (page.Entries[0].Level != billing.PluginLogError || row["credential_error"] == nil) {
				t.Fatalf("rejection is not an actionable error: %+v", page.Entries[0])
			}
			if strings.Contains(page.Entries[0].Message, "dummy upstream secret") || strings.Contains(string(response.ResponseBody), "dummy upstream secret") {
				t.Fatal("host error leaked into public diagnostics")
			}
			if len(requestEventEntries(t, app)) != 0 {
				t.Fatal("routing bookkeeping fabricated provider usage")
			}
		})
	}
}

func TestMissingRoutedCredentialIsExplicitInLog(t *testing.T) {
	levels := map[routingShortfall]billing.PluginLogLevel{
		routeUnavailable: billing.PluginLogInfo, routeNoCredential: billing.PluginLogError, routeNoMatch: billing.PluginLogInfo,
	}
	for _, shortfall := range routingShortfalls {
		t.Run(shortfall.result, func(t *testing.T) {
			app, scope := configuredRoutingApp(t, billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAuthFiles, Provider: "codex"}}})
			if _, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{RequestID: "no-match", Model: "codex/deepseek", RequestedModel: "codex/deepseek", Metadata: map[string]any{MetadataCallerScope: scope}})); err != nil {
				t.Fatal(err)
			}
			if _, err := app.HandleMethod(MethodRequestComplete, mustMarshal(t, RequestCompletion{RequestID: "no-match", Outcome: "failed", StatusCode: 503, Error: shortfall.message})); err != nil {
				t.Fatal(err)
			}
			page, err := app.store.PluginLogsPage(billing.PluginLogQuery{Limit: 10})
			if err != nil || len(page.Entries) == 0 || page.Entries[0].Level != levels[shortfall] {
				t.Fatalf("entries=%+v err=%v", page.Entries, err)
			}
			var row map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(page.Entries[0].Message, "route ")), &row); err != nil {
				t.Fatal(err)
			}
			if row["credential_result"] != shortfall.result {
				t.Fatalf("row=%+v", row)
			}
		})
	}
}

func TestRejectedModelLogsCredentialStageAsNotReached(t *testing.T) {
	app, scope := configuredRoutingApp(t, billing.RouteRule{Models: []string{"deepseek-v4"}, CredentialIDs: []string{billing.CredentialFingerprint("auth-a")}})
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{RequestID: "model-denied", SourceFormat: "openai", Model: "gpt-5.6", RequestedModel: "gpt-5.6", Metadata: map[string]any{MetadataCallerScope: scope}}))
	if err != nil {
		t.Fatal(err)
	}
	var response RequestInterceptResponse
	decodeResult(t, raw, &response)
	if !response.Terminate || response.StatusCode != 403 {
		t.Fatalf("response=%+v", response)
	}
	if _, err := app.HandleMethod(MethodRequestComplete, mustMarshal(t, RequestCompletion{RequestID: "model-denied", Outcome: "rejected", StatusCode: 403})); err != nil {
		t.Fatal(err)
	}
	page, err := app.store.PluginLogsPage(billing.PluginLogQuery{Levels: []billing.PluginLogLevel{billing.PluginLogDebug}, Limit: 10})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("entries=%+v err=%v", page.Entries, err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(page.Entries[0].Message, "route ")), &row); err != nil {
		t.Fatal(err)
	}
	if row["model_result"] != "deny" || row["credential_result"] != "not_reached" {
		t.Fatalf("row=%+v", row)
	}
}

func TestRoutingDebugLogMasksSecretLikeLabelsAndModels(t *testing.T) {
	const secret = "sk-super-secret-value-123456"
	app, scope := configuredRoutingApp(t, billing.RouteRule{Models: []string{secret}})
	if err := app.store.SetLabel(scope, "owner@example.com "+secret); err != nil {
		t.Fatal(err)
	}
	if _, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{RequestID: "secret-log", SourceFormat: "openai", Model: secret, RequestedModel: secret, Metadata: map[string]any{MetadataCallerScope: scope}})); err != nil {
		t.Fatal(err)
	}
	if _, err := app.HandleMethod(MethodRequestComplete, mustMarshal(t, RequestCompletion{RequestID: "secret-log", Outcome: "succeeded", StatusCode: 200})); err != nil {
		t.Fatal(err)
	}
	page, err := app.store.PluginLogsPage(billing.PluginLogQuery{Levels: []billing.PluginLogLevel{billing.PluginLogDebug}, Limit: 10})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	for _, plaintext := range []string{secret, "owner@example.com"} {
		if strings.Contains(page.Entries[0].Message, plaintext) {
			t.Fatalf("routing log leaked %q: %s", plaintext, page.Entries[0].Message)
		}
	}
}

func TestAIProviderCredentialDisplayNameMasksKeysAndAccounts(t *testing.T) {
	name := safeCredentialName("codex-sk-secret1234-private@example.com.json", "private@example.com", "codex", billing.CredentialFingerprint("id"))
	for _, secret := range []string{"sk-secret1234", "private@example.com"} {
		if strings.Contains(name, secret) {
			t.Fatalf("safe name %q leaked %q", name, secret)
		}
	}
}

// Auth files are named like CPAMC names them, and an email is never parsed out
// of a file name.
func TestAuthFileCredentialDisplayNameFollowsCPAMC(t *testing.T) {
	const fileName = "codex-1dfbc38b-wrong@example.com-pro.json"
	for _, test := range []struct {
		file hostAuthFile
		want string
	}{
		{hostAuthFile{Name: fileName, Label: "private label", Email: "user@example.com", ProjectID: "project-1"}, "user@example.com"},
		{hostAuthFile{Name: fileName, ProjectID: "project-1"}, "project-1"},
		{hostAuthFile{Name: fileName, Account: "sk-upstream-secret-value"}, "codex-1dfbc38b-wrong@example.com-pro"},
	} {
		if got := credentialDisplayName(test.file, billing.CredentialSourceAuthFiles, "codex", billing.CredentialFingerprint("id")); got != test.want {
			t.Fatalf("name = %q, want %q", got, test.want)
		}
	}
}

func TestCredentialLogNameKeepsAuthFileEmailAndMasksAIProvider(t *testing.T) {
	if got := credentialLogName(credentialView{Source: billing.CredentialSourceAuthFiles, ProviderName: "codex", DisplayName: "user@example.com"}); got != "codex · user@example.com" {
		t.Fatalf("auth-file log name=%q", got)
	}
	const secret = "sk-super-secret-value-123456"
	got := credentialLogName(credentialView{Source: billing.CredentialSourceAIProviders, ProviderName: "codex", DisplayName: secret})
	if strings.Contains(got, secret) {
		t.Fatalf("AI Provider log name leaked secret: %q", got)
	}
}

func TestAIProviderCredentialLearnsMaskedAPIKeyFromUsage(t *testing.T) {
	app := newConfiguredApp(t)
	const upstreamKey = "sk-dummy-upstream-secret-1234"
	app.observeCandidates([]SchedulerAuthCandidate{{ID: "config-codex", Provider: "codex", Attributes: map[string]string{"source": "config:codex[abc]"}}})
	app.observeRouteCredential(RequestInterceptRequest{Metadata: map[string]any{MetadataSelectedAuth: "config-codex", MetadataSelectedIndex: "auth-index"}}, "unrestricted", "")
	publishUsageRecord(t, app, UsageRecord{Provider: "codex", APIKey: "downstream-key", AuthIndex: "auth-index", AuthType: "apikey", Source: upstreamKey})

	credentials := app.credentialInventory()
	if len(credentials) != 1 || credentials[0].DisplayName != billing.PreviewKey(upstreamKey) {
		t.Fatalf("credentials=%+v", credentials)
	}
	if strings.Contains(credentials[0].DisplayName, upstreamKey) {
		t.Fatalf("credential display name leaked upstream key: %q", credentials[0].DisplayName)
	}

	publishUsageRecord(t, app, UsageRecord{Provider: "codex", APIKey: "downstream-key", AuthIndex: "auth-index", AuthType: "apikey", Source: "downstream-key"})
	if got := app.credentialInventory()[0].DisplayName; got != billing.PreviewKey(upstreamKey) {
		t.Fatalf("downstream key fallback replaced credential display name: %q", got)
	}
}

func TestCredentialSourceClassification(t *testing.T) {
	tests := []struct {
		name       string
		attributes map[string]string
		want       string
	}{
		{name: "config backend", attributes: map[string]string{"source_backend": "config"}, want: billing.CredentialSourceAIProviders},
		{name: "config source", attributes: map[string]string{"source": "config:codex[0]"}, want: billing.CredentialSourceAIProviders},
		{name: "runtime backend", attributes: map[string]string{"source_backend": "memory"}, want: billing.CredentialSourceAIProviders},
		{name: "runtime marker", attributes: map[string]string{"runtime_only": "true", "path": "/ignored"}, want: billing.CredentialSourceAIProviders},
		{name: "file backend", attributes: map[string]string{"source_backend": "file"}, want: billing.CredentialSourceAuthFiles},
		{name: "remote auth file", attributes: map[string]string{"source_backend": "postgres"}, want: billing.CredentialSourceAuthFiles},
		{name: "path", attributes: map[string]string{"path": "/auth/codex.json", "auth_kind": "apikey"}, want: billing.CredentialSourceAuthFiles},
		{name: "auth kind is not source", attributes: map[string]string{"auth_kind": "apikey"}, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := credentialSourceFromCandidate(SchedulerAuthCandidate{Attributes: test.attributes})
			if got != test.want {
				t.Fatalf("source=%q, want %q", got, test.want)
			}
		})
	}
	if got := credentialSourceFromHost(hostAuthFile{RuntimeOnly: true, Source: "file", Path: "/ignored"}); got != billing.CredentialSourceAIProviders {
		t.Fatalf("runtime-only host credential source=%q", got)
	}
}

func schedulerRequest(scope string, candidates ...SchedulerAuthCandidate) SchedulerPickRequest {
	request := SchedulerPickRequest{Model: "gpt-5.6", Candidates: candidates}
	request.Options.Metadata = map[string]any{MetadataCallerScope: scope}
	return request
}

func TestSchedulerSeparatesSameProviderBySource(t *testing.T) {
	app, scope := configuredRoutingApp(t, billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAIProviders, Provider: "codex"}}})
	req := schedulerRequest(scope,
		SchedulerAuthCandidate{ID: "file-codex", Provider: "codex", Attributes: map[string]string{"path": "/auth/codex.json"}},
		SchedulerAuthCandidate{ID: "config-codex", Provider: "codex", Attributes: map[string]string{"source": "config:codex[abc]"}},
	)
	raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, req))
	if err != nil {
		t.Fatal(err)
	}
	var response SchedulerPickResponse
	decodeResult(t, raw, &response)
	if !response.Handled || response.AuthID != "config-codex" {
		t.Fatalf("response=%+v", response)
	}
}

func TestUnclassifiedCandidateRequiresExactAllowlistReference(t *testing.T) {
	candidate := SchedulerAuthCandidate{ID: "opaque-auth", Provider: "codex", Attributes: map[string]string{"auth_kind": "apikey"}}
	providerOnly := billing.RoutingDecision{RouteRule: billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAIProviders, Provider: "codex"}}}}
	if candidateAllowed(candidate, providerOnly) {
		t.Fatal("unclassified candidate matched a provider selector")
	}
	exact := billing.RoutingDecision{RouteRule: billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint(candidate.ID)}}}
	if !candidateAllowed(candidate, exact) {
		t.Fatal("unclassified candidate did not match its exact fingerprint")
	}
}

func TestSchedulerKeepsRestrictedPoolsLocal(t *testing.T) {
	for _, tc := range []struct {
		rule    billing.RouteRule
		handled bool
	}{
		{billing.RouteRule{}, false},
		{billing.RouteRule{Models: []string{"gpt-5.6"}}, false},
		{billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAIProviders, Provider: "codex"}}}, true},
		{billing.RouteRule{DeniedCredentialIDs: []string{billing.CredentialFingerprint("outside")}}, true},
	} {
		app, scope := configuredRoutingApp(t, tc.rule)
		raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, schedulerRequest(scope, SchedulerAuthCandidate{ID: "config-codex", Provider: "codex", Attributes: map[string]string{"source": "config:codex[abc]"}})))
		if err != nil {
			t.Fatal(err)
		}
		var response SchedulerPickResponse
		decodeResult(t, raw, &response)
		if response.Handled != tc.handled || (tc.handled && response.AuthID != "config-codex") {
			t.Fatalf("rule=%+v response=%+v", tc.rule, response)
		}
	}
}

func TestSchedulerCredentialPolicyIsIndependentOfRequestedModel(t *testing.T) {
	app, scope := configuredRoutingApp(t, billing.RouteRule{
		Models: []string{"public-model"},
		CredentialProviders: []billing.CredentialProviderSelector{{
			Source: billing.CredentialSourceAuthFiles, Provider: "codex",
		}},
	})
	if err := app.store.SetKeyRoutes(scope, billing.RouteBindings{RouteIDs: []string{"route-test"}, RouteRule: billing.RouteRule{Models: []string{"direct-model"}}}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		upstream, requested string
	}{
		{upstream: "public-model"},
		{upstream: "direct-model"},
		{upstream: "upstream-model", requested: "public-model"},
		{upstream: "upstream-model", requested: "direct-model"},
	} {
		t.Run(test.upstream+"/"+test.requested, func(t *testing.T) {
			req := schedulerRequest(scope,
				SchedulerAuthCandidate{ID: "file-codex", Provider: "codex", Attributes: map[string]string{"path": "/auth/codex.json"}},
				SchedulerAuthCandidate{ID: "config-codex", Provider: "codex", Attributes: map[string]string{"source": "config:codex[0]"}},
			)
			req.Model = test.upstream
			req.Options.Metadata[MetadataRequestedModel] = test.requested
			raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, req))
			if err != nil {
				t.Fatal(err)
			}
			var response SchedulerPickResponse
			decodeResult(t, raw, &response)
			if !response.Handled || response.AuthID != "file-codex" {
				t.Fatalf("response=%+v", response)
			}
		})
	}
}

func TestSchedulerUsesCredentialsFromEveryBoundRoute(t *testing.T) {
	app, scope := configuredRoutingApp(t, billing.RouteRule{
		Models: []string{"model-a"}, CredentialIDs: []string{billing.CredentialFingerprint("auth-a")},
	})
	if _, err := app.store.CreateRoute(billing.Route{ID: "route-b", Name: "Route B", Rule: billing.RouteRule{
		Models: []string{"model-b"}, CredentialIDs: []string{billing.CredentialFingerprint("auth-b")},
	}}, []string{scope}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		model, credential string
	}{
		{model: "model-a", credential: "auth-b"},
		{model: "model-b", credential: "auth-a"},
	} {
		t.Run(test.model, func(t *testing.T) {
			candidate := SchedulerAuthCandidate{ID: test.credential, Provider: "codex"}
			req := schedulerRequest(scope, candidate, SchedulerAuthCandidate{ID: "outside", Provider: "codex"})
			req.Model = test.model
			raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, req))
			if err != nil {
				t.Fatal(err)
			}
			var response SchedulerPickResponse
			decodeResult(t, raw, &response)
			if !response.Handled || response.AuthID != test.credential {
				t.Fatalf("credential from another route was not selected: %+v", response)
			}
		})
	}
}

// Hosts offer only usable candidates; host.auth.models tells whether an allowed
// credential serves the model at all.
func TestSchedulerFailsClosedWithoutAUsableRoutedCandidate(t *testing.T) {
	const serves = `{"credentials":[{"id":"wanted","provider":"codex","models":["gpt-5.6"]},{"id":"a","provider":"codex","models":["gpt-5.6"]},{"id":"outside","provider":"codex","models":["gpt-5.6"]}]}`
	tests := []struct {
		name       string
		rule       billing.RouteRule
		candidates []SchedulerAuthCandidate
		models     string
		want       routingShortfall
	}{
		{
			name: "older host",
			rule: billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("wanted")}},
			candidates: []SchedulerAuthCandidate{
				{ID: "other", Provider: "codex", Attributes: map[string]string{"path": "/other"}},
			},
			want: routeNoMatch,
		},
		{
			name: "allowed credential unavailable",
			rule: billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("wanted")}},
			candidates: []SchedulerAuthCandidate{
				{ID: "other", Provider: "codex", Attributes: map[string]string{"path": "/other"}},
			},
			models: serves,
			want:   routeUnavailable,
		},
		{
			name: "all denied",
			rule: billing.RouteRule{DeniedCredentialIDs: []string{billing.CredentialFingerprint("wanted"), billing.CredentialFingerprint("a"), billing.CredentialFingerprint("outside")}},
			candidates: []SchedulerAuthCandidate{
				{ID: "outside", Provider: "codex", Attributes: map[string]string{"path": "/outside"}},
			},
			models: serves,
			want:   routeNoCredential,
		},
		{
			name: "non-positive weight",
			rule: billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("a")}},
			candidates: []SchedulerAuthCandidate{
				{ID: "a", Provider: "codex", Attributes: map[string]string{"path": "/a", "weight": "0"}},
				{ID: "outside", Provider: "codex", Attributes: map[string]string{"path": "/outside"}},
			},
			models: serves,
			want:   routeUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, scope := configuredRoutingApp(t, test.rule)
			app.scheduler.configure(billing.RoutingConfig{Strategy: "weighted-round-robin", SessionAffinityTTL: "1h"}, true)
			if test.models != "" {
				app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
					if method != hostAuthModels {
						t.Fatalf("host method = %q", method)
					}
					return json.RawMessage(test.models), nil
				})
				app.authModelsSupported.Store(true)
			}
			raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, schedulerRequest(scope, test.candidates...)))
			if err != nil {
				t.Fatal(err)
			}
			var envelope Envelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.OK || envelope.Error == nil || envelope.Error.HTTPStatus != 503 || envelope.Error.Message != test.want.message {
				t.Fatalf("envelope=%+v, want %q", envelope, test.want.message)
			}
		})
	}
}

func TestSchedulerUsesConfiguredStrategies(t *testing.T) {
	candidates := []SchedulerAuthCandidate{
		{ID: "a", Provider: "codex", Attributes: map[string]string{"path": "/a", "weight": "2"}},
		{ID: "b", Provider: "codex", Attributes: map[string]string{"path": "/b", "weight": "1"}},
		{ID: "outside", Provider: "codex", Attributes: map[string]string{"path": "/outside", "weight": "1000"}},
	}
	for _, test := range []struct {
		name string
		rule billing.RouteRule
	}{
		{"allow", billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("a"), billing.CredentialFingerprint("b")}}},
		{"deny", billing.RouteRule{DeniedCredentialIDs: []string{billing.CredentialFingerprint("outside")}}},
		{"provider with exception", billing.RouteRule{
			CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAuthFiles, Provider: "codex"}},
			DeniedCredentialIDs: []string{billing.CredentialFingerprint("outside")},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, strategy := range []struct {
				name string
				a, b int
			}{
				{"round-robin", 15, 15}, {"weighted-round-robin", 20, 10}, {"fill-first", 30, 0},
			} {
				t.Run(strategy.name, func(t *testing.T) {
					app, scope := configuredRoutingApp(t, test.rule)
					app.scheduler.configure(billing.RoutingConfig{Strategy: strategy.name, SessionAffinityTTL: "1h"}, true)
					counts := map[string]int{}
					for range 30 {
						raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, schedulerRequest(scope, candidates...)))
						if err != nil {
							t.Fatal(err)
						}
						var response SchedulerPickResponse
						decodeResult(t, raw, &response)
						if !response.Handled {
							t.Fatalf("response=%+v, want routed selection", response)
						}
						counts[response.AuthID]++
					}
					if counts["a"] != strategy.a || counts["b"] != strategy.b || counts["outside"] != 0 {
						t.Fatalf("counts=%v", counts)
					}
				})
			}
		})
	}
	for _, strategy := range []string{"round-robin", "fill-first", "weighted-round-robin"} {
		var scheduler subsetScheduler
		scheduler.configure(billing.RoutingConfig{Strategy: strategy, SessionAffinityTTL: "1h"}, true)
		want := "a"
		if strategy == "weighted-round-robin" {
			want = "b"
		}
		got := scheduler.pick("key", "pool", "", []SchedulerAuthCandidate{{ID: "a", Priority: 10, Attributes: map[string]string{"weight": "0"}}, {ID: "b"}}, time.Now())
		if got != want {
			t.Fatalf("%s: zero-weight priority picked %q, want %q", strategy, got, want)
		}
	}
}

func TestSubsetSchedulerKeepsAPIKeysAndTemporaryCandidateChangesIndependent(t *testing.T) {
	var scheduler subsetScheduler
	scheduler.configure(billing.RoutingConfig{Strategy: "weighted-round-robin", SessionAffinityTTL: "1h"}, true)
	now := time.Now()
	candidates := []SchedulerAuthCandidate{
		{ID: "a", Attributes: map[string]string{"weight": "1"}},
		{ID: "b", Attributes: map[string]string{"weight": "1"}},
	}
	firstA := scheduler.pick("key-a", "pool", "", candidates, now)
	firstB := scheduler.pick("key-b", "pool", "", candidates, now)
	if firstA != "a" || firstB != "a" {
		t.Fatalf("first picks key-a=%q key-b=%q, want independent pools", firstA, firstB)
	}
	if picked := scheduler.pick("key-a", "pool", "", candidates[1:], now); picked != "b" {
		t.Fatalf("temporary subset pick=%q", picked)
	}
	if picked := scheduler.pick("key-a", "pool", "", candidates, now); picked != "b" {
		t.Fatalf("restored candidate pick=%q, want preserved smooth scores", picked)
	}
	if picked := scheduler.pick("key-b", "pool", "", candidates, now); picked != "b" {
		t.Fatalf("key-b second pick=%q, key-a must not advance it", picked)
	}
	if picked := scheduler.pick("key-a", "pool", "", candidates, now); picked != "a" {
		t.Fatalf("key-a third pick=%q", picked)
	}
	scheduler.prune(map[string]struct{}{"key-a": {}})
	if picked := scheduler.pick("key-a", "pool", "", candidates, now); picked != "b" {
		t.Fatalf("retained key pick=%q", picked)
	}
	if picked := scheduler.pick("key-b", "pool", "", candidates, now); picked != "a" {
		t.Fatalf("pruned key pick=%q", picked)
	}
	// Prune both score maps even when repeated weight changes reset Current.
	for i := 0; i < maxCredentialsPerPool+3; i++ {
		scheduler.pick("key-a", "weights", "", []SchedulerAuthCandidate{
			{ID: "a", Attributes: map[string]string{"weight": fmt.Sprint(1 + i%2)}},
			{ID: fmt.Sprintf("rotating-%d", i)},
		}, now)
	}
	if state := scheduler.keys["key-a"]["weights"]; len(state.Current) > maxCredentialsPerPool || len(state.Weights) > maxCredentialsPerPool {
		t.Fatalf("unbounded weight history: current=%d weights=%d", len(state.Current), len(state.Weights))
	}
	// Pool eviction must preserve progress for recently used models.
	scheduler.configure(billing.RoutingConfig{Strategy: "round-robin", SessionAffinityTTL: "1h"}, true)
	scheduler.pick("key-a", "active", "", candidates, now)
	for i := 0; i < maxPoolsPerKey; i++ {
		scheduler.pick("key-a", fmt.Sprintf("pool-%d", i), "", candidates, now.Add(time.Duration(i)*time.Second))
		if i == maxPoolsPerKey-2 {
			scheduler.pick("key-a", "active", "", candidates, now.Add(time.Duration(i)*time.Second))
		}
	}
	if len(scheduler.keys["key-a"]) != maxPoolsPerKey || scheduler.keys["key-a"]["active"] == nil || scheduler.keys["key-a"]["pool-0"] != nil {
		t.Fatal("pool eviction discarded recent progress or exceeded capacity")
	}
}

// Issue #25: route membership is applied before priority, for Codex auth files
// as well as configured providers. Unavailable candidates are removed by CPA.
func TestRoutePriorityIssue25(t *testing.T) {
	a := SchedulerAuthCandidate{ID: "codex-a", Provider: "codex", Priority: 10, Attributes: map[string]string{"source_backend": "file"}}
	b := SchedulerAuthCandidate{ID: "codex-b", Provider: "codex", Priority: 0, Attributes: map[string]string{"source_backend": "file"}}
	for _, tc := range []struct {
		name       string
		allowed    []string
		candidates []SchedulerAuthCandidate
		want       string
	}{
		{"only A", []string{a.ID}, []SchedulerAuthCandidate{a, b}, a.ID},
		{"only B", []string{b.ID}, []SchedulerAuthCandidate{a, b}, b.ID},
		{"both preserve host priority", []string{a.ID, b.ID}, []SchedulerAuthCandidate{a, b}, a.ID},
		{"A disabled or cooling down", []string{a.ID, b.ID}, []SchedulerAuthCandidate{b}, b.ID},
		{"allowed subset retains priority", []string{a.ID, b.ID}, []SchedulerAuthCandidate{a, b, {ID: "outside", Provider: "codex", Priority: 20}}, a.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids := []string{}
			for _, id := range tc.allowed {
				ids = append(ids, billing.CredentialFingerprint(id))
			}
			app, scope := configuredRoutingApp(t, billing.RouteRule{CredentialIDs: ids})
			raw, err := app.pickCredential(mustMarshal(t, SchedulerPickRequest{Model: "gpt-5.6", Options: struct {
				Metadata map[string]any `json:"Metadata"`
			}{map[string]any{MetadataCallerScope: scope}}, Candidates: tc.candidates}))
			if err != nil {
				t.Fatal(err)
			}
			var result SchedulerPickResponse
			decodeResult(t, raw, &result)
			if !result.Handled || result.AuthID != tc.want {
				t.Fatalf("decision=%+v", result)
			}
		})
	}
}

func TestSchedulerSessionAffinity(t *testing.T) {
	var scheduler subsetScheduler
	scheduler.configure(billing.RoutingConfig{Strategy: "round-robin", SessionAffinity: true, SessionAffinityTTL: "10s"}, true)
	a := SchedulerAuthCandidate{ID: "a", Priority: 10}
	b := SchedulerAuthCandidate{ID: "b", Priority: 0}
	equal := []SchedulerAuthCandidate{a, {ID: "b", Priority: 10}}
	now := time.Now()
	for _, tc := range []struct {
		name, scope, pool, session string
		candidates                 []SchedulerAuthCandidate
		seconds                    int
		want                       string
	}{
		{"cold low priority", "key-a", "pool", "session", []SchedulerAuthCandidate{b}, 0, "b"},
		{"binding survives higher priority recovery", "key-a", "pool", "session", []SchedulerAuthCandidate{a, b}, 1, "b"},
		{"new session uses highest priority", "key-a", "pool", "new", []SchedulerAuthCandidate{a, b}, 2, "a"},
		{"failover", "key-a", "pool", "session", []SchedulerAuthCandidate{a}, 3, "a"},
		{"new binding retained", "key-a", "pool", "session", []SchedulerAuthCandidate{a, b}, 4, "a"},
		{"caller isolation", "key-b", "pool", "session", equal, 5, "a"},
		{"route isolation", "key-a", "another-pool", "session", equal, 5, "a"},
		{"ttl bind", "key-a", "ttl", "session", equal, 10, "a"},
		{"missing session rotates normally", "key-a", "ttl", "", equal, 15, "b"},
		{"ttl refreshed on hit", "key-a", "ttl", "session", equal, 19, "a"},
		{"anonymous rotation advances", "key-a", "ttl", "", equal, 20, "a"},
		{"binding outlives original expiry", "key-a", "ttl", "session", equal, 21, "a"},
		{"idle expiry reselects", "key-a", "ttl", "session", equal, 32, "b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := scheduler.pick(tc.scope, tc.pool, tc.session, tc.candidates, now.Add(time.Duration(tc.seconds)*time.Second)); got != tc.want {
				t.Fatalf("picked %q, want %q", got, tc.want)
			}
		})
	}
	scheduler.pick("key-b", "pool", "session", equal, now.Add(33*time.Second))
	scheduler.prune(map[string]struct{}{"key-a": {}})
	for key := range scheduler.sessions {
		if key.Scope != "key-a" {
			t.Fatal("removed caller retained a session binding")
		}
	}
}

func TestSchedulerConfigurationUpdatesAndSessionMetadata(t *testing.T) {
	app, path := newAppWithPriceAndState(t, true)
	candidates := []SchedulerAuthCandidate{{ID: "a"}, {ID: "b"}}
	if _, err := app.store.SyncKeys([]string{"dummy-scheduler-key"}, false); err != nil {
		t.Fatal(err)
	}
	caller := billing.CallerScope("dummy-scheduler-key")
	if _, err := app.store.CreateRoute(billing.Route{Name: "restricted", Rule: billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("a"), billing.CredentialFingerprint("b")}}}, []string{caller}); err != nil {
		t.Fatal(err)
	}
	reconfigure := func(strategy, ttl string, affinity bool) error {
		raw := mustMarshal(t, LifecycleRequest{ConfigYAML: []byte(fmt.Sprintf("enabled: true\nstate_file: %q\nrouting:\n  strategy: %s\n  session-affinity: %t\n  session-affinity-ttl: %s\n", path, strategy, affinity, ttl))})
		_, err := app.HandleMethod(MethodPluginReconfigure, raw)
		return err
	}
	if err := reconfigure("round-robin", "1h", true); err != nil {
		t.Fatal(err)
	}
	req := schedulerRequest(caller, candidates...)
	req.Options.Metadata["canonical_session_id"] = "session"
	pick := func() string {
		raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, req))
		if err != nil {
			t.Fatal(err)
		}
		var result SchedulerPickResponse
		decodeResult(t, raw, &result)
		if !result.Handled {
			t.Fatal("restricted route delegated to host")
		}
		return result.AuthID
	}
	if pick() != "a" || pick() != "a" {
		t.Fatal("scheduler did not consume host session metadata")
	}
	if err := reconfigure("rr", "60m", true); err != nil {
		t.Fatal(err)
	}
	if len(app.scheduler.sessions) != 1 {
		t.Fatal("equivalent reconfiguration lost session state")
	}
	if err := reconfigure("invalid", "1h", true); err == nil {
		t.Fatal("accepted invalid routing strategy")
	}
	if pick() != "a" {
		t.Fatal("failed reconfiguration changed active bindings")
	}
	for _, tc := range []struct {
		metadata map[string]any
		want     string
	}{
		{map[string]any{"derived_session_id": "session"}, "b"},
		{map[string]any{"execution_session_id": "session", "derived_session_id": "session"}, "a"},
		{map[string]any{"canonical_session_id": "session", "execution_session_id": "other"}, "a"},
		{map[string]any{"canonical_session_id": false, "derived_session_id": "session"}, "b"},
	} {
		req.Options.Metadata = tc.metadata
		req.Options.Metadata[MetadataCallerScope] = caller
		if got := pick(); got != tc.want {
			t.Fatalf("session identity precedence or isolation failed: metadata=%+v picked=%q", tc.metadata, got)
		}
	}
	// Model spelling, provider and thinking suffixes follow CPA's pool identity;
	// billing-price normalization must not merge case-sensitive model routes.
	req.Options.Metadata = map[string]any{MetadataCallerScope: caller, "derived_session_id": "session"}
	for _, tc := range []struct{ model, provider, want string }{
		{"GPT-5.6", "", "a"},
		{"gpt-5.6", "", "b"},
		{"gpt-5.6(high)", "", "b"},
		{"gpt-5.6", "codex", "a"},
	} {
		req.Model, req.Provider = tc.model, tc.provider
		if got := pick(); got != tc.want {
			t.Fatalf("model=%q provider=%q picked=%q, want %q", tc.model, tc.provider, got, tc.want)
		}
	}
	req.Model, req.Provider = "gpt-5.6", ""
	req.Options.Metadata = map[string]any{MetadataCallerScope: caller, "canonical_session_id": "session"}
	if err := reconfigure("round-robin", "1h", false); err != nil {
		t.Fatal(err)
	}
	if len(app.scheduler.sessions) != 0 {
		t.Fatal("disabling affinity retained bindings")
	}
	if pick() != "a" || pick() != "b" {
		t.Fatal("disabled affinity did not rotate")
	}
	if err := reconfigure("fill-first", "1h", true); err != nil {
		t.Fatal(err)
	}
	if pick() != "a" || pick() != "a" {
		t.Fatal("fill-first was not applied")
	}
	// Updating permissions must invalidate the old route namespace immediately.
	if err := app.store.SetKeyRoutes(caller, billing.RouteBindings{RouteRule: billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("b")}}}); err != nil {
		t.Fatal(err)
	}
	if pick() != "b" {
		t.Fatal("session escaped updated route permissions")
	}
}

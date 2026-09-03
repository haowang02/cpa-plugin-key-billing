package plugin

import (
	"encoding/json"
	"strings"
	"testing"

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

func TestAfterAuthSelectionIsRecordedForTheSameRequest(t *testing.T) {
	app, scope := configuredRoutingApp(t, billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAuthFiles, Provider: "codex"}}})
	app.observeCandidates([]SchedulerAuthCandidate{{ID: "codex-auth", Provider: "codex", Attributes: map[string]string{"path": "/auth/codex.json"}}})
	if _, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{RequestID: "selected-request", Model: "gpt-5.6", RequestedModel: "gpt-5.6", Metadata: map[string]any{MetadataCallerScope: scope}})); err != nil {
		t.Fatal(err)
	}
	if _, err := app.HandleMethod(MethodRequestInterceptAfter, mustMarshal(t, RequestInterceptRequest{RequestID: "selected-request", Metadata: map[string]any{MetadataSelectedAuth: "codex-auth"}})); err != nil {
		t.Fatal(err)
	}
	if _, err := app.HandleMethod(MethodRequestComplete, mustMarshal(t, RequestCompletion{RequestID: "selected-request", Outcome: "succeeded", StatusCode: 200})); err != nil {
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
	if row["credential_result"] != "selected" || row["selected_credential"] != "codex · 未提供邮箱" {
		t.Fatalf("row=%+v", row)
	}
}

func TestMissingRoutedCredentialIsExplicitInLog(t *testing.T) {
	app, scope := configuredRoutingApp(t, billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAuthFiles, Provider: "codex"}}})
	if _, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{RequestID: "no-match", Model: "codex/deepseek", RequestedModel: "codex/deepseek", Metadata: map[string]any{MetadataCallerScope: scope}})); err != nil {
		t.Fatal(err)
	}
	if _, err := app.HandleMethod(MethodRequestComplete, mustMarshal(t, RequestCompletion{RequestID: "no-match", Outcome: "failed", StatusCode: 503, Error: noRoutedCredentialMessage})); err != nil {
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
	if row["credential_result"] != "no_match" {
		t.Fatalf("row=%+v", row)
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

func TestRoutingConfigurationErrorIsExplicitInCombinedLog(t *testing.T) {
	app := newConfiguredApp(t)
	app.beginRouteLog("config-error", "scope", billing.RoutingDecision{Model: "gpt-5.6", ConfigurationError: "missing route"})
	app.finishRouteLog(RequestCompletion{RequestID: "config-error", Outcome: "rejected", StatusCode: 503})

	page, err := app.store.PluginLogsPage(billing.PluginLogQuery{Levels: []billing.PluginLogLevel{billing.PluginLogDebug}, Limit: 10})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("entries=%+v err=%v", page.Entries, err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(page.Entries[0].Message, "route ")), &row); err != nil {
		t.Fatal(err)
	}
	if row["model_result"] != "configuration_error" || row["credential_result"] != "configuration_error" {
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

func TestAuthFileCredentialDisplayNameUsesOnlyEmail(t *testing.T) {
	file := hostAuthFile{
		Name:    "codex-1dfbc38b-wrong@example.com-pro.json",
		Label:   "private label",
		Email:   "user@example.com",
		Account: "sk-upstream-secret-value",
	}
	name := credentialDisplayName(file, billing.CredentialSourceAuthFiles, "codex", billing.CredentialFingerprint("id"))
	if name != file.Email {
		t.Fatalf("name=%q, want email %q", name, file.Email)
	}
	if inferred := credentialDisplayName(hostAuthFile{Name: file.Name}, billing.CredentialSourceAuthFiles, "codex", billing.CredentialFingerprint("missing-email")); inferred != "未提供邮箱" {
		t.Fatalf("missing CPA email inferred from filename: %q", inferred)
	}
}

func TestCredentialLogNameKeepsAuthFileEmailAndMasksAIProvider(t *testing.T) {
	if got := credentialLogName(credentialView{Source: billing.CredentialSourceAuthFiles, Provider: "codex", DisplayName: "user@example.com"}); got != "codex · user@example.com" {
		t.Fatalf("auth-file log name=%q", got)
	}
	const secret = "sk-super-secret-value-123456"
	got := credentialLogName(credentialView{Source: billing.CredentialSourceAIProviders, Provider: "codex", DisplayName: secret})
	if strings.Contains(got, secret) {
		t.Fatalf("AI Provider log name leaked secret: %q", got)
	}
}

func TestAIProviderCredentialLearnsMaskedAPIKeyFromUsage(t *testing.T) {
	app := newConfiguredApp(t)
	const upstreamKey = "sk-dummy-upstream-secret-1234"
	app.observeCandidates([]SchedulerAuthCandidate{{ID: "config-codex", Provider: "codex", Attributes: map[string]string{"source": "config:codex[abc]"}}})
	app.observeRouteCredential("", "config-codex", "auth-index")
	app.observeCredentialUsage("auth-index", "apikey", upstreamKey, "downstream-scope")

	credentials := app.credentialInventory()
	if len(credentials) != 1 || credentials[0].DisplayName != billing.PreviewKey(upstreamKey) {
		t.Fatalf("credentials=%+v", credentials)
	}
	if strings.Contains(credentials[0].DisplayName, upstreamKey) {
		t.Fatalf("credential display name leaked upstream key: %q", credentials[0].DisplayName)
	}

	app.observeCredentialUsage("auth-index", "apikey", "downstream-key", billing.CallerScope("downstream-key"))
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

func TestUnclassifiedCandidateRequiresExactCredentialReference(t *testing.T) {
	candidate := SchedulerAuthCandidate{ID: "opaque-auth", Provider: "codex", Attributes: map[string]string{"auth_kind": "apikey"}}
	providerOnly := billing.RoutingDecision{CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAIProviders, Provider: "codex"}}}
	if candidateAllowed(candidate, providerOnly) {
		t.Fatal("unclassified candidate matched a provider selector")
	}
	exact := billing.RoutingDecision{CredentialIDs: []string{billing.CredentialFingerprint(candidate.ID)}}
	if !candidateAllowed(candidate, exact) {
		t.Fatal("unclassified candidate did not match its exact fingerprint")
	}
}

func TestSchedulerDelegatesAnUnchangedCandidateSet(t *testing.T) {
	app, scope := configuredRoutingApp(t, billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAIProviders, Provider: "codex"}}})
	raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, schedulerRequest(scope, SchedulerAuthCandidate{ID: "config-codex", Provider: "codex", Attributes: map[string]string{"source": "config:codex[abc]"}})))
	if err != nil {
		t.Fatal(err)
	}
	var response SchedulerPickResponse
	decodeResult(t, raw, &response)
	if response.Handled {
		t.Fatalf("response=%+v, want CPA delegation", response)
	}
}

func TestSchedulerUsesClientRequestedModelForConditionalRoutes(t *testing.T) {
	app, scope := configuredRoutingApp(t, billing.RouteRule{
		Models: []string{"public-model"},
		CredentialProviders: []billing.CredentialProviderSelector{{
			Source: billing.CredentialSourceAuthFiles, Provider: "codex",
		}},
	})
	req := schedulerRequest(scope,
		SchedulerAuthCandidate{ID: "file-codex", Provider: "codex", Attributes: map[string]string{"path": "/auth/codex.json"}},
		SchedulerAuthCandidate{ID: "config-codex", Provider: "codex", Attributes: map[string]string{"source": "config:codex[0]"}},
	)
	req.Model = "upstream-model"
	req.Options.Metadata[MetadataRequestedModel] = "public-model"
	raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, req))
	if err != nil {
		t.Fatal(err)
	}
	var response SchedulerPickResponse
	decodeResult(t, raw, &response)
	if !response.Handled || response.AuthID != "file-codex" {
		t.Fatalf("response=%+v", response)
	}
}

func TestSchedulerFailsClosedWithoutAUsableRoutedCandidate(t *testing.T) {
	tests := []struct {
		name       string
		rule       billing.RouteRule
		candidates []SchedulerAuthCandidate
	}{
		{
			name: "no match",
			rule: billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("wanted")}},
			candidates: []SchedulerAuthCandidate{
				{ID: "other", Provider: "codex", Attributes: map[string]string{"path": "/other"}},
			},
		},
		{
			name: "non-positive weight",
			rule: billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("a")}},
			candidates: []SchedulerAuthCandidate{
				{ID: "a", Provider: "codex", Attributes: map[string]string{"path": "/a", "weight": "0"}},
				{ID: "outside", Provider: "codex", Attributes: map[string]string{"path": "/outside"}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, scope := configuredRoutingApp(t, test.rule)
			raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, schedulerRequest(scope, test.candidates...)))
			if err != nil {
				t.Fatal(err)
			}
			var envelope Envelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.OK || envelope.Error == nil || envelope.Error.HTTPStatus != 503 {
				t.Fatalf("envelope=%+v", envelope)
			}
		})
	}
}

func TestSubsetSchedulerUsesCandidateWeights(t *testing.T) {
	app, scope := configuredRoutingApp(t, billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("a"), billing.CredentialFingerprint("b")}})
	candidates := []SchedulerAuthCandidate{{ID: "a", Provider: "codex", Attributes: map[string]string{"path": "/a", "weight": "2"}}, {ID: "b", Provider: "codex", Attributes: map[string]string{"path": "/b", "weight": "1"}}, {ID: "outside", Provider: "codex", Attributes: map[string]string{"path": "/outside"}}}
	counts := map[string]int{}
	for range 30 {
		raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, schedulerRequest(scope, candidates...)))
		if err != nil {
			t.Fatal(err)
		}
		var response SchedulerPickResponse
		decodeResult(t, raw, &response)
		counts[response.AuthID]++
	}
	if counts["a"] != 20 || counts["b"] != 10 || counts["outside"] != 0 {
		t.Fatalf("counts=%v", counts)
	}
}

func TestSubsetSchedulerKeepsAPIKeysAndTemporaryCandidateChangesIndependent(t *testing.T) {
	var scheduler subsetScheduler
	candidates := []SchedulerAuthCandidate{
		{ID: "a", Attributes: map[string]string{"weight": "1"}},
		{ID: "b", Attributes: map[string]string{"weight": "1"}},
	}
	firstA := scheduler.pick("key-a", "pool", candidates)
	firstB := scheduler.pick("key-b", "pool", candidates)
	if firstA != "a" || firstB != "a" {
		t.Fatalf("first picks key-a=%q key-b=%q, want independent pools", firstA, firstB)
	}
	if picked := scheduler.pick("key-a", "pool", candidates[1:]); picked != "b" {
		t.Fatalf("temporary subset pick=%q", picked)
	}
	if picked := scheduler.pick("key-a", "pool", candidates); picked != "b" {
		t.Fatalf("restored candidate pick=%q, want preserved smooth scores", picked)
	}
	if picked := scheduler.pick("key-b", "pool", candidates); picked != "b" {
		t.Fatalf("key-b second pick=%q, key-a must not advance it", picked)
	}
	if picked := scheduler.pick("key-a", "pool", candidates); picked != "a" {
		t.Fatalf("key-a third pick=%q", picked)
	}
	scheduler.prune(map[string]struct{}{"key-a": {}})
	if picked := scheduler.pick("key-a", "pool", candidates); picked != "b" {
		t.Fatalf("retained key pick=%q", picked)
	}
	if picked := scheduler.pick("key-b", "pool", candidates); picked != "a" {
		t.Fatalf("pruned key pick=%q", picked)
	}
}

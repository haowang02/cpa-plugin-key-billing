package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"cpa-key-billing/internal/billing"
)

func TestRouteLogRecordsEachStage(t *testing.T) {
	codexFiles := billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAuthFiles, Provider: "codex"}}}
	succeeded := RequestCompletion{Outcome: "succeeded", StatusCode: 200}
	for _, test := range []struct {
		name       string
		rule       billing.RouteRule
		completion RequestCompletion
		level      billing.PluginLogLevel
		want       map[string]any
	}{
		{"unrestricted", billing.RouteRule{}, succeeded, "", nil},
		{"model allowed", billing.RouteRule{Models: []string{"gpt-5.6"}}, succeeded, billing.PluginLogDebug,
			map[string]any{"model_policy": "restricted", "model_result": "allow", "credential_policy": "unrestricted", "outcome": "succeeded"}},
		{"model denied", billing.RouteRule{Models: []string{"deepseek-v4"}, CredentialIDs: []string{billing.CredentialFingerprint("auth-a")}},
			RequestCompletion{Outcome: "rejected", StatusCode: 403}, billing.PluginLogDebug, map[string]any{"model_result": "deny", "credential_result": "not_reached"}},
		{"credentials unavailable", codexFiles, RequestCompletion{Outcome: "failed", StatusCode: 503, Error: routeUnavailable.message("gpt-5.6")},
			billing.PluginLogInfo, map[string]any{"credential_policy": "restricted", "credential_result": routeUnavailable.result}},
		{"no credential serves the model", codexFiles, RequestCompletion{Outcome: "failed", StatusCode: 403, Error: routeNoCredential.message("gpt-5.6")},
			billing.PluginLogError, map[string]any{"credential_policy": "restricted", "credential_result": routeNoCredential.result}},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, scope := configuredRoutingApp(t, test.rule)
			if _, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{RequestID: "request", SourceFormat: "openai", Model: "gpt-5.6", RequestedModel: "gpt-5.6", Metadata: map[string]any{MetadataCallerScope: scope}})); err != nil {
				t.Fatal(err)
			}
			test.completion.RequestID = "request"
			if _, err := app.HandleMethod(MethodRequestComplete, mustMarshal(t, test.completion)); err != nil {
				t.Fatal(err)
			}
			page, err := app.store.PluginLogsPage(billing.PluginLogQuery{Limit: 1})
			if err != nil || len(page.Entries) != 1 {
				t.Fatalf("entries=%+v err=%v", page.Entries, err)
			}
			entry := page.Entries[0]
			if logged := strings.HasPrefix(entry.Message, "route "); logged != (test.want != nil) {
				t.Fatalf("latest log=%+v", entry)
			}
			if test.want == nil {
				return
			}
			var row map[string]any
			if entry.Level != test.level || json.Unmarshal([]byte(strings.TrimPrefix(entry.Message, "route ")), &row) != nil {
				t.Fatalf("entry=%+v", entry)
			}
			for field, want := range test.want {
				if row[field] != want {
					t.Fatalf("row=%+v, want %s=%v", row, field, want)
				}
			}
		})
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

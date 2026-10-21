package plugin

import (
	"net/http"
	"strings"
	"testing"

	"cpa-key-billing/internal/billing"
)

// The host offers an auth file as a scheduler candidate whose ID is its path
// under the auth directory, the way CLIProxyAPI reports it.
func observeOpenCodeZenCredential(t *testing.T, app *App) {
	t.Helper()
	const credentialID = "/root/.cli-proxy-api/opencodezen-Syngnat.json"
	app.observeCandidates([]SchedulerAuthCandidate{{
		ID: credentialID, Provider: "freetier",
		Attributes: map[string]string{"source_backend": "file", "path": credentialID},
	}})
	app.observeRouteCredential(RequestInterceptRequest{
		Metadata: map[string]any{MetadataSelectedAuth: credentialID, MetadataSelectedIndex: "auth-index"},
	}, "unrestricted", "")
}

// A request event names the upstream credential that served the request, in the
// same "provider · name" form the routing rules and the route log show.
func TestRequestEventNamesTheUpstreamCredential(t *testing.T) {
	app := newConfiguredApp(t)
	observeOpenCodeZenCredential(t, app)

	publishUsageRecord(t, app, UsageRecord{
		Provider: "freetier", ExecutorType: "executorAdapter", Model: "minimax-m3.1-flash-free",
		Alias: "minimax-m3.1-flash-free", APIKey: testAPIKey, AuthIndex: "auth-index",
		AuthType: "apikey", Generate: true, RequestedAt: app.store.Now(),
		Detail: UsageDetail{OutputTokens: 10, TotalTokens: 10},
	})

	entries := requestEventEntries(t, app)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	name := entries[0].CredentialName
	if name == "" {
		t.Fatal("request event did not name the upstream credential")
	}
	if !strings.Contains(name, "opencodezen-Syngnat") {
		t.Fatalf("credential name = %q, want the upstream account name", name)
	}
}

// A credential the plugin never saw stays unnamed: the plugin must not invent
// an account for it.
func TestRequestEventLeavesAnUnknownCredentialUnnamed(t *testing.T) {
	app := newConfiguredApp(t)
	publishUsageRecord(t, app, UsageRecord{
		Provider: "freetier", ExecutorType: "executorAdapter", Model: "minimax-m3.1-flash-free",
		Alias: "minimax-m3.1-flash-free", APIKey: testAPIKey, AuthIndex: "unknown-index",
		AuthType: "apikey", Generate: true, RequestedAt: app.store.Now(),
		Detail: UsageDetail{OutputTokens: 10, TotalTokens: 10},
	})

	entries := requestEventEntries(t, app)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].CredentialName != "" {
		t.Fatalf("credential name = %q, want empty for an unknown credential", entries[0].CredentialName)
	}
}

// The client User-Agent of the inbound request is stored with its event.
func TestRequestEventRecordsTheClientUserAgent(t *testing.T) {
	app := newConfiguredApp(t)
	const userAgent = "hermes-agent/1.0"
	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		RequestID: "ua-request", TraceID: "ua-trace", SourceFormat: "openai",
		Model: flowModel, RequestedModel: flowModel,
		Headers: http.Header{"User-Agent": []string{userAgent}},
		Metadata: map[string]any{
			MetadataCallerScope: flowScope(), MetadataRequestPath: "/v1/responses",
		},
	}))
	if errHandle != nil {
		t.Fatalf("request.intercept_before error = %v", errHandle)
	}
	var admitted RequestInterceptResponse
	decodeResult(t, raw, &admitted)
	if admitted.Terminate {
		t.Fatalf("request was terminated: %s", admitted.ResponseBody)
	}

	publishUsageRecord(t, app, UsageRecord{
		TraceID: "ua-trace", Provider: "codex", ExecutorType: "CodexExecutor",
		Model: flowModel, Alias: flowModel, APIKey: testAPIKey, AuthType: "oauth",
		Generate: true, RequestedAt: app.store.Now(),
		Detail: UsageDetail{OutputTokens: 10, TotalTokens: 10},
	})

	entries := requestEventEntries(t, app)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].UserAgent != userAgent {
		t.Fatalf("user agent = %q, want %q", entries[0].UserAgent, userAgent)
	}
}

// A request without a User-Agent reports none rather than an empty invention.
func TestRequestEventWithoutUserAgentStaysEmpty(t *testing.T) {
	app := newConfiguredApp(t)
	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		RequestID: "no-ua", TraceID: "no-ua-trace", SourceFormat: "openai",
		Model: flowModel, RequestedModel: flowModel,
		Metadata: map[string]any{
			MetadataCallerScope: flowScope(), MetadataRequestPath: "/v1/responses",
		},
	}))
	if errHandle != nil {
		t.Fatalf("request.intercept_before error = %v", errHandle)
	}
	var admitted RequestInterceptResponse
	decodeResult(t, raw, &admitted)

	publishUsageRecord(t, app, UsageRecord{
		TraceID: "no-ua-trace", Provider: "codex", ExecutorType: "CodexExecutor",
		Model: flowModel, Alias: flowModel, APIKey: testAPIKey, AuthType: "oauth",
		Generate: true, RequestedAt: app.store.Now(),
		Detail: UsageDetail{OutputTokens: 10, TotalTokens: 10},
	})

	entries := requestEventEntries(t, app)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].UserAgent != "" {
		t.Fatalf("user agent = %q, want empty", entries[0].UserAgent)
	}
}

// The source of a request event names the credential when one is known.
func TestRequestEventSourceNamesTheCredential(t *testing.T) {
	app := newConfiguredApp(t)
	observeOpenCodeZenCredential(t, app)

	publishUsageRecord(t, app, UsageRecord{
		Provider: "freetier", ExecutorType: "executorAdapter", Model: "minimax-m3.1-flash-free",
		Alias: "minimax-m3.1-flash-free", APIKey: testAPIKey, AuthIndex: "auth-index",
		AuthType: "apikey", Generate: true, RequestedAt: app.store.Now(),
		Detail: UsageDetail{OutputTokens: 10, TotalTokens: 10},
	})

	entries := requestEventEntries(t, app)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	if !strings.Contains(entries[0].Source, "opencodezen-Syngnat") {
		t.Fatalf("source = %q, want the upstream account name", entries[0].Source)
	}
}

// A long User-Agent is bounded so one request cannot bloat every stored row.
func TestRequestUserAgentIsBounded(t *testing.T) {
	long := strings.Repeat("a", 1024)
	if got := requestUserAgent(http.Header{"User-Agent": []string{long}}); len(got) != 512 {
		t.Fatalf("user agent length = %d, want 512", len(got))
	}
	if got := requestUserAgent(http.Header{}); got != "" {
		t.Fatalf("user agent = %q, want empty", got)
	}
}

var _ = billing.CredentialSourceAuthFiles

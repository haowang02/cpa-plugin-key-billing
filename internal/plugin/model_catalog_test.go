package plugin

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"cpa-key-billing/internal/billing"
)

// One auth file serves codex models; one config key, which host.auth.list
// omits, serves an OpenAI-compatible provider.
func catalogHost(t *testing.T) HostCaller {
	t.Helper()
	return func(method string, _ any) (json.RawMessage, error) {
		switch method {
		case hostAuthModels:
			return json.RawMessage(`{"credentials":[
				{"id":"codex-a.json","auth_index":"1","provider":"codex","models":["gpt-5.5","gpt-5.6"]},
				{"id":"openai-compatibility:compat:0","auth_index":"2","provider":"openai-compatible-compat","models":["gpt-5.5","deepseek-v4"]}]}`), nil
		case hostAuthList:
			return json.RawMessage(`{"files":[{"id":"codex-a.json","provider":"codex","source":"file","path":"/auth/codex-a.json"}]}`), nil
		}
		t.Fatalf("unexpected host call %s", method)
		return nil, nil
	}
}

func catalogApp(t *testing.T, rule billing.RouteRule) *App {
	t.Helper()
	app := restrictApp(t, rule)
	app.SetHostCaller(catalogHost(t))
	app.authModelsSupported.Store(true)
	return app
}

func interceptCatalog(t *testing.T, app *App, headers http.Header, body string) []byte {
	t.Helper()
	raw, err := app.HandleMethod(MethodResponseInterceptAfter, mustMarshal(t, ResponseInterceptRequest{RequestHeaders: headers, Body: []byte(body)}))
	if err != nil {
		t.Fatalf("response.intercept_after error = %v", err)
	}
	var response ResponseInterceptResponse
	decodeResult(t, raw, &response)
	return response.Body
}

func catalogIDs(t *testing.T, body []byte) []string {
	t.Helper()
	catalog, ok := parseModelCatalog(body)
	if !ok {
		t.Fatalf("unparseable catalog %s", body)
	}
	return catalog.ids
}

func bearer(key string) http.Header { return http.Header{"Authorization": {"Bearer " + key}} }

func TestModelCatalogKeepsAllowedModelsInEveryFormat(t *testing.T) {
	app := catalogApp(t, billing.RouteRule{Models: []string{"gpt-5.5", "deepseek-v4"}})
	for _, test := range []struct {
		name, body  string
		want        []string
		first, last string
	}{
		{"openai", `{"object":"list","data":[{"id":"gpt-5.5"},{"id":"gpt-5.6"},{"id":"deepseek-v4"}]}`, []string{"gpt-5.5", "deepseek-v4"}, "", ""},
		{"codex", `{"models":[{"slug":"gpt-5.6"},{"slug":"gpt-5.5"}]}`, []string{"gpt-5.5"}, "", ""},
		{"gemini", `{"models":[{"name":"models/gpt-5.5"},{"name":"models/gpt-5.6"}]}`, []string{"gpt-5.5"}, "", ""},
		{"claude", `{"data":[{"id":"claude-fable-5-dd-4v-keespeed"},{"id":"claude-fable-5-dd-5.5-tpg"},{"id":"claude-fable-5-dd-6.5-tpg"}],` +
			`"first_id":"claude-fable-5-dd-4v-keespeed","has_more":false,"last_id":"claude-fable-5-dd-6.5-tpg"}`,
			[]string{"deepseek-v4", "gpt-5.5"}, "claude-fable-5-dd-4v-keespeed", "claude-fable-5-dd-5.5-tpg"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := interceptCatalog(t, app, bearer(testAPIKey), test.body)
			if ids := catalogIDs(t, body); !slices.Equal(ids, test.want) {
				t.Fatalf("ids = %v, want %v (body=%s)", ids, test.want, body)
			}
			var bounds struct {
				FirstID string `json:"first_id"`
				LastID  string `json:"last_id"`
			}
			if err := json.Unmarshal(body, &bounds); err != nil || bounds.FirstID != test.first || bounds.LastID != test.last {
				t.Fatalf("first/last = %+v, %v", bounds, err)
			}
		})
	}
}

// CLIProxyAPI authenticates the first configured key among Authorization,
// X-Goog-Api-Key and X-Api-Key; management panels see the whole proxy.
func TestModelCatalogFollowsTheAuthenticatedKey(t *testing.T) {
	app := catalogApp(t, billing.RouteRule{DeniedModels: []string{"gpt-5.6"}})
	const catalog = `{"data":[{"id":"gpt-5.5"},{"id":"gpt-5.6"}]}`
	for _, test := range []struct {
		name     string
		headers  http.Header
		filtered bool
	}{
		{"bearer", bearer(testAPIKey), true},
		{"raw authorization", http.Header{"Authorization": {testAPIKey}}, true},
		{"unknown key first", http.Header{"Authorization": {"Bearer sk-unknown"}, "X-Api-Key": {testAPIKey}}, true},
		{"untracked key", bearer("sk-unknown"), false},
		{"management panel", http.Header{"Authorization": {"Bearer " + testAPIKey}, "Referer": {"https://cpa.example/management.html#/system"}}, false},
		{"oh-my-cpa backend", http.Header{"Authorization": {"Bearer " + testAPIKey}, "User-Agent": {"Oh-My-CPA/1.2.3-dev"}}, false},
		{"other product", http.Header{"Authorization": {"Bearer " + testAPIKey}, "User-Agent": {"Oh-My-CPA-Helper/0.1.0"}}, true},
		{"explicit request", http.Header{"Authorization": {"Bearer " + testAPIKey}, http.CanonicalHeaderKey(modelCatalogHeader): {"all"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := interceptCatalog(t, app, test.headers, catalog)
			if filtered := body != nil; filtered != test.filtered {
				t.Fatalf("filtered = %v, want %v (body=%s)", filtered, test.filtered, body)
			}
		})
	}

	raw, err := app.HandleMethod(MethodResponseInterceptAfter, mustMarshal(t, ResponseInterceptRequest{
		Model: "gpt-5.6", RequestedModel: "gpt-5.6", RequestHeaders: bearer(testAPIKey), Body: []byte(catalog),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var response ResponseInterceptResponse
	if decodeResult(t, raw, &response); response.Body != nil {
		t.Fatalf("execution response was rewritten: %s", response.Body)
	}
}

func TestModelCatalogKeepsModelsServedByAllowedCredentials(t *testing.T) {
	const catalog = `{"data":[{"id":"gpt-5.5"},{"id":"gpt-5.6"},{"id":"deepseek-v4"},{"id":"claude-opus-5"}]}`
	for _, test := range []struct {
		name string
		rule billing.RouteRule
		want []string
	}{
		{"auth file category", billing.RouteRule{
			CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAuthFiles, Provider: "codex"}},
		}, []string{"gpt-5.5", "gpt-5.6"}},
		{"config key category and models", billing.RouteRule{
			Models:              []string{"gpt-5.5", "gpt-5.6"},
			CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAIProviders, Provider: "openai-compatible-compat"}},
		}, []string{"gpt-5.5"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := interceptCatalog(t, catalogApp(t, test.rule), bearer(testAPIKey), catalog)
			if ids := catalogIDs(t, body); !slices.Equal(ids, test.want) {
				t.Fatalf("ids = %v, want %v", ids, test.want)
			}
		})
	}
}

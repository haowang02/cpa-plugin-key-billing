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

func TestAuthFilesExposeOnlyDisplayFieldsInCategoryOrder(t *testing.T) {
	app := newConfiguredApp(t)
	app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
		if method != hostAuthList {
			t.Fatalf("host method = %q, want %q", method, hostAuthList)
		}
		return json.RawMessage(`{"files":[
			{"name":"disk-only.json","type":"codex"},
			{"auth_index":"api-1","name":"openai-api-key.json","type":"openai","provider":"openai","account_type":"api_key","account":"sk-upstream-secret"},
			{"auth_index":"x-2","name":"zeta.json","type":"xai","email":"z@example.com","disabled":true,"path":"/secret/zeta.json","account":"sk-upstream-secret","id_token":"secret"},
			{"auth_index":"c-2","name":"zeta.json","type":"codex","id_token":{"plan_type":"pro"}},
			{"auth_index":"c-1","name":"Alpha.json","type":"codex","email":"user@example.com","modtime":"2026-09-02T01:02:03Z","id_token":{"planType":"prolite"}},
			{"auth_index":"a-1","name":"antigravity.json","type":"antigravity"},
			{"auth_index":"cl-1","name":"claude.json","type":"claude"}
		]}`), nil
	})
	response := app.authFiles(viewAccess{})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	var payload authFileListResponse
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatal(errDecode)
	}
	got := make([]string, 0, len(payload.Files))
	for _, file := range payload.Files {
		got = append(got, file.Category+":"+file.Name)
	}
	want := []string{"claude:claude.json", "antigravity:antigravity.json", "codex:Alpha.json", "codex:zeta.json", "xai:zeta.json"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("files = %v, want %v", got, want)
	}
	encoded := string(response.Body)
	if payload.Files[2].CacheRevision != "2026-09-02T01:02:03Z" {
		t.Fatalf("cache revision = %q", payload.Files[2].CacheRevision)
	}
	if payload.Files[2].Email != "user@example.com" {
		t.Fatalf("email = %q, want CPA email", payload.Files[2].Email)
	}
	if payload.Files[4].QuotaSupported || payload.Files[4].QuotaReason != "认证文件已停用" {
		t.Fatalf("disabled quota availability = %+v", payload.Files[4])
	}
	for _, forbidden := range []string{"disk-only", "openai-api-key", "sk-upstream-secret", "/secret/zeta.json", "id_token", "secret", `"account"`, `"path"`} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestNormalizeCodexPlan(t *testing.T) {
	tests := map[string]string{
		"plus": "plus", " PRO ": "pro-20x", "prolite": "pro-5x", "pro-lite": "pro-5x",
		"pro_lite": "pro-5x", "pro-5x": "pro-5x", "pro-20x": "pro-20x", "Custom Plan": "Custom Plan",
	}
	for input, want := range tests {
		if got := normalizeCodexPlan(input); got != want {
			t.Errorf("normalizeCodexPlan(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAccountAuthFilesRequireTrackedAPIKey(t *testing.T) {
	app := newConfiguredApp(t)
	hostCalls := 0
	app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
		hostCalls++
		if method != hostAuthList {
			t.Fatalf("host method = %q, want %q", method, hostAuthList)
		}
		return json.RawMessage(`{"files":[]}`), nil
	})

	request := ManagementRequest{Headers: http.Header{"Authorization": {"Bearer " + accountTestKeyA}}}
	access, ok := app.apiKeyViewAccess(request)
	if !ok {
		t.Fatal("valid bearer was rejected")
	}
	if response := app.authFiles(access); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("untracked status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
	request.Query = url.Values{"auth_index": {"codex-1"}}
	if response := app.authQuota(request, access); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("untracked quota status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
	if hostCalls != 0 {
		t.Fatalf("untracked account made %d host calls", hostCalls)
	}
	if _, errSync := app.store.SyncKeys([]string{accountTestKeyA}, false); errSync != nil {
		t.Fatal(errSync)
	}
	access, _ = app.apiKeyViewAccess(request)
	if response := app.authFiles(access); response.StatusCode != http.StatusOK {
		t.Fatalf("tracked status = %d, body = %s", response.StatusCode, response.Body)
	}
	if hostCalls != 1 {
		t.Fatalf("tracked account made %d host calls, want 1", hostCalls)
	}
}

func TestAccountAuthFilesFollowCredentialRouting(t *testing.T) {
	app := newConfiguredApp(t)
	if _, errSync := app.store.SyncKeys([]string{accountTestKeyA}, false); errSync != nil {
		t.Fatal(errSync)
	}
	scope := billing.CallerScope(accountTestKeyA)
	if _, errRoute := app.store.CreateRoute(billing.Route{Name: "受限认证文件", Rule: billing.RouteRule{
		CredentialIDs: []string{
			billing.CredentialFingerprint("auth-codex-allowed"),
			billing.CredentialFingerprint("config-codex-exact"),
		},
		CredentialProviders: []billing.CredentialProviderSelector{
			{Source: billing.CredentialSourceAuthFiles, Provider: "claude"},
			{Source: billing.CredentialSourceAIProviders, Provider: "codex"},
		},
	}}, []string{scope}); errRoute != nil {
		t.Fatal(errRoute)
	}
	hostGetCalls := 0
	app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
		switch method {
		case hostAuthList:
			return json.RawMessage(`{"files":[
				{"id":"auth-codex-allowed","auth_index":"codex-allowed","name":"allowed.json","type":"codex","source":"file","path":"/auth/allowed.json"},
				{"id":"auth-codex-denied","auth_index":"codex-denied","name":"denied.json","type":"codex","source":"file","path":"/auth/denied.json"},
				{"id":"auth-claude","auth_index":"claude-allowed","name":"claude.json","type":"claude","source":"file","path":"/auth/claude.json"},
				{"id":"config-codex-exact","auth_index":"config-exact","name":"configured-exact","type":"codex","provider":"codex","source":"config","runtime_only":true}
			]}`), nil
		case hostAuthGet:
			hostGetCalls++
			return nil, nil
		default:
			t.Fatalf("unexpected host method %q", method)
			return nil, nil
		}
	})
	access := viewAccess{APIKey: true, Scope: scope, Tracked: true}
	response := app.authFiles(access)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	var payload authFileListResponse
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatal(errDecode)
	}
	got := make([]string, 0, len(payload.Files))
	for _, file := range payload.Files {
		got = append(got, file.AuthIndex)
	}
	want := []string{"claude-allowed", "codex-allowed"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("auth files = %v, want %v", got, want)
	}

	request := ManagementRequest{Query: url.Values{"auth_index": {"codex-denied"}}}
	if response := app.authQuota(request, access); response.StatusCode != http.StatusNotFound {
		t.Fatalf("denied quota status = %d, body = %s", response.StatusCode, response.Body)
	}
	if hostGetCalls != 0 {
		t.Fatalf("denied credential made %d host.auth.get calls", hostGetCalls)
	}
}

func TestAccountAuthQuotaUsesPhysicalCredentialWithoutForwardingAPIKey(t *testing.T) {
	app := newConfiguredApp(t)
	if _, errSync := app.store.SyncKeys([]string{accountTestKeyA}, false); errSync != nil {
		t.Fatal(errSync)
	}
	app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
		encoded, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		if strings.Contains(string(encoded), accountTestKeyA) {
			t.Fatalf("host payload leaked downstream API key: %s", encoded)
		}
		switch method {
		case hostAuthList:
			return json.RawMessage(`{"files":[{"auth_index":"codex-1","name":"codex.json","type":"codex"}]}`), nil
		case hostAuthGet:
			return json.RawMessage(`{"auth_index":"codex-1","json":{"access_token":"dummy-upstream-token"}}`), nil
		case hostHTTPDo:
			request := payload.(hostHTTPRequest)
			if request.Headers.Get("Authorization") != "Bearer dummy-upstream-token" {
				t.Fatalf("authorization = %q", request.Headers.Get("Authorization"))
			}
			return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"rate_limit":{}}`)}), nil
		default:
			t.Fatalf("unexpected host method %q", method)
			return nil, nil
		}
	})
	request := ManagementRequest{
		Headers: http.Header{"Authorization": {"Bearer " + accountTestKeyA}},
		Query:   url.Values{"auth_index": {"codex-1"}},
	}
	access, ok := app.apiKeyViewAccess(request)
	if !ok {
		t.Fatal("valid bearer was rejected")
	}
	response := app.authQuota(request, access)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
}

func TestRuntimeOnlyAuthFileDisablesQuotaWithoutLeakingDetails(t *testing.T) {
	app := newConfiguredApp(t)
	app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
		if method != hostAuthList {
			t.Fatalf("host method = %q", method)
		}
		return json.RawMessage(`{"files":[{"auth_index":"runtime-1","name":"runtime","type":"codex","runtime_only":true,"path":"/secret/runtime"}]}`), nil
	})
	response := app.authFiles(viewAccess{})
	var payload authFileListResponse
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(payload.Files) != 1 || payload.Files[0].QuotaSupported || payload.Files[0].QuotaReason == "" {
		t.Fatalf("files = %+v", payload.Files)
	}
	if strings.Contains(string(response.Body), "/secret/runtime") {
		t.Fatalf("response leaked path: %s", response.Body)
	}
}

func TestPaidXAICredential(t *testing.T) {
	if !paidXAICredential(map[string]any{"access_token": "e30.eyJ0aWVyIjoxfQ.sig"}) {
		t.Fatal("paid JWT was not recognized")
	}
}

func TestAPIKeyAuthFileCannotBeQueried(t *testing.T) {
	app := newConfiguredApp(t)
	hostGetCalls := 0
	app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
		switch method {
		case hostAuthList:
			return json.RawMessage(`{"files":[{"auth_index":"xai-1","name":"xai.json","type":"xai","account_type":"api_key","account":"dummy-paid-key"}]}`), nil
		case hostAuthGet:
			hostGetCalls++
			return nil, nil
		default:
			t.Fatalf("unexpected host method %q", method)
			return nil, nil
		}
	})
	response := app.authQuota(ManagementRequest{Query: url.Values{"auth_index": {"xai-1"}}}, viewAccess{})
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	if hostGetCalls != 0 {
		t.Fatalf("host.auth.get calls = %d, want 0", hostGetCalls)
	}
}

func TestPhysicalAPIKeyCredentialCannotReachUpstream(t *testing.T) {
	app := newConfiguredApp(t)
	app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
		if method != hostAuthGet {
			t.Fatalf("unexpected host method %q", method)
		}
		return json.RawMessage(`{"auth_index":"xai-1","json":{"api_key":"dummy-paid-key"}}`), nil
	})
	_, errFetch := app.fetchAuthQuota("", hostAuthFile{AuthIndex: "xai-1"}, "xai")
	if errFetch == nil || errFetch.Error() != "API Key 凭证不支持此限额查询" {
		t.Fatalf("error = %v", errFetch)
	}
}

func TestUpstreamErrorsRedactPhysicalCredential(t *testing.T) {
	app := newConfiguredApp(t)
	app.SetHostCaller(func(string, any) (json.RawMessage, error) {
		return mustJSONRaw(t, hostHTTPResponse{
			StatusCode: http.StatusBadRequest,
			Body:       []byte(`{"error":{"message":"rejected dummy-secret-token"}}`),
		}), nil
	})
	_, errCall := app.upstream("", http.MethodGet, "https://example.invalid/quota", "dummy-secret-token", nil, nil)
	if errCall == nil || strings.Contains(errCall.Error(), "dummy-secret-token") || !strings.Contains(errCall.Error(), "[REDACTED]") {
		t.Fatalf("error = %v", errCall)
	}
}

func TestCodexQuotaPreservesAdditionalDynamicWindows(t *testing.T) {
	app := newConfiguredApp(t)
	var endpoint string
	app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case hostAuthList:
			return json.RawMessage(`{"files":[{"auth_index":"codex-1","name":"codex.json","type":"codex"}]}`), nil
		case hostAuthGet:
			return json.RawMessage(`{"auth_index":"codex-1","json":{"access_token":"dummy-token","account_id":"dummy-account"}}`), nil
		case hostHTTPDo:
			request := payload.(hostHTTPRequest)
			endpoint = request.URL
			if request.Headers.Get("Authorization") != "Bearer dummy-token" || request.Headers.Get("Chatgpt-Account-Id") != "dummy-account" {
				t.Fatalf("headers = %#v", request.Headers)
			}
			body := `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":38,"limit_window_seconds":604800}},"additional_rate_limits":[{"limit_name":"GPT-5.3-Codex-Spark","rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":18000},"secondary_window":{"used_percent":0,"limit_window_seconds":604800}}}],"rate_limit_reset_credits":{"available_count":1}}`
			return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(body)}), nil
		default:
			t.Fatalf("unexpected host method %q", method)
			return nil, nil
		}
	})
	response := app.authQuota(ManagementRequest{Query: url.Values{"auth_index": {"codex-1"}}}, viewAccess{})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	var result authQuotaResponse
	if errDecode := json.Unmarshal(response.Body, &result); errDecode != nil {
		t.Fatal(errDecode)
	}
	if endpoint != "https://chatgpt.com/backend-api/wham/usage" {
		t.Fatalf("endpoint = %q", endpoint)
	}
	if result.Plan != "pro-20x" || len(result.Quota) != 3 {
		t.Fatalf("quota = %+v", result)
	}
	if result.Quota[0].RemainingPercent == nil || *result.Quota[0].RemainingPercent != 62 {
		t.Fatalf("remaining percent = %+v", result.Quota[0].RemainingPercent)
	}
	wantLabels := []string{"周限额", "GPT-5.3-Codex-Spark 5 小时限额", "GPT-5.3-Codex-Spark 周限额"}
	for index, want := range wantLabels {
		if result.Quota[index].Label != want {
			t.Fatalf("quota[%d].Label = %q, want %q", index, result.Quota[index].Label, want)
		}
	}
}

func TestCodexQuotaOrdersAndLabelsWindows(t *testing.T) {
	result := authQuotaResponse{Quota: []quotaRow{}}
	appendCodexRateLimit(&result, "", map[string]any{
		"allowed": false,
		"primary_window": map[string]any{
			"limit_window_seconds": float64(29 * 24 * 60 * 60),
		},
		"secondary_window": map[string]any{
			"limit_window_seconds": float64(5 * 60 * 60),
		},
	})
	if len(result.Quota) != 2 || result.Quota[0].Label != "5 小时限额" || result.Quota[1].Label != "月限额" || result.Quota[0].RemainingPercent == nil || *result.Quota[0].RemainingPercent != 0 {
		t.Fatalf("rows = %+v", result.Quota)
	}
}

func TestClaudeQuotaUsesFableLimitAndCanonicalTeamPlan(t *testing.T) {
	app := newConfiguredApp(t)
	responses := map[string]string{
		"https://api.anthropic.com/api/oauth/usage":   `{"five_hour":{"utilization":10},"seven_day_sonnet":{"utilization":20},"iguana_necktie":{"utilization":15},"limits":[{"kind":"weekly_scoped","percent":42,"is_active":true,"resets_at":"2026-09-09T00:00:00Z","scope":{"model":{"display_name":"Fable 5"}}}]}`,
		"https://api.anthropic.com/api/oauth/profile": `{"account":{"has_claude_max":false,"has_claude_pro":false},"organization":{"organization_type":"claude_team","subscription_status":"active","rate_limit_tier":"internal_team_tier"}}`,
	}
	app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
		if method != hostHTTPDo {
			t.Fatalf("host method = %q", method)
		}
		request := payload.(hostHTTPRequest)
		return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(responses[request.URL])}), nil
	})
	result := authQuotaResponse{Quota: []quotaRow{}}
	if errFetch := app.fetchClaudeQuota("", "token", &result); errFetch != nil {
		t.Fatal(errFetch)
	}
	if len(result.Quota) != 3 || result.Quota[0].Label != "5 小时限额" || result.Quota[1].Label != "Sonnet 周限额" || result.Quota[2].Label != "Fable 周限额" {
		t.Fatalf("quota = %+v", result.Quota)
	}
	if result.Plan != "Team" {
		t.Fatalf("plan = %q", result.Plan)
	}
}

func TestClaudeQuotaOmitsDisabledExtraUsage(t *testing.T) {
	app := newConfiguredApp(t)
	profileCalled := false
	app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
		request := payload.(hostHTTPRequest)
		if strings.HasSuffix(request.URL, "/usage") {
			return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"extra_usage":{"is_enabled":false,"used_credits":500,"monthly_limit":1000}}`)}), nil
		}
		profileCalled = true
		return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"account":{"has_claude_pro":true}}`)}), nil
	})
	result := authQuotaResponse{Quota: []quotaRow{}}
	if errFetch := app.fetchClaudeQuota("", "token", &result); errFetch != nil {
		t.Fatal(errFetch)
	}
	if len(result.Quota) != 0 || !profileCalled || result.Plan != "Pro" {
		t.Fatalf("quota = %+v, profile called = %v, plan = %q", result.Quota, profileCalled, result.Plan)
	}
}

func TestAntigravityQuotaReadsNestedCredentialFields(t *testing.T) {
	app := newConfiguredApp(t)
	var requestProject string
	app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case hostAuthGet:
			return json.RawMessage(`{"auth_index":"ag-1","json":{"metadata":{"token":{"access_token":"nested-token"}},"installed":{"project_id":"nested-project"}}}`), nil
		case hostHTTPDo:
			request := payload.(hostHTTPRequest)
			if request.Headers.Get("Authorization") != "Bearer nested-token" {
				t.Fatalf("authorization = %q", request.Headers.Get("Authorization"))
			}
			if strings.Contains(request.URL, "retrieveUserQuotaSummary") {
				var body map[string]string
				if errDecode := json.Unmarshal(request.Body, &body); errDecode != nil {
					t.Fatal(errDecode)
				}
				requestProject = body["project"]
				return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"body":{"groups":[{"displayName":"Gemini","buckets":[{"bucketId":"weekly","window":"weekly","remainingFraction":0.5},{"bucketId":"five","window":"5h","remainingFraction":0.8}]}]}}`)}), nil
			}
			return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"body":{"currentTier":{"id":"pro","name":"Google AI Pro"}}}`)}), nil
		default:
			t.Fatalf("unexpected host method %q", method)
			return nil, nil
		}
	})
	result, errFetch := app.fetchAuthQuota("", hostAuthFile{AuthIndex: "ag-1"}, "antigravity")
	if errFetch != nil {
		t.Fatal(errFetch)
	}
	if requestProject != "nested-project" || len(result.Quota) != 2 || result.Quota[0].Label != "5 小时限额" || result.Quota[1].Label != "周限额" || result.Plan != "Google AI Pro" {
		t.Fatalf("project = %q, quota = %+v", requestProject, result.Quota)
	}
}

func TestKimiQuotaReadsNestedLimitShape(t *testing.T) {
	app := newConfiguredApp(t)
	app.SetHostCaller(func(string, any) (json.RawMessage, error) {
		return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"limits":[{"detail":{"title":"Coding 5 小时限额","used":20,"limit":100,"duration":5,"timeUnit":"H","reset_at":"invalid","ttl":3600}}]}`)}), nil
	})
	fetchedAt := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	result := authQuotaResponse{FetchedAt: fetchedAt, Quota: []quotaRow{}}
	if errFetch := app.fetchKimiQuota("", "token", &result); errFetch != nil {
		t.Fatal(errFetch)
	}
	if len(result.Quota) != 1 {
		t.Fatalf("quota = %+v", result.Quota)
	}
	row := result.Quota[0]
	if row.Label != "Coding 5 小时限额" || row.RemainingPercent == nil || *row.RemainingPercent != 80 || row.ResetAt != fetchedAt.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("quota = %+v", result.Quota)
	}
}

func TestXAIQuotaDeduplicatesProductLimits(t *testing.T) {
	app := newConfiguredApp(t)
	app.SetHostCaller(func(_ string, payload any) (json.RawMessage, error) {
		request := payload.(hostHTTPRequest)
		if strings.Contains(request.URL, "format=credits") {
			body := `{"config":{"productUsage":[{"product":"Grok Code","usagePercent":20},{"product":"grok code","usagePercent":45},{"product":"Grok Vision","usagePercent":12}]}}`
			return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(body)}), nil
		}
		return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"config":{}}`)}), nil
	})
	result := authQuotaResponse{Quota: []quotaRow{}}
	if errFetch := app.fetchXAIQuota("", "token", "user", &result); errFetch != nil {
		t.Fatal(errFetch)
	}
	if len(result.Quota) != 2 || result.Quota[0].Label != "grok code 用量" || result.Quota[0].RemainingPercent == nil || *result.Quota[0].RemainingPercent != 55 || result.Quota[1].Label != "Grok Vision 用量" {
		t.Fatalf("quota = %+v", result.Quota)
	}
}

func TestCredentialProxyFailsInsteadOfSendingDirectRequest(t *testing.T) {
	app := newConfiguredApp(t)
	app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
		if method != hostAuthGet {
			t.Fatalf("unexpected host method %q", method)
		}
		return json.RawMessage(`{"auth_index":"codex-1","json":{"access_token":"dummy-token","proxy_url":"socks5://proxy.example:1080"}}`), nil
	})
	_, errFetch := app.fetchAuthQuota("", hostAuthFile{AuthIndex: "codex-1"}, "codex")
	if errFetch == nil || !strings.Contains(errFetch.Error(), "独立代理") {
		t.Fatalf("error = %v", errFetch)
	}
}

func TestXAIQuotaCombinesWeeklyMonthlyAndOnDemand(t *testing.T) {
	responses := map[string]string{
		"https://cli-chat-proxy.grok.com/v1/billing?format=credits": `{"config":{"creditUsagePercent":62.5,"currentPeriod":{"end":"2026-09-08T00:00:00Z"}}}`,
		"https://cli-chat-proxy.grok.com/v1/billing":                `{"config":{"monthlyLimit":{"val":1000},"used":{"val":1250},"onDemandCap":{"val":500},"billingPeriodEnd":"2026-10-01T00:00:00Z"}}`,
	}
	app := newConfiguredApp(t)
	app.SetHostCaller(func(_ string, payload any) (json.RawMessage, error) {
		request := payload.(hostHTTPRequest)
		body, ok := responses[request.URL]
		if !ok {
			t.Fatalf("unexpected endpoint %q", request.URL)
		}
		return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(body)}), nil
	})
	result := authQuotaResponse{Quota: []quotaRow{}}
	if errFetch := app.fetchXAIQuota("", "token", "user", &result); errFetch != nil {
		t.Fatal(errFetch)
	}
	want := []string{"周限额", "月度额度", "按量付费额度"}
	if len(result.Quota) != len(want) {
		t.Fatalf("quota = %+v", result.Quota)
	}
	for index, label := range want {
		if result.Quota[index].Label != label {
			t.Fatalf("quota[%d].Label = %q, want %q", index, result.Quota[index].Label, label)
		}
	}
	monthly := result.Quota[1]
	if monthly.Currency != "USD" || monthly.Used == nil || *monthly.Used != 10 || monthly.Limit == nil || *monthly.Limit != 10 || monthly.RemainingPercent == nil || *monthly.RemainingPercent != 0 {
		t.Fatalf("monthly quota = %+v", monthly)
	}
}

func mustJSONRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	return raw
}

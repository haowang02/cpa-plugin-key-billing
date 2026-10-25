package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/messages"
)

func TestAuthFilesExposeOnlyDisplayFieldsInCategoryOrder(t *testing.T) {
	app := newConfiguredApp(t)
	app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
		if method == hostAuthGet {
			switch payload.(map[string]string)["auth_index"] {
			case "a-1":
				return json.RawMessage(`{"json":{"weight":0}}`), nil
			case "c-1":
				return json.RawMessage(`{"json":{"weight":"10"}}`), nil
			default:
				return json.RawMessage(`{"json":{"access_token":"dummy-secret"}}`), nil
			}
		}
		if method != hostAuthList {
			t.Fatalf("host method = %q, want %q", method, hostAuthList)
		}
		return json.RawMessage(`{"files":[
			{"name":"disk-only.json","type":"codex"},
			{"auth_index":"api-1","name":"openai-api-key.json","type":"openai","provider":"openai","account_type":"api_key","account":"sk-upstream-secret"},
			{"auth_index":"x-2","name":"zeta.json","type":"xai","email":"z@example.com","disabled":true,"path":"/secret/zeta.json","account":"sk-upstream-secret","id_token":"secret"},
			{"auth_index":"c-2","name":"zeta.json","type":"codex","path":"/dummy/c-2.json","id_token":{"plan_type":"pro"}},
			{"auth_index":"c-1","name":"Alpha.json","type":"codex","path":"/dummy/c-1.json","email":"user@example.com","priority":99,"modtime":"2026-09-02T01:02:03Z","id_token":{"planType":"prolite"}},
			{"auth_index":"a-1","name":"antigravity.json","type":"antigravity","path":"/dummy/a-1.json"},
			{"auth_index":"cl-1","name":"claude.json","type":"claude","path":"/dummy/cl-1.json"}
		]}`), nil
	})
	response := app.authFiles(ManagementRequest{}, viewAccess{})
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
	if payload.Files[2].Title != "user@example.com" || payload.Files[3].Title != "zeta" {
		t.Fatalf("titles = %q, %q, want the CPA email and the file name", payload.Files[2].Title, payload.Files[3].Title)
	}
	// The host omits the default priority, which the UI still shows as P0.
	if payload.Files[2].Priority != 99 || !strings.Contains(encoded, `"priority":0`) {
		t.Fatalf("priorities = %d, body = %s", payload.Files[2].Priority, encoded)
	}
	for i, want := range []int64{1, 0, 10, 1, 1} {
		if weight := payload.Files[i].Weight; weight == nil || *weight != want {
			t.Fatalf("weight[%d] in %s, want %d", i, encoded, want)
		}
	}
	if payload.Files[4].QuotaSupported || payload.Files[4].QuotaReason != "Auth file is disabled" {
		t.Fatalf("disabled quota availability = %+v", payload.Files[4])
	}
	if payload.Files[4].QuotaReasonMessage.Key == "" {
		t.Fatal("missing translated quota availability")
	}
	for _, forbidden := range []string{"disk-only", "openai-api-key", "sk-upstream-secret", "/secret/zeta.json", "id_token", "access_token", "secret", `"account"`, `"path"`} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, encoded)
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
	if response := app.authFiles(ManagementRequest{}, access); response.StatusCode != http.StatusUnauthorized {
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
	if response := app.authFiles(ManagementRequest{}, access); response.StatusCode != http.StatusOK {
		t.Fatalf("tracked status = %d, body = %s", response.StatusCode, response.Body)
	}
	if hostCalls != 1 {
		t.Fatalf("tracked account made %d host calls, want 1", hostCalls)
	}
}

func TestAuthFilesStatisticsFollowTimeRangeAndAccountScope(t *testing.T) {
	app := newAppWithPrice(t, true)
	for _, key := range []string{accountTestKeyA, accountTestKeyB} {
		app.store.AcquireSlot(billing.CallerScope(key), key)
		app.store.AssignSlotCredential(key, billing.CredentialFingerprint("dummy-auth-file"))
	}
	from := app.store.Now().Add(-time.Hour).Truncate(time.Second)
	to := from.Add(2 * time.Minute)
	for _, sample := range []struct {
		at   time.Time
		key  string
		zero bool
	}{
		{from.Add(-45 * 24 * time.Hour), accountTestKeyA, false},
		{from.Add(-time.Nanosecond), accountTestKeyA, false},
		{from, accountTestKeyA, false},
		{from.Add(time.Minute), accountTestKeyA, true},
		{from.Add(time.Minute), accountTestKeyB, false},
		{to, accountTestKeyA, false},
	} {
		record := UsageRecord{Provider: "codex", AuthType: "oauth", AuthIndex: "dummy-auth",
			APIKey: sample.key, Model: "gpt-5.5", RequestedAt: sample.at,
			Detail: UsageDetail{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 50, TotalTokens: 120}}
		if sample.zero {
			record.Failed = true
			record.Detail = UsageDetail{}
		}
		if sample.at.Equal(to) {
			record.ResponseHeaders = http.Header{
				"X-Codex-Plan-Type": {"plus"}, "X-Codex-Primary-Used-Percent": {"90"},
				"X-Codex-Primary-Window-Minutes": {"300"}, "X-Codex-Primary-Reset-After-Seconds": {"1800"},
			}
		}
		publishUsageRecord(t, app, record)
	}
	if _, err := app.store.UpsertPrice(billing.CustomPrice{ModelID: "gpt-5.5", PriceRates: billing.PriceRates{InputPer1M: 99}}); err != nil {
		t.Fatal(err)
	}
	app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
		if method != hostAuthList {
			t.Fatalf("unexpected host method: %s", method)
		}
		return json.RawMessage(`{"files":[{"id":"dummy-auth-file","auth_index":"dummy-auth","name":"dummy.json","type":"codex"}]}`), nil
	})
	query := url.Values{"from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}
	for _, tc := range []struct {
		name, key string
		query     url.Values
		want      billing.CredentialUsageStats
	}{
		{"selected", "", query, billing.CredentialUsageStats{Requests: 3, Failed: 1, InputTokens: 200, CacheReadTokens: 100, OutputTokens: 40, CostUSD: 0.00019}},
		{"account", accountTestKeyA, query, billing.CredentialUsageStats{Requests: 2, Failed: 1, InputTokens: 100, CacheReadTokens: 50, OutputTokens: 20, CostUSD: 0.000095}},
		{"default", "", nil, billing.CredentialUsageStats{Requests: 5, Failed: 1, InputTokens: 400, CacheReadTokens: 200, OutputTokens: 80, CostUSD: 0.00038}},
		{"extended", "", url.Values{"from": {from.Add(-60 * 24 * time.Hour).Format(time.RFC3339Nano)}},
			billing.CredentialUsageStats{Requests: 6, Failed: 1, InputTokens: 500, CacheReadTokens: 250, OutputTokens: 100, CostUSD: 0.000475}},
		{"empty", "", url.Values{"from": {to.Add(time.Minute).Format(time.RFC3339Nano)}}, billing.CredentialUsageStats{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var response ManagementResponse
			if tc.key != "" {
				response = callAccount(t, app, routeAuthFiles, tc.key, tc.query)
			} else {
				response = callManagement(t, app, http.MethodGet, routeAuthFiles, tc.query, nil)
			}
			var view authFileListResponse
			if err := json.Unmarshal(response.Body, &view); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK || len(view.Files) != 1 {
				t.Fatalf("response=%s, want=%+v", response.Body, tc.want)
			}
			got := view.Files[0].Usage
			wantInFlight := 2
			if tc.key != "" {
				wantInFlight = 1
			}
			if view.Files[0].InFlight != wantInFlight {
				t.Fatalf("in-flight requests = %d, want %d", view.Files[0].InFlight, wantInFlight)
			}
			// Every request used gpt-5.5, so the breakdown of the same requests repeats the totals.
			breakdown := url.Values{"auth_index": {view.Files[0].AuthIndex}, "from": tc.query["from"], "to": tc.query["to"]}
			if tc.key != "" {
				response = callAccount(t, app, routeUsageModels, tc.key, breakdown)
			} else {
				response = callManagement(t, app, http.MethodGet, routeUsageModels, breakdown, nil)
			}
			var models struct{ Models []billing.ModelUsage }
			wantModels := 0
			if tc.want.Requests > 0 {
				wantModels = 1
			}
			if err := json.Unmarshal(response.Body, &models); err != nil || len(models.Models) != wantModels ||
				wantModels == 1 && (models.Models[0].Requests != tc.want.Requests || models.Models[0].InputTokens != tc.want.InputTokens) {
				t.Fatalf("breakdown=%s, want=%+v", response.Body, tc.want)
			}
			assertCostClose(t, got.CostUSD, tc.want.CostUSD)
			got.CostUSD = tc.want.CostUSD
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("response=%s, want=%+v", response.Body, tc.want)
			}
			quota := view.Files[0].Quota
			if quota == nil || quota.Plan != "Plus" || len(quota.Quota) != 1 || *quota.Quota[0].RemainingPercent != 10 {
				t.Fatalf("latest quota=%+v", quota)
			}
		})
	}
	for _, invalid := range []url.Values{
		{"from": {"bad"}}, {"to": {"bad"}}, {"from": query["to"], "to": query["from"]},
	} {
		callOK(t, app, http.MethodGet, routeAuthFiles, invalid, nil, http.StatusBadRequest, nil)
	}
}

func TestAccountAuthFilesFollowCredentialRouting(t *testing.T) {
	for _, mode := range []string{"allow", "deny"} {
		t.Run(mode, func(t *testing.T) {
			app := newConfiguredApp(t)
			if _, errSync := app.store.SyncKeys([]string{accountTestKeyA}, false); errSync != nil {
				t.Fatal(errSync)
			}
			scope := billing.CallerScope(accountTestKeyA)
			rule := billing.RouteRule{
				CredentialIDs: []string{
					billing.CredentialFingerprint("auth-codex-allowed"),
					billing.CredentialFingerprint("config-codex-exact"),
				},
				CredentialProviders: []billing.CredentialProviderSelector{
					{Source: billing.CredentialSourceAuthFiles, Provider: "claude"},
					{Source: billing.CredentialSourceAIProviders, Provider: "codex"},
				},
			}
			if mode == "deny" {
				rule = billing.RouteRule{DeniedCredentialIDs: []string{billing.CredentialFingerprint("auth-codex-denied")}}
			}
			if _, errRoute := app.store.CreateRoute(billing.Route{Name: "受限认证文件", Rule: rule}, []string{scope}); errRoute != nil {
				t.Fatal(errRoute)
			}
			hostGetCalls := 0
			app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
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
					switch payload.(map[string]string)["auth_index"] {
					case "codex-allowed":
						return json.RawMessage(`{"json":{"weight":-2}}`), nil
					case "claude-allowed":
						return json.RawMessage(`{"json":{"weight":10}}`), nil
					default:
						t.Fatalf("read a credential outside the route: %v", payload)
						return nil, nil
					}
				default:
					t.Fatalf("unexpected host method %q", method)
					return nil, nil
				}
			})
			access := viewAccess{APIKey: true, Scope: scope, Tracked: true}
			response := callAccount(t, app, routeAuthFiles, accountTestKeyA, nil)
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
			if hostGetCalls != 2 || payload.Files[0].Weight == nil || *payload.Files[0].Weight != 10 ||
				payload.Files[1].Weight == nil || *payload.Files[1].Weight != 0 {
				t.Fatalf("weight reads=%d, body=%s", hostGetCalls, response.Body)
			}
			hostGetCalls = 0

			request := ManagementRequest{Query: url.Values{"auth_index": {"codex-denied"}}}
			if response := app.authQuota(request, access); response.StatusCode != http.StatusNotFound {
				t.Fatalf("denied quota status = %d, body = %s", response.StatusCode, response.Body)
			}
			if hostGetCalls != 0 {
				t.Fatalf("denied credential made %d host.auth.get calls", hostGetCalls)
			}
		})
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
	response := app.authFiles(ManagementRequest{}, viewAccess{})
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
	_, errFetch := app.fetchAuthQuota("", hostAuthFile{AuthIndex: "xai-1", Type: "xai"})
	if errFetch == nil || errFetch.Error() != "API key credentials do not support this quota query" {
		t.Fatalf("error = %v", errFetch)
	}
}

func TestUpstreamErrorsRedactPhysicalCredential(t *testing.T) {
	app := newConfiguredApp(t)
	for _, tc := range []struct {
		status    int
		body, key string
	}{
		{http.StatusBadRequest, `{"error":{"message":"rejected dummy-secret-token"}}`, "backend.upstream_returned_http_value_value"},
		{http.StatusUnauthorized, `{"error":{"message":"rejected dummy-secret-token"}}`, "backend.upstream_credentials_invalid_detail"},
		{http.StatusUnauthorized, `{}`, "backend.upstream_credentials_invalid"},
	} {
		app.SetHostCaller(func(string, any) (json.RawMessage, error) {
			return mustJSONRaw(t, hostHTTPResponse{StatusCode: tc.status, Body: []byte(tc.body)}), nil
		})
		client := &authQuotaClient{hostCaller: app.hostCaller, token: "dummy-secret-token"}
		_, errCall := client.upstream(http.MethodGet, "https://example.invalid/quota", nil, nil)
		detail := messages.FromError(errCall)
		if errCall == nil || detail.Key != tc.key || strings.Contains(detail.Text, "dummy-secret-token") {
			t.Fatalf("error = %v, detail = %+v", errCall, detail)
		}
		if tc.body != `{}` && (!strings.Contains(detail.Text, "[REDACTED]") || detail.Params["v1"] != "rejected [REDACTED]") {
			t.Fatalf("credential was not redacted in both representations: %+v", detail)
		}
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
			if strings.HasSuffix(request.URL, "/rate-limit-reset-credits") {
				return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"credits":[{"reset_type":"codex_rate_limits","status":"available","expires_at":"2026-10-04T02:27:00Z"}]}`)}), nil
			}
			endpoint = request.URL
			if request.Headers.Get("Authorization") != "Bearer dummy-token" || request.Headers.Get("Chatgpt-Account-Id") != "dummy-account" {
				t.Fatalf("headers = %#v", request.Headers)
			}
			body := `{"plan_type":"pro","credits":{"balance":"12.5"},"rate_limit":{"primary_window":{"used_percent":38,"limit_window_seconds":604800}},"additional_rate_limits":[{"limit_name":"GPT-5.3-Codex-Spark","rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":18000},"secondary_window":{"used_percent":0,"limit_window_seconds":604800}}}],"rate_limit_reset_credits":{"available_count":1}}`
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
	if result.Plan != "Pro 200" || result.CreditBalance != "12.5" || len(result.Quota) != 3 {
		t.Fatalf("quota = %+v", result)
	}
	if len(result.RateLimitResetCredits) != 1 || result.RateLimitResetCredits[0].ExpiresAt != "2026-10-04T02:27:00Z" {
		t.Fatalf("reset credits = %+v", result.RateLimitResetCredits)
	}
	if result.Quota[0].RemainingPercent == nil || *result.Quota[0].RemainingPercent != 62 {
		t.Fatalf("remaining percent = %+v", result.Quota[0].RemainingPercent)
	}
	wantLabels := []string{"Weekly limit", "GPT-5.3-Codex-Spark 5-hour limit", "GPT-5.3-Codex-Spark Weekly limit"}
	for index, want := range wantLabels {
		if result.Quota[index].Label != want {
			t.Fatalf("quota[%d].Label = %q, want %q", index, result.Quota[index].Label, want)
		}
	}
}

func TestCodexResetCreditExpiry(t *testing.T) {
	for _, tc := range []struct {
		name, usage, credits           string
		status, wantCount, wantCredits int
		unavailable                    bool
	}{
		{"available only", `{"available_count":9}`, `{"credits":[{"reset_type":"codex_rate_limits","status":"available","expires_at":"2026-10-04T10:27:00+08:00"},{"resetType":"codex_rate_limits","status":"available","expiresAt":"2026-10-05T06:45:00+08:00"},{"reset_type":"codex_rate_limits","status":"consumed","expires_at":"2026-10-04T02:27:00Z"},{"reset_type":"other","status":"available","expires_at":"2026-10-04T02:27:00Z"},{"reset_type":"codex_rate_limits","status":"available","expires_at":""}]}`, 200, 2, 2, false},
		{"failure preserves windows and usage count", `{"available_count":1}`, `{}`, 503, 1, 0, true},
		{"invalid response", `{"available_count":1}`, `{}`, 200, 1, 0, true},
		{"no available credits", `{"available_count":0}`, `{"credits":[]}`, 200, 0, 0, false},
		{"unknown count stays unknown", `{}`, `{"credits":[]}`, 200, -1, 0, false},
		{"empty details preserve usage count", `{"available_count":2}`, `{"credits":[]}`, 200, 2, 0, false},
		{"detail count wins", `{"available_count":9}`, `{"availableCount":"3","credits":[]}`, 200, 3, 0, false},
		{"explicit zero wins", `{"available_count":9}`, `{"available_count":0,"credits":[{"reset_type":"codex_rate_limits","status":"available","expires_at":"2026-10-04T10:27:00+08:00"}]}`, 200, 0, 1, false},
		{"missing usage count still queries details", `{}`, `{"credits":[{"reset_type":"codex_rate_limits","status":"available","expires_at":"2026-10-04T10:27:00+08:00"}]}`, 200, 1, 1, false},
		{"zero usage count still queries details", `{"available_count":0}`, `{"available_count":2}`, 200, 2, 0, false},
		{"applicable count is not total count", `{"available_count":2}`, `{"applicable_available_count":0}`, 200, 2, 0, false},
		{"nonfinite detail count falls back", `{"available_count":2}`, `{"available_count":"NaN"}`, 200, 2, 0, false},
		{"failure without usage count", `{}`, `{}`, 503, -1, 0, true},
		{"malformed JSON", `{"available_count":1}`, `{`, 200, 1, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newConfiguredApp(t)
			calls := 0
			app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
				request := payload.(hostHTTPRequest)
				calls++
				if method != hostHTTPDo || request.Method != http.MethodGet || request.HostCallbackID != "dummy-callback" || request.Headers.Get("Chatgpt-Account-Id") != "dummy-account" || request.Headers.Get("Authorization") != "Bearer dummy-token" {
					t.Fatalf("unexpected request: %s %+v", method, request)
				}
				if strings.HasSuffix(request.URL, "/usage") {
					body := `{"rate_limit":{"primary_window":{"used_percent":38}},"rate_limit_reset_credits":` + tc.usage + `}`
					return mustJSONRaw(t, hostHTTPResponse{StatusCode: 200, Body: []byte(body)}), nil
				}
				if request.URL != "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits" || request.Headers.Get("OpenAI-Beta") != "codex-1" || request.Headers.Get("Originator") != "Codex Desktop" {
					t.Fatalf("unexpected expiry request: %+v", request)
				}
				return mustJSONRaw(t, hostHTTPResponse{StatusCode: tc.status, Body: []byte(tc.credits)}), nil
			})
			result := authQuotaResponse{}
			client := &authQuotaClient{
				hostCaller: app.hostCaller, callbackID: "dummy-callback", token: "dummy-token",
				credential: map[string]any{"account_id": "dummy-account"},
			}
			if err := client.fetchCodexQuota(&result); err != nil {
				t.Fatal(err)
			}
			if calls != 2 || len(result.RateLimitResetCredits) != tc.wantCredits || result.RateLimitResetCreditsUnavailable != tc.unavailable || len(result.Quota) != 1 || *result.Quota[0].RemainingPercent != 62 {
				t.Fatalf("calls = %d, result = %+v", calls, result)
			}
			if tc.wantCount < 0 {
				if result.RateLimitResetCreditsAvailableCount != nil {
					t.Fatalf("unexpected count: %d", *result.RateLimitResetCreditsAvailableCount)
				}
			} else if result.RateLimitResetCreditsAvailableCount == nil || *result.RateLimitResetCreditsAvailableCount != tc.wantCount {
				t.Fatalf("count = %v, want %d", result.RateLimitResetCreditsAvailableCount, tc.wantCount)
			}
			if tc.wantCredits > 0 && result.RateLimitResetCredits[0].ExpiresAt != "2026-10-04T10:27:00+08:00" {
				t.Fatalf("expiry not preserved: %+v", result.RateLimitResetCredits)
			}
		})
	}
}

func TestCodexResetCreditPreservesUnknownDateFormat(t *testing.T) {
	var object map[string]any
	if err := json.Unmarshal([]byte(`{"credits":[{"reset_type":"codex_rate_limits","status":"available","expires_at":"unknown-date-format"}]}`), &object); err != nil {
		t.Fatal(err)
	}
	result := normalizeCodexResetCredits(object)
	if result.invalidPayload || len(result.credits) != 1 || result.credits[0].ExpiresAt != "unknown-date-format" {
		t.Fatalf("summary = %+v", result)
	}
}

func TestCodexQuotaOrdersAndLabelsWindows(t *testing.T) {
	result := authQuotaResponse{Quota: []quotaRow{}}
	appendCodexRateLimit(&result, codexMainLimit, map[string]any{
		"allowed": false,
		"primary_window": map[string]any{
			"limit_window_seconds": float64(29 * 24 * 60 * 60),
		},
		"secondary_window": map[string]any{
			"limit_window_seconds": float64(5 * 60 * 60),
		},
	})
	if len(result.Quota) != 2 || result.Quota[0].Label != "5-hour limit" || result.Quota[1].Label != "Monthly limit" || result.Quota[0].RemainingPercent == nil || *result.Quota[0].RemainingPercent != 0 {
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
	client := &authQuotaClient{hostCaller: app.hostCaller, token: "token"}
	if errFetch := client.fetchClaudeQuota(&result); errFetch != nil {
		t.Fatal(errFetch)
	}
	if len(result.Quota) != 3 || result.Quota[0].Label != "5-hour limit" || result.Quota[1].Label != "Sonnet weekly limit" || result.Quota[2].Label != "Fable weekly limit" {
		t.Fatalf("quota = %+v", result.Quota)
	}
	if result.Plan != "Team" {
		t.Fatalf("plan = %q", result.Plan)
	}
}

func TestClaudeResetSpendsOnlyAUsableGrant(t *testing.T) {
	const resetID = "00112233-4455-4677-8899-aabbccddeeff"
	grants := `[{"id":"a","resets_total":1,"resets_left":1,"usable_now":true},{"id":"b","resets_total":1,"resets_left":1,"usable_now":true}]`
	for _, tc := range []struct {
		name, status, result, claimed, want string
	}{
		{"recommended grant", `{"eligible":true,"at_limit":true,"next_grant_id":"b","grants":` + grants + `}`, `{"result":"reset"}`, "b", ""},
		{"not rate limited", `{"eligible":true,"at_limit":false,"grants":` + grants + `}`, "", "", errClaudeResetNotLimited.Error()},
		{"unconfirmed result", `{"eligible":true,"at_limit":true,"grants":` + grants + `}`, `{}`, "a", "Claude did not confirm the reset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newConfiguredApp(t)
			claimed := ""
			app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
				if method == hostAuthGet {
					return json.RawMessage(`{"json":{"access_token":"dummy-upstream-token"}}`), nil
				}
				request := payload.(hostHTTPRequest)
				body := map[string]string{
					"https://api.anthropic.com/api/oauth/profile":                          `{"organization":{"uuid":"00112233-4455-6677-8899-AABBCCDDEEFF"}}`,
					"https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1": `{"cedar_ember":` + tc.status + `}`,
				}[request.URL]
				if request.Method == http.MethodPost {
					var claim map[string]string
					if request.URL != "https://api.anthropic.com/api/organizations/00112233-4455-6677-8899-aabbccddeeff/reset_rate_limits" ||
						json.Unmarshal(request.Body, &claim) != nil || claim["program"] != "cedar_ember" || claim["request_id"] != resetID {
						t.Fatalf("unexpected claim: %s %s", request.URL, request.Body)
					}
					claimed, body = claim["grant_id"], tc.result
				}
				return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(body)}), nil
			})
			got := ""
			client, err := app.openAuthQuotaClient("", hostAuthFile{AuthIndex: "claude-1", Type: "claude"})
			if err != nil {
				t.Fatal(err)
			}
			defer client.close()
			if err := client.resetClaudeQuota(resetID); err != nil {
				got = err.Error()
			}
			if claimed != tc.claimed || got != tc.want {
				t.Fatalf("claimed = %q, err = %q", claimed, got)
			}
		})
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
	client := &authQuotaClient{hostCaller: app.hostCaller, token: "token"}
	if errFetch := client.fetchClaudeQuota(&result); errFetch != nil {
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
	result, errFetch := app.fetchAuthQuota("", hostAuthFile{AuthIndex: "ag-1", Type: "antigravity"})
	if errFetch != nil {
		t.Fatal(errFetch)
	}
	if requestProject != "nested-project" || len(result.Quota) != 2 || result.Quota[0].Label != "5-hour limit" || result.Quota[1].Label != "Weekly limit" || result.Plan != "Google AI Pro" {
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
	client := &authQuotaClient{hostCaller: app.hostCaller, token: "token"}
	if errFetch := client.fetchKimiQuota(&result); errFetch != nil {
		t.Fatal(errFetch)
	}
	if len(result.Quota) != 1 {
		t.Fatalf("quota = %+v", result.Quota)
	}
	row := result.Quota[0]
	if row.Label != "Coding 5 小时限额" || row.RemainingPercent == nil || *row.RemainingPercent != 80 || row.ResetAt != fetchedAt.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("quota = %+v", result.Quota)
	}
	if row.LabelMessage.Key != "" {
		t.Fatal("upstream label must not be interpreted as plugin text")
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
	client := &authQuotaClient{hostCaller: app.hostCaller, token: "token", credential: map[string]any{"user_id": "user"}}
	if errFetch := client.fetchXAIQuota(&result); errFetch != nil {
		t.Fatal(errFetch)
	}
	if len(result.Quota) != 2 || result.Quota[0].Label != "grok code usage" || result.Quota[0].RemainingPercent == nil || *result.Quota[0].RemainingPercent != 55 || result.Quota[1].Label != "Grok Vision usage" {
		t.Fatalf("quota = %+v", result.Quota)
	}
}

func TestInvalidCredentialProxyFailsInsteadOfSendingHostRequest(t *testing.T) {
	app := newConfiguredApp(t)
	app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
		if method != hostAuthGet {
			t.Fatalf("unexpected host method %q", method)
		}
		return json.RawMessage(`{"auth_index":"codex-1","json":{"access_token":"dummy-token","proxy_url":"invalid://dummy-proxy:1080"}}`), nil
	})
	_, errFetch := app.fetchAuthQuota("", hostAuthFile{AuthIndex: "codex-1", Type: "codex"})
	if errFetch == nil || errFetch.Error() != "Invalid auth file proxy" {
		t.Fatalf("error = %v", errFetch)
	}
}

func TestXAIQuotaCombinesWeeklyMonthlyAndOnDemand(t *testing.T) {
	responses := map[string]string{
		"https://cli-chat-proxy.grok.com/v1/billing?format=credits": `{"config":{"creditUsagePercent":62.5,"currentPeriod":{"end":"2026-09-08T00:00:00Z"}}}`,
		"https://cli-chat-proxy.grok.com/v1/billing":                `{"config":{"monthlyLimit":{"val":1000},"used":{"val":1250},"onDemandCap":{"val":500},"prepaidBalance":{"val":1234},"billingPeriodEnd":"2026-10-01T00:00:00Z"}}`,
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
	client := &authQuotaClient{hostCaller: app.hostCaller, token: "token", credential: map[string]any{"user_id": "user"}}
	if errFetch := client.fetchXAIQuota(&result); errFetch != nil {
		t.Fatal(errFetch)
	}
	want := []string{"Weekly limit", "Monthly allowance", "Pay-as-you-go allowance"}
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
	if result.CreditBalance != "12.34" || result.CreditCurrency != "USD" {
		t.Fatalf("prepaid credits = %q %q", result.CreditBalance, result.CreditCurrency)
	}
}

func TestAuthQuotaReset(t *testing.T) {
	const resetID = "00112233-4455-4677-8899-aabbccddeeff"
	for _, tc := range []struct {
		name           string
		upstreamStatus int
		upstreamBody   string
		want           int
	}{
		{"admin", 204, "", 200},
		{"account", 204, "", 200},
		{"masked name", 204, "", 200},
		{"non-JSON success", 200, "accepted", 200},
		{"no credits", 409, `{"error":{"message":"No reset credits available"}}`, 502},
		{"expired credentials", 401, `{"error":{"message":"dummy-upstream-token expired"}}`, 502},
		{"transport failure", 0, "", 502},
		{"permission disabled", 0, "", 403},
		{"unknown key", 0, "", 401},
		{"invalid reset ID", 0, "", 400},
		{"missing revision", 0, "", 409},
		{"replaced file", 0, "", 409},
		{"missing file", 0, "", 404},
		{"unsupported provider", 0, "", 422},
		{"disabled file", 0, "", 422},
		{"denied credential", 0, "", 404},
		{"outside allowlist", 0, "", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp(t)
			allowed := tc.name != "admin" && tc.name != "permission disabled"
			config := string(testConfigYAML(t, true)) + "allow_api_key_quota_reset: " + strconv.FormatBool(allowed) +
				"\nmask_api_key_view_emails: " + strconv.FormatBool(tc.name == "masked name") + "\n"
			t.Cleanup(app.Shutdown)
			if err := app.configure(mustMarshal(t, LifecycleRequest{ConfigYAML: []byte(config)})); err != nil {
				t.Fatal(err)
			}
			if _, err := app.store.SyncKeys([]string{accountTestKeyA}, false); err != nil {
				t.Fatal(err)
			}
			file := hostAuthFile{ID: "reset-file", AuthIndex: "codex-1", Name: "user@example.com.json", Type: "codex", Source: "file"}
			req := ManagementRequest{
				Method: http.MethodGet, Path: resourceBase + routeAuthQuotaReset, HostCallbackID: "reset-callback",
				Headers: http.Header{"Authorization": {"Bearer " + accountTestKeyA}},
				Query:   url.Values{"auth_index": {file.AuthIndex}, "auth_name": {file.Name}, "auth_revision": {""}},
			}
			req.Headers.Set("X-Quota-Reset-ID", resetID)
			switch tc.name {
			case "admin":
				req.Method, req.Path = http.MethodPost, managementBase+routeAuthQuotaReset
			case "unknown key":
				req.Headers.Set("Authorization", "Bearer dummy-unknown-key")
			case "invalid reset ID":
				req.Headers.Set("X-Quota-Reset-ID", "invalid")
			case "missing revision":
				req.Query.Del("auth_revision")
			case "replaced file":
				file.ModTime = time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
			case "missing file":
				req.Query.Set("auth_index", "missing")
			case "unsupported provider":
				file.Type = "xai"
			case "disabled file":
				file.Disabled = true
			case "denied credential", "outside allowlist":
				rule := billing.RouteRule{DeniedCredentialIDs: []string{billing.CredentialFingerprint(file.ID)}}
				if tc.name == "outside allowlist" {
					rule = billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("another-file")}}
				}
				if _, err := app.store.CreateRoute(billing.Route{Name: "reset access", Rule: rule}, []string{billing.CallerScope(accountTestKeyA)}); err != nil {
					t.Fatal(err)
				}
			}
			hostCalls, reads, consumes := 0, 0, 0
			app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
				hostCalls++
				switch method {
				case hostRoutingResetCooldown:
					return json.RawMessage(`{}`), nil
				case hostAuthList:
					return mustJSONRaw(t, hostAuthListResponse{Files: []hostAuthFile{file}}), nil
				case hostAuthGet:
					reads++
					return json.RawMessage(`{"json":{"access_token":"dummy-upstream-token","account_id":"account-7"}}`), nil
				case hostHTTPDo:
					consumes++
					upstream := payload.(hostHTTPRequest)
					if upstream.Method != http.MethodPost || upstream.URL != "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume" ||
						upstream.HostCallbackID != req.HostCallbackID || upstream.Headers.Get("Chatgpt-Account-Id") != "account-7" {
						t.Fatalf("unexpected reset request: %+v", upstream)
					}
					var body map[string]string
					if err := json.Unmarshal(upstream.Body, &body); err != nil || body["redeem_request_id"] != resetID {
						t.Fatalf("reset ID was not preserved: %s", upstream.Body)
					}
					if tc.name == "transport failure" {
						return nil, fmt.Errorf("dummy-upstream-token: connection closed")
					}
					return mustJSONRaw(t, hostHTTPResponse{StatusCode: tc.upstreamStatus, Body: []byte(tc.upstreamBody)}), nil
				default:
					t.Fatalf("unexpected host method %q", method)
					return nil, nil
				}
			})
			access, _ := app.apiKeyViewAccess(req)
			var profile accountProfileResponse
			if err := json.Unmarshal(app.accountProfile(access).Body, &profile); err != nil {
				t.Fatal(err)
			}
			if profile.CanResetAuthQuota != (allowed && access.Tracked) {
				t.Fatalf("incorrect reset permission: %+v", profile)
			}
			if tc.name == "masked name" {
				var files authFileListResponse
				if err := json.Unmarshal(app.authFiles(ManagementRequest{}, access).Body, &files); err != nil {
					t.Fatal(err)
				}
				if files.Files[0].Name == file.Name {
					t.Fatal("file name was not masked")
				}
				req.Query.Set("auth_name", files.Files[0].Name)
			}
			raw, err := app.handleManagement(mustMarshal(t, req))
			if err != nil {
				t.Fatal(err)
			}
			var response ManagementResponse
			decodeResult(t, raw, &response)
			if response.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d: %s", response.StatusCode, tc.want, response.Body)
			}
			wantCalls := 0
			if tc.want == 200 || tc.want == 502 {
				wantCalls = 1
			}
			if reads != wantCalls || consumes != wantCalls {
				t.Fatalf("credential reads = %d, resets = %d; want %d", reads, consumes, wantCalls)
			}
			if (tc.want == 400 || tc.want == 401 || tc.want == 403) && hostCalls != 0 {
				t.Fatalf("rejected request made %d host calls", hostCalls)
			}
			if tc.want == 200 && string(response.Body) != `{"cooldown_cleared":true,"reset":true}` {
				t.Fatalf("unexpected reset result: %s", response.Body)
			}
			if strings.Contains(string(response.Body), "dummy-upstream-token") {
				t.Fatal("response leaked credentials")
			}
			if tc.name != "admin" && !strings.Contains(response.Headers.Get("Cache-Control"), "no-store") {
				t.Fatal("resource response is cacheable")
			}
		})
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

func TestStandardQuotaFetchAndReset(t *testing.T) {
	quota := (authQuotaResponse{CreditBalance: "9.5", CreditCurrency: "USD", Quota: []quotaRow{
		{Label: "Unknown"},
		{Label: "Known usage", Used: floatPointer(25), Limit: floatPointer(100)},
		{Label: "Invalid", RemainingPercent: floatPointer(math.Inf(1))},
	}}).normalized()
	if len(quota.Summary) != 1 || quota.Summary[0].Value != 9.5 || quota.Summary[0].Format != "currency" || len(quota.Groups) != 1 || len(quota.Groups[0].Buckets) != 1 || quota.Groups[0].Buckets[0].RemainingFraction != 0.75 {
		t.Fatalf("unknown quota was fabricated or known values were lost: %+v", quota)
	}
	quota = (authQuotaResponse{CreditBalance: "0", CreditsUnlimited: true}).normalized()
	if len(quota.Summary) != 0 || len(quota.Groups) != 1 || quota.Groups[0].Buckets[0].Window != "Unlimited credits" {
		t.Fatalf("unlimited credits presented as exhausted: %+v", quota)
	}
	for _, cooldownFails := range []bool{false, true} {
		app := newConfiguredApp(t)
		upstreamCalls, cooldownCalls := 0, 0
		app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
			switch method {
			case hostAuthList:
				return json.RawMessage(`{"files":[{"id":"dummy-auth","auth_index":"dummy-index","type":"codex","source":"file"}]}`), nil
			case hostAuthGet:
				return json.RawMessage(`{"json":{"access_token":"dummy-token"}}`), nil
			case hostHTTPDo:
				request := payload.(hostHTTPRequest)
				if request.HostCallbackID != "dummy-callback" {
					t.Fatal("lost callback scope")
				}
				upstreamCalls++
				if request.Method == "GET" {
					return mustJSONRaw(t, hostHTTPResponse{StatusCode: 200, Body: []byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_at":1900000000}}}`)}), nil
				}
				var body map[string]string
				if json.Unmarshal(request.Body, &body) != nil || !quotaResetIDPattern.MatchString(body["redeem_request_id"]) {
					t.Fatal("invalid reset identity")
				}
				return mustJSONRaw(t, hostHTTPResponse{StatusCode: 204}), nil
			case hostRoutingResetCooldown:
				cooldownCalls++
				if cooldownFails {
					return nil, errors.New("callback unavailable")
				}
				return json.RawMessage(`{}`), nil
			default:
				t.Fatalf("unexpected host call %q", method)
				return nil, nil
			}
		})
		req := mustMarshal(t, QuotaRequest{AuthIndex: "dummy-index", AuthID: "dummy-auth", Provider: "codex", HostCallbackID: "dummy-callback"})
		raw, err := app.HandleMethod(MethodQuotaFetch, req)
		if err != nil {
			t.Fatal(err)
		}
		var quota QuotaResponse
		decodeResult(t, raw, &quota)
		if quota.Subscription == nil || quota.Subscription.Plan != "Plus" || len(quota.Groups) != 1 || quota.Groups[0].Buckets[0].RemainingFraction != 0.75 {
			t.Fatalf("quota=%+v", quota)
		}
		raw, err = app.HandleMethod(MethodQuotaReset, req)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Success bool
			Message string
		}
		decodeResult(t, raw, &result)
		if !result.Success || (result.Message != "") != cooldownFails || upstreamCalls != 3 || cooldownCalls != 1 {
			t.Fatalf("reset=%+v upstream=%d cooldown=%d", result, upstreamCalls, cooldownCalls)
		}
	}
}

package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

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

func TestAntigravityQuotaPreservesGroupsAndFallbacks(t *testing.T) {
	const groups = `{"body":{"groups":[
		{"displayName":"Claude and GPT Models","buckets":[
			{"bucketId":"claude-weekly","window":"weekly","remainingFraction":0.99,"resetTime":"2026-11-02T01:49:00Z"},
			{"bucketId":"claude-five","window":"5h","remainingFraction":0.9,"resetTime":"2026-10-27T01:49:00Z"}]},
		{"display_name":"Gemini Models","buckets":[
			{"bucket_id":"gemini-weekly","window":"week","remaining_fraction":" 72 % ","reset_time":"2026-11-01T01:49:00Z"},
			{"bucket_id":"gemini-five","window":"five_hour","remainingFraction":" 0.8 "},
			{"remainingFraction":"NaN"},{"remainingFraction":"+Inf"},{"remainingFraction":"invalid"},{}]}
	]}}`
	for _, tc := range []struct {
		name      string
		responses []hostHTTPResponse
		wantRows  int
		wantError bool
	}{
		{"all model groups", []hostHTTPResponse{{StatusCode: 200, Body: []byte(groups)}}, 4, false},
		{"empty and unusable quotas fall back", []hostHTTPResponse{
			{StatusCode: 200, Body: []byte(`{"groups":[]}`)},
			{StatusCode: 200, Body: []byte(`{"groups":[{"buckets":[{"remainingFraction":"NaN"}]}]}`)},
			{StatusCode: 200, Body: []byte(groups)},
		}, 4, false},
		{"empty success survives later failures", []hostHTTPResponse{
			{StatusCode: 200, Body: []byte(`{"groups":[]}`)},
			{StatusCode: 503}, {StatusCode: 503},
		}, 0, false},
		{"all endpoints fail", []hostHTTPResponse{{StatusCode: 503}, {StatusCode: 503}, {StatusCode: 503}}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newConfiguredApp(t)
			calls := 0
			app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
				switch method {
				case hostAuthGet:
					return json.RawMessage(`{"auth_index":"ag-1","json":{"metadata":{"token":{"access_token":"nested-token"}},"installed":{"project_id":"nested-project"}}}`), nil
				case hostHTTPDo:
					request := payload.(hostHTTPRequest)
					if request.Headers.Get("Authorization") != "Bearer nested-token" || request.Headers.Get("User-Agent") != "antigravity/hub/2.23.0 darwin/arm64" {
						t.Fatalf("unexpected quota request headers: %v", request.Headers)
					}
					if strings.Contains(request.URL, "retrieveUserQuotaSummary") {
						var body map[string]string
						if err := json.Unmarshal(request.Body, &body); err != nil {
							t.Fatal(err)
						}
						if body["project"] != "nested-project" {
							t.Fatalf("project = %q", body["project"])
						}
						if calls >= len(tc.responses) {
							t.Fatal("unexpected quota fallback")
						}
						response := tc.responses[calls]
						calls++
						return mustJSONRaw(t, response), nil
					}
					return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"body":{"currentTier":{"id":"pro","name":"Google AI Pro"}}}`)}), nil
				default:
					t.Fatalf("unexpected host method %q", method)
					return nil, nil
				}
			})
			result, err := app.fetchAuthQuota("", hostAuthFile{AuthIndex: "ag-1", Type: "antigravity"})
			if (err != nil) != tc.wantError || len(result.Quota) != tc.wantRows || calls != len(tc.responses) {
				t.Fatalf("error = %v, calls = %d, quota = %+v", err, calls, result.Quota)
			}
			if !tc.wantError && result.Plan != "Google AI Pro" {
				t.Fatalf("plan = %q", result.Plan)
			}
			expected := []struct {
				group, label, reset string
				percent             float64
			}{
				{"Claude and GPT Models", "5-hour limit", "2026-10-27T01:49:00Z", 90},
				{"Claude and GPT Models", "Weekly limit", "2026-11-02T01:49:00Z", 99},
				{"Gemini Models", "5-hour limit", "", 80},
				{"Gemini Models", "Weekly limit", "2026-11-01T01:49:00Z", 72},
			}
			for i, row := range result.Quota {
				want := expected[i]
				if row.GroupLabel != want.group || row.Label != want.label || row.RemainingPercent == nil || *row.RemainingPercent != want.percent || row.ResetAt != want.reset {
					t.Fatalf("quota row %d = %+v, want %+v", i, row, want)
				}
			}
		})
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

func TestPaidXAIQuotaCombinesWeeklyMonthlyAndOnDemand(t *testing.T) {
	responses := map[string]string{
		"https://cli-chat-proxy.grok.com/v1/billing?format=credits": `{"config":{"creditUsagePercent":62.5,"currentPeriod":{"end":"2026-09-08T00:00:00Z"}}}`,
		"https://cli-chat-proxy.grok.com/v1/billing":                `{"config":{"monthlyLimit":{"val":1000},"used":{"val":1250},"onDemandCap":{"val":500},"prepaidBalance":{"val":1234},"billingPeriodEnd":"2026-10-01T00:00:00Z"}}`,
	}
	app := newConfiguredApp(t)
	app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
		if method == hostAuthGet {
			return json.RawMessage(`{"json":{"access_token":"e30.eyJ0aWVyIjoxfQ.sig","sub":"dummy-user"}}`), nil
		}
		request := payload.(hostHTTPRequest)
		body, ok := responses[request.URL]
		if !ok {
			t.Fatalf("unexpected endpoint %q", request.URL)
		}
		return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(body)}), nil
	})
	result, errFetch := app.fetchAuthQuota("", hostAuthFile{AuthIndex: "xai-1", Type: "xai"})
	if errFetch != nil {
		t.Fatal(errFetch)
	}
	if result.Plan != "Paid" {
		t.Fatalf("plan = %q", result.Plan)
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

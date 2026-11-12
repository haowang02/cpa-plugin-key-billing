package plugin

import (
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func quotaUsageByLabel(t *testing.T, quota *authFileQuota) map[string]*billing.UsageTotal {
	t.Helper()
	if quota == nil {
		t.Fatal("no quota")
	}
	usage := make(map[string]*billing.UsageTotal)
	for _, row := range quota.Quota {
		usage[row.Label] = row.Usage
	}
	return usage
}

func TestObservedQuotaUsageStartsAfterEarlyResetAndSkipsAdditionalLimitModels(t *testing.T) {
	app, _ := newAppWithPriceAndState(t, true)
	base := app.store.Now().Add(-3 * time.Hour).Truncate(time.Second)
	unix := func(at time.Time) string { return strconv.FormatInt(at.Unix(), 10) }
	fiveHourReset, weeklyReset := base.Add(4*time.Hour), base.Add(5*24*time.Hour)
	codex := func(primary string, primaryReset time.Time, secondary string) http.Header {
		return http.Header{
			"X-Codex-Primary-Used-Percent": {primary}, "X-Codex-Primary-Window-Minutes": {"300"}, "X-Codex-Primary-Reset-At": {unix(primaryReset)},
			"X-Codex-Secondary-Used-Percent": {secondary}, "X-Codex-Secondary-Window-Minutes": {"10080"}, "X-Codex-Secondary-Reset-At": {unix(weeklyReset)},
		}
	}
	for _, event := range []struct {
		offset  time.Duration
		model   string
		apiKey  string
		tokens  int64
		headers http.Header
	}{
		{-2 * time.Hour, "gpt-5.5", "sk-dummy-one", 1000, codex("90", base.Add(-time.Hour), "10")},
		{0, "gpt-5.5", "sk-dummy-one", 100, codex("40", fiveHourReset, "20")},
		{10 * time.Minute, "gpt-5.5", "sk-dummy-one", 200, codex("85", fiveHourReset, "30")},
		// The provider reset the counter early but kept the window's reset time.
		{20 * time.Minute, "gpt-5.5", "sk-dummy-one", 400, codex("0", fiveHourReset, "31")},
		{30 * time.Minute, "gpt-5.5", "sk-dummy-one", 800, nil},
		{40 * time.Minute, "gpt-5.3-codex-spark", "sk-dummy-one", 1600, http.Header{
			"X-Codex-Active-Limit": {"codex_bengalfox"}, "X-Codex-Bengalfox-Limit-Name": {"GPT-5.3-Codex-Spark"},
			"X-Codex-Bengalfox-Primary-Used-Percent": {"10"}, "X-Codex-Bengalfox-Primary-Window-Minutes": {"300"},
			"X-Codex-Bengalfox-Primary-Reset-At": {unix(base.Add(5 * time.Hour))},
		}},
		{50 * time.Minute, "gpt-5.5", "sk-dummy-two", 3200, codex("3", fiveHourReset, "32")},
	} {
		publishUsageRecord(t, app, UsageRecord{Provider: "codex", AuthType: "oauth", AuthIndex: "codex-1", APIKey: event.apiKey,
			Model: event.model, RequestedAt: base.Add(event.offset), ResponseHeaders: event.headers,
			Detail: UsageDetail{InputTokens: event.tokens, TotalTokens: event.tokens}})
	}
	app.SetHostCaller(func(string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"files":[{"auth_index":"codex-1","name":"dummy.json","type":"codex"}]}`), nil
	})
	files, err := app.listAuthFiles(viewAccess{}, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	usage := quotaUsageByLabel(t, files[0].Quota)
	if fiveHour := usage["5-hour limit"]; fiveHour == nil || fiveHour.Tokens != 4400 || math.Abs(fiveHour.CostUSD-0.0044) > 1e-12 {
		t.Fatalf("5-hour usage = %+v", fiveHour)
	}
	if weekly := usage["Weekly limit"]; weekly == nil || weekly.Tokens != 5700 {
		t.Fatalf("weekly usage = %+v", weekly)
	}
	if spark := usage["GPT-5.3-Codex-Spark 5-hour limit"]; spark == nil || spark.Tokens != 1600 {
		t.Fatalf("spark usage = %+v", spark)
	}
	files, err = app.listAuthFiles(viewAccess{APIKey: true, Tracked: true, Scope: billing.CallerScope("sk-dummy-one")}, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if fiveHour := quotaUsageByLabel(t, files[0].Quota)["5-hour limit"]; fiveHour == nil || fiveHour.Tokens != 1200 {
		t.Fatalf("scoped 5-hour usage = %+v", fiveHour)
	}
}

func TestFetchedQuotaKeysUnifiedWindowsAndCoversModelFamilies(t *testing.T) {
	app, _ := newAppWithPriceAndState(t, true)
	now := app.store.Now()
	for _, event := range []struct {
		offset time.Duration
		model  string
		tokens int64
	}{
		{-6 * time.Hour, "claude-opus-4-1", 400},
		{-time.Hour, "claude-opus-4-1", 100},
		{-30 * time.Minute, "claude-sonnet-4-5", 200},
	} {
		publishUsageRecord(t, app, UsageRecord{Provider: "claude", AuthType: "oauth", AuthIndex: "claude-1", APIKey: "sk-dummy",
			Model: event.model, RequestedAt: now.Add(event.offset), Detail: UsageDetail{InputTokens: event.tokens, TotalTokens: event.tokens}})
	}
	publishUsageRecord(t, app, UsageRecord{Provider: "claude", AuthType: "oauth", AuthIndex: "claude-1", APIKey: "sk-dummy",
		Model: "claude-sonnet-4-5", RequestedAt: now, Detail: UsageDetail{InputTokens: 50, TotalTokens: 50},
		ResponseHeaders: http.Header{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.9"},
			"Anthropic-Ratelimit-Unified-5h-Reset": {strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}}})
	resetAt := func(after time.Duration) string { return now.Add(after).UTC().Format(time.RFC3339) }
	usage := `{"five_hour":{"utilization":30,"resets_at":"` + resetAt(time.Hour) + `"},` +
		`"seven_day_opus":{"utilization":10,"resets_at":"` + resetAt(24*time.Hour) + `"},` +
		`"seven_day_sonnet":{"utilization":40,"resets_at":"` + resetAt(-time.Minute) + `"},` +
		`"seven_day_oauth_apps":{"utilization":5,"resets_at":"` + resetAt(24*time.Hour) + `"}}`
	app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case hostAuthList:
			return json.RawMessage(`{"files":[{"auth_index":"claude-1","name":"claude.json","type":"claude"}]}`), nil
		case hostAuthGet:
			return json.RawMessage(`{"json":{"access_token":"dummy-token"}}`), nil
		}
		if payload.(hostHTTPRequest).URL != "https://api.anthropic.com/api/oauth/usage" {
			return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusNotFound}), nil
		}
		return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(usage)}), nil
	})
	response := app.authQuota(ManagementRequest{Query: url.Values{"auth_index": {"claude-1"}}}, viewAccess{})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	// Only the unified windows are reported together; observed reports replace them and nothing else.
	// A window whose reset time has passed has no current period to meter.
	want := map[string]struct {
		limitKey string
		tokens   int64
	}{"5-hour limit": {claudeUnifiedLimit, 350}, "Opus weekly limit": {"", 500}, "Sonnet weekly limit": {"", -1}, "OAuth Apps weekly limit": {"", -1}}
	check := func(rows []quotaRow) {
		t.Helper()
		if len(rows) != len(want) {
			t.Fatalf("rows: %+v", rows)
		}
		for _, row := range rows {
			expected, ok := want[row.Label]
			if !ok || row.LimitKey != expected.limitKey || expected.tokens < 0 && row.Usage != nil ||
				expected.tokens >= 0 && (row.Usage == nil || row.Usage.Tokens != expected.tokens) {
				t.Fatalf("%s: limit %q, usage %+v", row.Label, row.LimitKey, row.Usage)
			}
		}
	}
	var result authFileQuota
	if err := json.Unmarshal(response.Body, &result); err != nil {
		t.Fatal(err)
	}
	check(result.Quota)
	// The auth file list reuses the query, which is newer than the request's report of the 5-hour window.
	files, err := app.listAuthFiles(viewAccess{}, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if quota := files[0].Quota; quota == nil || *quota.Quota[0].RemainingPercent != 70 || !quota.FetchedAt.Equal(result.FetchedAt) {
		t.Fatalf("listed quota: %+v", quota)
	}
	check(files[0].Quota.Quota)
}

func TestQuotaUsageSeparatesAntigravityGroups(t *testing.T) {
	app, _ := newAppWithPriceAndState(t, true)
	now := app.store.Now().Truncate(time.Second)
	for _, event := range []struct {
		model, apiKey string
		offset        time.Duration
		tokens        int64
	}{
		{"gemini-3-flash", "sk-dummy-one", -time.Hour, 100},
		{"claude-sonnet-4-6", "sk-dummy-one", -time.Hour, 200},
		{"gpt-oss-120b", "sk-dummy-one", -time.Hour, 400},
		{"gemini-4-argon-high", "sk-dummy-one", -6 * time.Hour, 800},
		{"gemini-3-flash", "sk-dummy-two", -time.Hour, 1600},
		{"gemini-3-flash", "sk-dummy-one", -4 * time.Hour, 3200},
		{"gemini-3-flash", "sk-dummy-one", -4*time.Hour - time.Second, 6400},
		{"gemini-3-flash", "sk-dummy-one", -7 * 24 * time.Hour, 12800},
	} {
		// One priced alias for every upstream model keeps cost and grouping independent.
		publishUsageRecord(t, app, UsageRecord{Provider: "antigravity", AuthType: "oauth", AuthIndex: "ag-1",
			APIKey: event.apiKey, Model: event.model, Alias: "gpt-5.5", RequestedAt: now.Add(event.offset),
			Detail: UsageDetail{InputTokens: event.tokens, TotalTokens: event.tokens}})
	}
	var groups []any
	for _, label := range []string{"Gemini Models", "Claude & GPT Models", "Gemini 4 Argon", "Gemini 5 Argon"} {
		groups = append(groups, map[string]any{"displayName": label, "buckets": []any{
			map[string]any{"window": "weekly", "remainingFraction": 0.58, "resetTime": now.Add(24 * time.Hour).Format(time.RFC3339)},
			map[string]any{"window": "5h", "remainingFraction": 1.0, "resetTime": now.Add(time.Hour).Format(time.RFC3339)},
		}})
	}
	for _, tc := range []struct {
		scope  string
		tokens []int64
	}{
		{"", []int64{4900, 11300, 600, 600, 0, 800, -1, -1}},
		{billing.CallerScope("sk-dummy-one"), []int64{3300, 9700, 600, 600, 0, 800, -1, -1}},
	} {
		quota := authFileQuota{Quota: antigravityQuotaRows(map[string]any{"groups": groups})}
		if err := app.attachQuotaUsage(tc.scope, map[quotaCredential]*authFileQuota{{"ag-1", "antigravity"}: &quota}); err != nil {
			t.Fatal(err)
		}
		if len(quota.Quota) != len(tc.tokens) {
			t.Fatalf("quota rows = %+v", quota.Quota)
		}
		for i, expected := range tc.tokens {
			row := quota.Quota[i]
			if expected < 0 {
				if row.Usage != nil || row.UsageQuery != nil {
					t.Fatalf("unknown group reports usage: %+v", row)
				}
				continue
			}
			if row.Usage == nil || row.UsageQuery == nil || row.Usage.Tokens != expected ||
				math.Abs(row.Usage.CostUSD-float64(expected)*1e-6) > 1e-12 {
				t.Fatalf("scoped=%v, row %d usage=%+v, want %d tokens", tc.scope != "", i, row.Usage, expected)
			}
		}
	}
}

func TestQuotaHistoryFollowsPlanChangesAndMatchesCurrentUsage(t *testing.T) {
	app, _ := newAppWithPriceAndState(t, true)
	now := app.store.Now()
	base := now.Add(-241 * time.Hour).Truncate(time.Hour)
	hour := func(value int) time.Time { return base.Add(time.Duration(value) * time.Hour) }
	unix := func(value int) string { return strconv.FormatInt(hour(value).Unix(), 10) }
	publish := func(at int, provider, model, apiKey string, tokens int64, headers http.Header) {
		publishUsageRecord(t, app, UsageRecord{Provider: provider, AuthType: "oauth", AuthIndex: "codex-1", APIKey: apiKey,
			Model: model, RequestedAt: hour(at), ResponseHeaders: headers, Detail: UsageDetail{InputTokens: tokens, TotalTokens: tokens}})
	}
	for _, event := range []struct {
		at                      int
		plan                    string
		fiveHour, fiveHourReset int // fiveHour is -1 when the plan has no 5-hour window
		weekly, weeklyReset     int
		apiKey                  string
		tokens                  int64
	}{
		{0, "plus", 10, 5, 5, 167, "sk-dummy-one", 100},
		{1, "plus", 30, 5, 7, 167, "sk-dummy-one", 200},
		{48, "plus", 5, 53, 20, 167, "sk-dummy-one", 400},
		{95, "plus", 50, 100, 25, 167, "sk-dummy-one", 1600},
		// Pro keeps only a weekly window, reported as the primary one, and restarts it.
		{96, "prolite", -1, 0, 3, 264, "sk-dummy-one", 800},
		// Back on Plus, the 5-hour window returns while the weekly one runs on.
		{192, "plus", 2, 197, 40, 264, "sk-dummy-two", 3200},
		{237, "plus", 20, 242, 45, 264, "sk-dummy-two", 6400},
		{239, "plus", 25, 242, 46, 264, "sk-dummy-two", 100},
	} {
		headers := http.Header{"X-Codex-Plan-Type": {event.plan}, "X-Codex-Active-Limit": {"premium"}}
		weeklyRole := "Secondary"
		if event.fiveHour < 0 {
			weeklyRole = "Primary"
		} else {
			headers["X-Codex-Primary-Used-Percent"] = []string{strconv.Itoa(event.fiveHour)}
			headers["X-Codex-Primary-Window-Minutes"] = []string{"300"}
			headers["X-Codex-Primary-Reset-At"] = []string{unix(event.fiveHourReset)}
		}
		headers["X-Codex-"+weeklyRole+"-Used-Percent"] = []string{strconv.Itoa(event.weekly)}
		headers["X-Codex-"+weeklyRole+"-Window-Minutes"] = []string{"10080"}
		headers["X-Codex-"+weeklyRole+"-Reset-At"] = []string{unix(event.weeklyReset)}
		publish(event.at, "codex", "gpt-5.5", event.apiKey, event.tokens, headers)
	}
	// Image requests report their own pool's daily window under the main headers, unnamed on older responses.
	image := func(at int, used string) http.Header {
		return http.Header{"X-Codex-Primary-Used-Percent": {used}, "X-Codex-Primary-Window-Minutes": {"1440"}, "X-Codex-Primary-Reset-At": {unix(at + 24)}}
	}
	publish(238, "codex", "gpt-image-2", "sk-dummy-two", 50, image(238, "0"))
	named := image(240, "10")
	named.Set("X-Codex-Active-Limit", "images")
	publish(240, "codex", "gpt-image-2", "sk-dummy-two", 70, named)
	// A request to another provider on the same auth index belongs to none of these windows.
	publish(238, "claude", "claude-sonnet-4-5", "sk-dummy-two", 9000, http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.5"}, "Anthropic-Ratelimit-Unified-5h-Reset": {unix(242)}})
	app.SetHostCaller(func(string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"files":[{"auth_index":"codex-1","name":"dummy.json","type":"codex"}]}`), nil
	})
	history := func(access viewAccess) authFileHistoryResponse {
		t.Helper()
		response := app.authFileHistory(ManagementRequest{Query: url.Values{"auth_index": {"codex-1"}}}, access)
		var result authFileHistoryResponse
		if response.StatusCode != http.StatusOK || json.Unmarshal(response.Body, &result) != nil {
			t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
		}
		return result
	}
	type period struct {
		start, end int // end -1 while the period runs
		reason     billing.QuotaPeriodEnd
		tokens     int64
		plans      string
	}
	// Periods end on schedule or at a counter reset; a response that leaves a window out says nothing about it.
	want := map[string][]period{
		"5-hour limit": {{237, -1, "", 6620, "Plus"}, {192, 197, billing.QuotaPeriodScheduled, 3200, "Plus"},
			{95, 100, billing.QuotaPeriodScheduled, 2400, "Plus"}, {48, 53, billing.QuotaPeriodScheduled, 400, "Plus"},
			{0, 5, billing.QuotaPeriodScheduled, 300, "Plus"}},
		"Weekly limit": {{96, -1, "", 10620, "Pro 100,Plus"}, {-1, 96, billing.QuotaPeriodEarlyReset, 2300, "Plus"}},
	}
	result := history(viewAccess{})
	if result.Usage.Requests != 11 || result.Usage.InputTokens != 21920 || len(result.Windows) != 2 ||
		result.Windows[0].Label != "5-hour limit" || result.Windows[1].Label != "Weekly limit" {
		t.Fatalf("history: %+v", result)
	}
	for _, window := range result.Windows {
		if len(window.Periods) != len(want[window.Label]) {
			t.Fatalf("%s periods: %+v", window.Label, window.Periods)
		}
		for i, expected := range want[window.Label] {
			got, end := window.Periods[i], time.Time{}
			if expected.end >= 0 {
				end = hour(expected.end)
			}
			if !got.Start.Equal(hour(expected.start)) || !got.End.Equal(end) || got.EndReason != expected.reason ||
				got.Usage.Tokens != expected.tokens || strings.Join(got.Plans, ",") != expected.plans {
				t.Fatalf("%s period %d: %+v, want %+v", window.Label, i, got, expected)
			}
		}
	}
	files, err := app.listAuthFiles(viewAccess{}, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// The newest report is an image request's, yet the main limit keeps both windows and nothing else.
	var labels []string
	for _, row := range files[0].Quota.Quota {
		labels = append(labels, row.Label)
	}
	usage := quotaUsageByLabel(t, files[0].Quota)
	if strings.Join(labels, ",") != "5-hour limit,Weekly limit" || !files[0].Quota.Quota[0].ObservedAt.Equal(hour(239)) ||
		usage["5-hour limit"].Tokens != 6620 || usage["Weekly limit"].Tokens != 10620 {
		t.Fatalf("current windows differ from the history: %+v", files[0].Quota.Quota)
	}
	scoped := history(viewAccess{APIKey: true, Tracked: true, Scope: billing.CallerScope("sk-dummy-one")})
	if scoped.Usage.Requests != 5 || scoped.Windows[1].Periods[0].Usage.Tokens != 800 {
		t.Fatalf("scoped history: %+v", scoped)
	}
}

func TestQuotaHistoryListsPeriodsThatBeganBeforeTheRangeWhole(t *testing.T) {
	app, _ := newAppWithPriceAndState(t, true)
	now := app.store.Now().Truncate(time.Second)
	hour := func(value int) time.Time { return now.Add(time.Duration(value) * time.Hour) }
	unix := func(value int) string { return strconv.FormatInt(hour(value).Unix(), 10) }
	spark := func(used string) http.Header {
		return http.Header{"X-Codex-Bengalfox-Limit-Name": {"GPT-5.3-Codex-Spark"}, "X-Codex-Bengalfox-Primary-Used-Percent": {used},
			"X-Codex-Bengalfox-Primary-Window-Minutes": {"43200"}, "X-Codex-Bengalfox-Primary-Reset-At": {unix(-240)}}
	}
	for _, event := range []struct {
		at      int
		model   string
		headers http.Header
		tokens  int64
	}{
		// A weekly period opened 33 days ago and ended 26 days ago.
		{-790, "gpt-5.5", http.Header{"X-Codex-Secondary-Used-Percent": {"10"}, "X-Codex-Secondary-Window-Minutes": {"10080"},
			"X-Codex-Secondary-Reset-At": {unix(-624)}}, 100},
		{-700, "gpt-5.5", nil, 200},
		// A monthly period opened 40 days ago and was reset 38 days ago.
		{-936, "gpt-5.3-codex-spark", spark("80"), 1000},
		{-912, "gpt-5.3-codex-spark", spark("0"), 2000},
		{-480, "gpt-5.3-codex-spark", spark("30"), 4000},
	} {
		publishUsageRecord(t, app, UsageRecord{Provider: "codex", AuthType: "oauth", AuthIndex: "codex-1", APIKey: "sk-dummy",
			Model: event.model, RequestedAt: hour(event.at), ResponseHeaders: event.headers,
			Detail: UsageDetail{InputTokens: event.tokens, TotalTokens: event.tokens}})
	}
	history, err := app.readAuthFileHistory(quotaCredential{"codex-1", "codex"}, "")
	if err != nil {
		t.Fatal(err)
	}
	type period struct {
		label      string
		start, end int
		tokens     int64
	}
	want := []period{{"Weekly limit", -792, -624, 300}, {"GPT-5.3-Codex-Spark Monthly limit", -912, -240, 6000}}
	if len(history.Windows) != len(want) {
		t.Fatalf("windows: %+v", history.Windows)
	}
	for i, expected := range want {
		window := history.Windows[i]
		if window.Label != expected.label || len(window.Periods) != 1 || !window.Periods[0].Start.Equal(hour(expected.start)) ||
			!window.Periods[0].End.Equal(hour(expected.end)) || window.Periods[0].Usage.Tokens != expected.tokens {
			t.Fatalf("%s: %+v, want %+v", window.Label, window.Periods, expected)
		}
	}
}

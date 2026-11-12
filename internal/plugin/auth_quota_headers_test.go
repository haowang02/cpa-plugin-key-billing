package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func TestUsageQuotaFollowsLatestLimitWindowsAndScopedStatistics(t *testing.T) {
	app, path := newAppWithPriceAndState(t, true)
	at := app.store.Now().Add(-time.Hour).Truncate(time.Second)
	headers := http.Header{
		"x-codex-plan-type":            {"plus"},
		"x-codex-primary-used-percent": {"20"}, "x-codex-primary-window-minutes": {"300"},
		"x-codex-primary-reset-after-seconds": {"1800"},
		"x-codex-secondary-used-percent":      {"40"}, "x-codex-secondary-window-minutes": {"10080"},
		"x-codex-secondary-reset-after-seconds": {"86400"},
		"Set-Cookie":                            {"dummy-private-cookie"}, "Authorization": {"Bearer dummy-private-token"},
	}
	record := UsageRecord{Provider: "codex", AuthType: "oauth", AuthIndex: "credential-1",
		APIKey: "sk-dummy-one", Model: "gpt-5.5", RequestedAt: at, ResponseHeaders: headers,
		Detail: UsageDetail{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 50, TotalTokens: 120}}
	publishUsageRecord(t, app, record)
	record.RequestedAt = at.Add(31 * time.Minute)
	record.APIKey = "sk-dummy-two"
	// After an upgrade to Pro the main limit has only a weekly window, so the
	// 5-hour window, reset meanwhile, is gone.
	record.ResponseHeaders = http.Header{
		"X-Codex-Plan-Type":            {"prolite"},
		"X-Codex-Primary-Used-Percent": {"35"}, "X-Codex-Primary-Window-Minutes": {"10080"},
		"X-Codex-Primary-Reset-After-Seconds": {"1740"},
	}
	record.Failed = true
	publishUsageRecord(t, app, record)
	record.RequestedAt = at.Add(-time.Minute)
	record.ResponseHeaders = headers
	publishUsageRecord(t, app, record)
	record.RequestedAt = at.Add(2 * time.Minute)
	record.ResponseHeaders = nil
	publishUsageRecord(t, app, record)
	record.RequestedAt = at.Add(-45 * 24 * time.Hour)
	publishUsageRecord(t, app, record)
	app.Shutdown()
	app = newTestApp(t)
	defer app.Shutdown()
	_, err := app.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{
		ConfigYAML: []byte("enabled: true\nstate_file: " + path + "\n"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
		if method != hostAuthList {
			t.Fatalf("unexpected host query: %s", method)
		}
		return json.RawMessage(`{"files":[{"auth_index":"credential-1","name":"dummy.json","type":"codex"}]}`), nil
	})
	files, err := app.listAuthFiles(viewAccess{}, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	quota := files[0].Quota
	if files[0].Usage.Requests != 4 || files[0].Usage.Failed != 3 ||
		files[0].Usage.InputTokens != 400 || files[0].Usage.CacheReadTokens != 200 || files[0].Usage.OutputTokens != 80 ||
		quota == nil || len(quota.Quota) != 1 || quota.Plan != "Pro 100" {
		t.Fatalf("credential view: %+v, quota: %+v", files[0], quota)
	}
	if row := quota.Quota[0]; row.Label != "Weekly limit" || *row.RemainingPercent != 65 || !row.ObservedAt.Equal(at.Add(31*time.Minute)) ||
		row.ResetAt != at.Add(60*time.Minute).UTC().Format(time.RFC3339) {
		t.Fatalf("dropped or late quota report changed windows: %+v", quota.Quota)
	}
	files, err = app.listAuthFiles(viewAccess{APIKey: true, Scope: billing.CallerScope("sk-dummy-one")}, time.Time{}, time.Time{})
	if err != nil || files[0].Usage.Requests != 1 || files[0].Usage.Failed != 0 || files[0].Quota == nil {
		t.Fatalf("scoped view: %+v, err: %v", files, err)
	}
	app.Shutdown()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"dummy-private-cookie", "dummy-private-token", "sk-dummy-one", "sk-dummy-two"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("persisted %s", secret)
		}
	}
}

func TestQuotaHeadersAcceptOnlyProviderQuotaFields(t *testing.T) {
	at := time.Now().UTC()
	claude := UsageRecord{Provider: "claude", AuthType: "oauth", AuthIndex: "dummy", RequestedAt: at,
		ResponseHeaders: http.Header{
			"anthropic-ratelimit-unified-5h-utilization": {"0.23"},
			"anthropic-ratelimit-unified-5h-reset":       {"1800000000"},
			"anthropic-ratelimit-unified-7d-utilization": {"NaN"},
			"x-ratelimit-remaining-tokens":               {"1000"},
		}}
	report := usageQuotaReport(claude, at)
	if report == nil || len(report.Windows) != 1 || report.Windows[0].ResetAt.Unix() != 1800000000 {
		t.Fatalf("Claude report: %+v", report)
	}
	if row := reportedRow("claude", report.Windows[0], at); *row.RemainingPercent != 77 {
		t.Fatalf("Claude quota: %+v", row)
	}
	claude.AuthType = "apikey"
	if usageQuotaReport(claude, at) != nil {
		t.Fatal("accepted API key credential")
	}
	report = quotaHeaderReport("codex", http.Header{
		"X-Codex-Active-Limit":         {"codex_spark"},
		"X-Codex-Primary-Used-Percent": {"90"}, "X-Codex-Primary-Window-Minutes": {"300"},
		"X-Codex-Spark-Limit-Name":           {"Spark"},
		"X-Codex-Spark-Primary-Used-Percent": {"20"}, "X-Codex-Spark-Primary-Window-Minutes": {"300"},
	}, at)
	if len(report.Windows) != 1 || reportedRow("codex", report.Windows[0], at).LabelPrefix != "Spark " {
		t.Fatalf("additional quota presented as main quota: %+v", report)
	}
}

func TestCurrentQuotaCombinesQueryWithLaterReports(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second)
	resetAt := func(after time.Duration) string { return at.Add(after).Format(time.RFC3339) }
	early := quotaHeaderReport("codex", http.Header{
		"X-Codex-Additional-GPT-5.3-Codex-Spark-Limit-Name":             {"GPT-5.3-Codex-Spark"},
		"X-Codex-Additional-GPT-5.3-Codex-Spark-Primary-Used-Percent":   {"20"},
		"X-Codex-Additional-GPT-5.3-Codex-Spark-Primary-Window-Minutes": {"300"},
		"X-Codex-Credits-Balance":                                       {"126.4"}, "X-Codex-Credits-Unlimited": {"true"},
	}, at)
	late := quotaHeaderReport("codex", http.Header{
		"X-Codex-Plan-Type": {"pro"}, "X-Codex-Primary-Used-Percent": {"50"}, "X-Codex-Primary-Window-Minutes": {"300"},
		"X-Codex-Primary-Reset-After-Seconds": {"14400"},
	}, at)
	resets := 2
	query := authFileQuota{FetchedAt: at.Add(time.Minute), Plan: "Plus", RateLimitResetCreditsAvailableCount: &resets, Quota: []quotaRow{
		quotaWindow{Seconds: 18000, RemainingPercent: remainingPercent(70), ResetAt: resetAt(4 * time.Hour), Meter: codexMainLimit.meter()}.row("codex", ""),
		quotaWindow{Seconds: 604800, RemainingPercent: remainingPercent(90), ResetAt: resetAt(72 * time.Hour), Meter: codexMainLimit.meter()}.row("codex", ""),
	}}
	quota := currentQuota("codex", &query, []billing.QuotaObservation{{At: at, QuotaReport: *early}, {At: at.Add(2 * time.Minute), QuotaReport: *late}})
	// The query replaces the windows reported before it and keeps the values it could not read;
	// a later report updates its own window in place.
	var rows []string
	for _, row := range quota.Quota {
		rows = append(rows, fmt.Sprint(row.Label, " ", *row.RemainingPercent, "@", row.ObservedAt.Sub(at)))
	}
	if strings.Join(rows, ", ") != "5-hour limit 50@2m0s, Weekly limit 90@1m0s" || quota.Plan != "Pro 200" ||
		quota.CreditBalance != "126.4" || quota.CreditsUnlimited || *quota.RateLimitResetCreditsAvailableCount != 2 {
		t.Fatalf("quota: %+v, windows: %v", quota, rows)
	}
}

func TestCodexQuotaHeadersAttributeWindowsByTheirLimit(t *testing.T) {
	at := time.Now().UTC()
	window := func(prefix, minutes string) http.Header {
		return http.Header{prefix + "Used-Percent": {"20"}, prefix + "Window-Minutes": {minutes}, prefix + "Reset-At": {"1800000000"}}
	}
	main := func(active, minutes string) http.Header {
		headers := window("X-Codex-Primary-", minutes)
		if active != "" {
			headers.Set("X-Codex-Active-Limit", active)
		}
		return headers
	}
	additional := func(name string) http.Header {
		headers := window("X-Codex-Feature-Primary-", "300")
		headers.Set("X-Codex-Feature-Limit-Name", name)
		return headers
	}
	for _, test := range []struct {
		name    string
		headers http.Header
		want    string // labels, with * marking metered windows
	}{
		{"main limit", main("premium", "300"), "5-hour limit*"},
		{"response before limit names", main("", "10080"), "Weekly limit*"},
		{"image pool before limit names", main("", "1440"), ""},
		{"image pool", main("images", "300"), ""},
		{"main limit beyond its windows", main("premium", "1440"), ""},
		{"Spark", additional("GPT-5.3-Codex-Spark"), "GPT-5.3-Codex-Spark 5-hour limit*"},
		{"additional limit of unknown models", additional("Fancy Feature"), "Fancy Feature 5-hour limit"},
	} {
		var labels []string
		if report := quotaHeaderReport("codex", test.headers, at); report != nil {
			for _, window := range report.Windows {
				row := reportedRow("codex", window, at)
				if row.Meter != nil {
					row.Label += "*"
				}
				labels = append(labels, row.Label)
			}
		}
		if strings.Join(labels, ",") != test.want {
			t.Errorf("%s: windows %v", test.name, labels)
		}
	}
}

package sqlite

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func TestQuotaReportsReadNewestValuesAndWindowHistories(t *testing.T) {
	d := openTestDB(t)
	unlimited, limited := true, false
	reports := []billing.QuotaReport{
		{Provider: "codex", Plan: "plus", CreditBalance: "12", CreditsUnlimited: &unlimited,
			Windows: []billing.QuotaReportWindow{{LimitKey: "codex", Seconds: 18000, UsedPercent: 10}, {LimitKey: "codex", Seconds: 604800, UsedPercent: 30}}},
		{Provider: "claude", Windows: []billing.QuotaReportWindow{{LimitKey: "unified", Seconds: 18000, UsedPercent: 9}}},
		{Provider: "codex", Plan: "pro", Windows: []billing.QuotaReportWindow{{LimitKey: "codex", Seconds: 18000, UsedPercent: 20}}},
		{Provider: "codex", CreditsUnlimited: &limited},
	}
	events := make([]billing.RequestEvent, len(reports))
	for i := range reports {
		events[i] = requestEvent("dummy", eventStart.Add(time.Duration(i)*time.Minute))
		events[i].Quota = &reports[i]
	}
	mustSave(t, d, billing.NewState(), billing.Changes{NormalRequestEvents: events})

	latest := func(since time.Time) string {
		view, err := d.LatestQuotaReports([]string{"auth-codex"}, since)
		if err != nil {
			t.Fatal(err)
		}
		var parts []string
		for _, report := range view["auth-codex"] {
			fields := []string{strconv.Itoa(int(report.At.Sub(eventStart) / time.Minute))}
			if report.Plan != "" {
				fields = append(fields, "plan="+report.Plan)
			}
			if report.CreditBalance != "" {
				fields = append(fields, "balance="+report.CreditBalance)
			}
			if report.CreditsUnlimited != nil {
				fields = append(fields, "unlimited="+strconv.FormatBool(*report.CreditsUnlimited))
			}
			for _, window := range report.Windows {
				fields = append(fields, fmt.Sprint(window.Seconds, ":", window.UsedPercent))
			}
			parts = append(parts, strings.Join(fields, " "))
		}
		return strings.Join(parts, " | ")
	}
	// An older report keeps only what no later one reported again.
	if got := latest(eventStart); got != "0 balance=12 604800:30 | 1 18000:9 | 2 plan=pro 18000:20 | 3 unlimited=false" {
		t.Fatalf("newest reports: %s", got)
	}
	if got := latest(eventStart.Add(90 * time.Second)); got != "2 plan=pro 18000:20 | 3 unlimited=false" {
		t.Fatalf("newest reports since: %s", got)
	}

	histories, err := d.QuotaWindowHistories("auth-codex", "codex", eventStart, eventStart.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, history := range histories {
		for _, report := range history.Reports {
			got = append(got, fmt.Sprint(history.Seconds, "@", int(report.At.Sub(eventStart)/time.Minute), ":", report.Plan, ":", report.UsedPercent))
		}
	}
	if strings.Join(got, " ") != "18000@0:plus:10 18000@2:pro:20 604800@0:plus:30" {
		t.Fatalf("window histories: %v", got)
	}

	// A credential keeps only its latest query, and saving drops every query made before since.
	for i, save := range []struct {
		authIndex, quota string
		since            time.Time
	}{{"auth-codex", "first", eventStart}, {"auth-claude", "old", eventStart}, {"auth-codex", "second", eventStart.Add(2 * time.Minute)}} {
		query := billing.QuotaQuery{Provider: "codex", At: eventStart.Add(time.Duration(i) * time.Minute), Quota: []byte(save.quota)}
		if err := d.SaveQuotaQuery(save.authIndex, query, save.since); err != nil {
			t.Fatal(err)
		}
	}
	queries, err := d.QuotaQueries([]string{"auth-codex", "auth-claude"}, eventStart)
	if err != nil || len(queries) != 1 || string(queries["auth-codex"].Quota) != "second" || !queries["auth-codex"].At.Equal(eventStart.Add(2*time.Minute)) {
		t.Fatalf("quota queries: %+v, err: %v", queries, err)
	}
}

func TestV19QuotaHeadersMigrateToReports(t *testing.T) {
	for _, reader := range []string{"present", "missing"} {
		t.Run(reader, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "v19.db")
			database := openDatabase(t, path)
			mustSave(t, database, billing.NewState(), billing.Changes{NormalRequestEvents: []billing.RequestEvent{
				requestEvent("dummy", eventStart), requestEvent("dummy", eventStart.Add(time.Minute)), requestEvent("dummy", eventStart.Add(2*time.Minute)),
			}})
			downgradeToV19(t, database)
			if _, err := database.db.Exec(`
				UPDATE request_events SET response_headers_json = '{"X-Used":["40"]}' WHERE id = 1;
				UPDATE request_events SET response_headers_json = '{"X-Used":"25"}' WHERE id = 2;
				UPDATE request_events SET response_headers_json = '{"X-Other":["1"]}' WHERE id = 3;`); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			var read StoredQuotaHeaders
			if reader == "present" {
				read = func(provider string, headers http.Header, at time.Time) *billing.QuotaReport {
					used, err := strconv.ParseFloat(headers.Get("X-Used"), 64)
					if err != nil {
						return nil
					}
					return &billing.QuotaReport{Provider: provider, Windows: []billing.QuotaReportWindow{{LimitKey: "codex", Seconds: 18000, UsedPercent: used}}}
				}
			}
			migrated, err := Open(path, read)
			if reader == "missing" {
				if err == nil {
					migrated.Close()
					t.Fatal("dropped stored headers without reading them")
				}
				raw, err := sql.Open("sqlite3", path)
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				var version, stored int
				if err := raw.QueryRow(`SELECT (SELECT user_version FROM pragma_user_version),
					(SELECT count(*) FROM request_events WHERE response_headers_json != '{}')`).Scan(&version, &stored); err != nil || version != 19 || stored != 3 {
					t.Fatal("failed migration changed the database", version, stored, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer migrated.Close()
			histories, err := migrated.QuotaWindowHistories("auth-codex", "codex", eventStart, eventStart.Add(time.Hour))
			if err != nil || len(histories) != 1 || fmt.Sprint(histories[0].Reports[0].UsedPercent, histories[0].Reports[1].UsedPercent, len(histories[0].Reports)) != "40 25 2" {
				t.Fatalf("migrated reports: %+v, err: %v", histories, err)
			}
			if view := mustQueryRequestEvents(t, migrated, billing.RequestEventQuery{}); view.Total != 3 {
				t.Fatalf("migration lost request events: %+v", view)
			}
		})
	}
}

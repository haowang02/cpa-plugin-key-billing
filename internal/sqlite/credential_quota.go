package sqlite

import (
	"cmp"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"time"

	"cpa-key-billing/internal/billing"
)

func insertQuotaReport(tx *sql.Tx, eventID int64, authIndex string, at time.Time, report billing.QuotaReport) error {
	var unlimited any
	if report.CreditsUnlimited != nil {
		unlimited = *report.CreditsUnlimited
	}
	if _, err := tx.Exec(`INSERT INTO quota_reports (request_event_id, auth_index, provider, at, plan, credit_balance, credits_unlimited)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, eventID, authIndex, report.Provider, nanos(at), report.Plan, report.CreditBalance, unlimited); err != nil {
		return fmt.Errorf("Write quota report: %w", err)
	}
	for _, window := range report.Windows {
		if _, err := tx.Exec(`INSERT INTO quota_report_windows (request_event_id, limit_key, seconds, auth_index, provider, at, plan, used_percent, reset_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, eventID, window.LimitKey, window.Seconds, authIndex, report.Provider, nanos(at), report.Plan,
			window.UsedPercent, nanos(window.ResetAt)); err != nil {
			return fmt.Errorf("Write quota report: %w", err)
		}
	}
	return nil
}

// The newest report of each window and value; max() gives the other columns
// of the row it selects.
const latestQuotaReportsSQL = `
	SELECT 'window', auth_index, provider, request_event_id, max(at), limit_key, seconds, used_percent, reset_at, NULL
	FROM quota_report_windows WHERE %[1]s GROUP BY auth_index, provider, limit_key, seconds
	UNION ALL
	SELECT 'plan', auth_index, provider, request_event_id, max(at), '', 0, 0, 0, plan
	FROM quota_reports WHERE %[1]s AND plan != '' GROUP BY auth_index, provider
	UNION ALL
	SELECT 'balance', auth_index, provider, request_event_id, max(at), '', 0, 0, 0, credit_balance
	FROM quota_reports WHERE %[1]s AND credit_balance != '' GROUP BY auth_index, provider
	UNION ALL
	SELECT 'unlimited', auth_index, provider, request_event_id, max(at), '', 0, 0, 0, credits_unlimited
	FROM quota_reports WHERE %[1]s AND credits_unlimited IS NOT NULL GROUP BY auth_index, provider`

func (d *DB) LatestQuotaReports(authIndexes []string, since time.Time) (map[string][]billing.QuotaObservation, error) {
	filter, args := authIndexFilter(authIndexes)
	filter += " AND at >= ?"
	args = append(args, nanos(since))
	rows, err := d.db.Query(fmt.Sprintf(latestQuotaReportsSQL, filter), slices.Concat(args, args, args, args)...)
	if err != nil {
		return nil, fmt.Errorf("Read credential quota: %w", err)
	}
	defer rows.Close()
	type request struct {
		authIndex string
		id        int64
	}
	reports := make(map[request]*billing.QuotaObservation)
	for rows.Next() {
		var kind, provider string
		var key request
		var at, resetAt int64
		var window billing.QuotaReportWindow
		var value sql.NullString
		if err := rows.Scan(&kind, &key.authIndex, &provider, &key.id, &at,
			&window.LimitKey, &window.Seconds, &window.UsedPercent, &resetAt, &value); err != nil {
			return nil, fmt.Errorf("Read credential quota: %w", err)
		}
		report := reports[key]
		if report == nil {
			report = &billing.QuotaObservation{At: timeAt(at), QuotaReport: billing.QuotaReport{Provider: provider}}
			reports[key] = report
		}
		switch kind {
		case "window":
			window.ResetAt = timeAt(resetAt)
			report.Windows = append(report.Windows, window)
		case "plan":
			report.Plan = value.String
		case "balance":
			report.CreditBalance = value.String
		case "unlimited":
			unlimited := value.String == "1"
			report.CreditsUnlimited = &unlimited
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("Read credential quota: %w", err)
	}
	keys := slices.SortedFunc(maps.Keys(reports), func(left, right request) int {
		return cmp.Or(reports[left].At.Compare(reports[right].At), cmp.Compare(left.id, right.id))
	})
	result := make(map[string][]billing.QuotaObservation)
	for _, key := range keys {
		result[key.authIndex] = append(result[key.authIndex], *reports[key])
	}
	return result, nil
}

func (d *DB) QuotaWindowHistories(authIndex, provider string, from, to time.Time) ([]billing.QuotaWindowHistory, error) {
	rows, err := d.db.Query(`SELECT limit_key, seconds, at, used_percent, reset_at, plan FROM quota_report_windows
		WHERE auth_index = ? AND provider = ? AND at >= ? AND at < ? ORDER BY limit_key, seconds, at, request_event_id`,
		authIndex, provider, nanos(from), nanos(to))
	if err != nil {
		return nil, fmt.Errorf("Read quota history: %w", err)
	}
	defer rows.Close()
	var histories []billing.QuotaWindowHistory
	for rows.Next() {
		var window billing.QuotaWindowHistory
		var report billing.QuotaWindowReport
		var at, resetAt int64
		if err := rows.Scan(&window.LimitKey, &window.Seconds, &at, &report.UsedPercent, &resetAt, &report.Plan); err != nil {
			return nil, fmt.Errorf("Read quota history: %w", err)
		}
		report.At, report.ResetAt = timeAt(at), timeAt(resetAt)
		if last := len(histories) - 1; last < 0 || histories[last].LimitKey != window.LimitKey || histories[last].Seconds != window.Seconds {
			histories = append(histories, window)
		}
		histories[len(histories)-1].Reports = append(histories[len(histories)-1].Reports, report)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("Read quota history: %w", err)
	}
	return histories, nil
}

func (d *DB) SaveQuotaQuery(authIndex string, query billing.QuotaQuery, since time.Time) error {
	return d.transact(func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT OR REPLACE INTO quota_queries (auth_index, provider, at, quota_json) VALUES (?, ?, ?, ?)",
			authIndex, query.Provider, nanos(query.At), string(query.Quota)); err != nil {
			return fmt.Errorf("Save quota query: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM quota_queries WHERE at < ?", nanos(since)); err != nil {
			return fmt.Errorf("Clean up quota queries: %w", err)
		}
		return nil
	})
}

func (d *DB) QuotaQueries(authIndexes []string, since time.Time) (map[string]billing.QuotaQuery, error) {
	filter, args := authIndexFilter(authIndexes)
	rows, err := d.db.Query("SELECT auth_index, provider, at, quota_json FROM quota_queries WHERE "+filter+" AND at >= ?",
		append(args, nanos(since))...)
	if err != nil {
		return nil, fmt.Errorf("Read quota queries: %w", err)
	}
	defer rows.Close()
	queries := make(map[string]billing.QuotaQuery)
	for rows.Next() {
		var authIndex, quota string
		var at int64
		var query billing.QuotaQuery
		if err := rows.Scan(&authIndex, &query.Provider, &at, &quota); err != nil {
			return nil, fmt.Errorf("Read quota queries: %w", err)
		}
		query.At, query.Quota = timeAt(at), []byte(quota)
		queries[authIndex] = query
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("Read quota queries: %w", err)
	}
	return queries, nil
}

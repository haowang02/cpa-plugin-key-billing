package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

func appendLog(tx *sql.Tx, changes billing.Changes) error {
	for _, entry := range changes.Log {
		_, errInsert := tx.Exec(`
			INSERT INTO billing_log (
				at, scope, request_id, endpoint, auth_index, upstream_model, billing_model,
				outcome, accounting_quality, price_source, reasoning_tokens,
				total_usd, uncached_input_usd, cache_read_usd, cache_write_usd, output_usd,
				uncached_input_tokens, cache_read_tokens, cache_write_tokens, billed_output_tokens,
				tiered, long_context, threshold_input_tokens,
				applied_input_per_1m, applied_output_per_1m,
				applied_cache_read_per_1m, applied_cache_write_per_1m
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nanos(entry.At), entry.Scope, entry.RequestID, entry.Endpoint, entry.AuthIndex,
			entry.UpstreamModel, entry.BillingModel, string(entry.Outcome),
			string(entry.AccountingQuality), string(entry.PriceSource), entry.ReasoningTokens,
			entry.Cost.TotalUSD, entry.Cost.UncachedInputUSD, entry.Cost.CacheReadUSD,
			entry.Cost.CacheWriteUSD, entry.Cost.OutputUSD,
			entry.Cost.UncachedInputTokens, entry.Cost.CacheReadTokens,
			entry.Cost.CacheWriteTokens, entry.Cost.BilledOutputTokens,
			entry.Cost.Tiered, entry.Cost.LongContext, entry.Cost.ThresholdInputTokens,
			entry.Cost.AppliedInputPer1M, entry.Cost.AppliedOutputPer1M,
			entry.Cost.AppliedCacheReadPer1M, entry.Cost.AppliedCacheWritePer1M)
		if errInsert != nil {
			return fmt.Errorf("写入计费日志：%w", errInsert)
		}
	}
	return pruneLog(tx.Exec, changes.LogCutoff)
}

// Appending is the moment the log grows, and opening it is the moment a log
// that stopped growing is looked at again; between them nothing else can notice
// that an entry aged out. A zero cutoff prunes nothing.
func pruneLog(exec func(string, ...any) (sql.Result, error), cutoff time.Time) error {
	if cutoff.IsZero() {
		return nil
	}
	if _, errPrune := exec("DELETE FROM billing_log WHERE at < ?", nanos(cutoff)); errPrune != nil {
		return fmt.Errorf("清理过期计费日志：%w", errPrune)
	}
	return nil
}

// The display identity lives on the key and credential rows rather than on the
// entry, so a renamed key or a newly learned credential renames its history —
// which also means both are searchable and both are joined here.
const logSource = `
	FROM billing_log l
	LEFT JOIN api_keys k ON k.scope = l.scope
	LEFT JOIN credentials c ON c.auth_index = l.auth_index
	WHERE l.at >= ?`

// Every field the table can show is searchable, one at a time: a term is matched
// against a single column rather than against them joined together, so it can
// never straddle two of them. Folding is ulower() rather than SQLite's built-in
// lower(), which leaves everything outside ASCII alone — labels and remarks are
// free operator text and are not written in ASCII everywhere.
const logSearch = ` AND (
	instr(ulower(coalesce(k.label, '')), ulower(?)) > 0 OR
	instr(ulower(coalesce(k.preview, '')), ulower(?)) > 0 OR
	instr(ulower(l.scope), ulower(?)) > 0 OR
	instr(ulower(l.upstream_model), ulower(?)) > 0 OR
	instr(ulower(l.billing_model), ulower(?)) > 0 OR
	instr(ulower(l.endpoint), ulower(?)) > 0 OR
	instr(ulower(coalesce(c.name, '')), ulower(?)) > 0 OR
	instr(ulower(l.request_id), ulower(?)) > 0)`

// Logs answers one page. The counts are taken before the status filter applies,
// so choosing one status does not collapse the others to zero.
func (d *DB) Logs(query billing.LogQuery, since time.Time) (billing.LogView, error) {
	view := billing.LogView{Entries: []billing.LogRow{}}
	where, args := logFilter(query, since)

	// The buckets bind the stored outcomes rather than restating them, so that
	// renaming one cannot leave a bucket counting nothing.
	succeededOutcome, _ := billing.StoredOutcome(billing.OutcomeSucceeded)
	countArgs := append([]any{succeededOutcome, string(billing.OutcomeFailed), string(billing.OutcomeCanceled)}, args...)
	counts := d.db.QueryRow(`
		SELECT count(*),
			sum(CASE WHEN l.outcome = ? THEN 1 ELSE 0 END),
			sum(CASE WHEN l.outcome = ? THEN 1 ELSE 0 END),
			sum(CASE WHEN l.outcome = ? THEN 1 ELSE 0 END)`+where, countArgs...)
	var succeeded, failed, canceled sql.NullInt64
	if errCount := counts.Scan(&view.Outcomes.All, &succeeded, &failed, &canceled); errCount != nil {
		return billing.LogView{}, fmt.Errorf("统计计费日志：%w", errCount)
	}
	view.Outcomes.Succeeded = int(succeeded.Int64)
	view.Outcomes.Failed = int(failed.Int64)
	view.Outcomes.Canceled = int(canceled.Int64)
	view.Total = view.Outcomes.Total(query.Outcome)

	// The page and the total it belongs to are two readings of the same filter,
	// so both take it from billing rather than each deciding what it means.
	page := where
	if stored, filtered := billing.StoredOutcome(query.Outcome); filtered {
		page += " AND l.outcome = ?"
		args = append(args, stored)
	}
	// Entries are read back in the order they were committed rather than by
	// their timestamp: a request is stamped when it started, so completion order
	// is what "newest first" means to someone watching the log.
	limit := query.Limit
	if limit <= 0 {
		limit = -1
	}
	page += " ORDER BY l.id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, query.Offset)

	rows, errQuery := d.db.Query(`
		SELECT l.at, l.scope, l.request_id, l.endpoint, l.auth_index, l.upstream_model, l.billing_model,
			l.outcome, l.accounting_quality, l.price_source, l.reasoning_tokens,
			l.total_usd, l.uncached_input_usd, l.cache_read_usd, l.cache_write_usd, l.output_usd,
			l.uncached_input_tokens, l.cache_read_tokens, l.cache_write_tokens, l.billed_output_tokens,
			l.tiered, l.long_context, l.threshold_input_tokens,
			l.applied_input_per_1m, l.applied_output_per_1m,
			l.applied_cache_read_per_1m, l.applied_cache_write_per_1m,
			coalesce(k.preview, ''), coalesce(k.label, ''), coalesce(c.name, '')`+page, args...)
	if errQuery != nil {
		return billing.LogView{}, fmt.Errorf("读取计费日志：%w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		row, errScan := scanLogRow(rows)
		if errScan != nil {
			return billing.LogView{}, errScan
		}
		view.Entries = append(view.Entries, row)
	}
	if errRows := rows.Err(); errRows != nil {
		return billing.LogView{}, fmt.Errorf("读取计费日志：%w", errRows)
	}
	return view, nil
}

func logFilter(query billing.LogQuery, since time.Time) (string, []any) {
	where := logSource
	args := []any{nanos(since)}
	search := strings.TrimSpace(query.Search)
	if search == "" {
		return where, args
	}
	where += logSearch
	for range strings.Count(logSearch, "?") {
		args = append(args, search)
	}
	return where, args
}

func scanLogRow(rows *sql.Rows) (billing.LogRow, error) {
	var (
		row                           billing.LogRow
		at                            int64
		outcome, quality, priceSource string
	)
	if errScan := rows.Scan(&at, &row.Scope, &row.RequestID, &row.Endpoint, &row.AuthIndex,
		&row.UpstreamModel, &row.BillingModel, &outcome, &quality, &priceSource, &row.ReasoningTokens,
		&row.Cost.TotalUSD, &row.Cost.UncachedInputUSD, &row.Cost.CacheReadUSD,
		&row.Cost.CacheWriteUSD, &row.Cost.OutputUSD,
		&row.Cost.UncachedInputTokens, &row.Cost.CacheReadTokens,
		&row.Cost.CacheWriteTokens, &row.Cost.BilledOutputTokens,
		&row.Cost.Tiered, &row.Cost.LongContext, &row.Cost.ThresholdInputTokens,
		&row.Cost.AppliedInputPer1M, &row.Cost.AppliedOutputPer1M,
		&row.Cost.AppliedCacheReadPer1M, &row.Cost.AppliedCacheWritePer1M,
		&row.Preview, &row.Label, &row.Source); errScan != nil {
		return billing.LogRow{}, fmt.Errorf("读取计费日志：%w", errScan)
	}
	row.At = timeAt(at)
	row.Outcome = billing.RequestOutcome(outcome)
	row.AccountingQuality = billing.TokenAccountingQuality(quality)
	row.PriceSource = billing.PriceSource(priceSource)
	return row, nil
}

func (d *DB) ClearLogs() (int, error) {
	result, errDelete := d.db.Exec("DELETE FROM billing_log")
	if errDelete != nil {
		return 0, fmt.Errorf("清空计费日志：%w", errDelete)
	}
	cleared, errCount := result.RowsAffected()
	if errCount != nil {
		return 0, fmt.Errorf("清空计费日志：%w", errCount)
	}
	return int(cleared), nil
}

func (d *DB) LoggedScopes(since time.Time) (map[string]struct{}, error) {
	rows, errQuery := d.db.Query("SELECT DISTINCT scope FROM billing_log WHERE at >= ?", nanos(since))
	if errQuery != nil {
		return nil, fmt.Errorf("读取计费日志涉及的 API Key：%w", errQuery)
	}
	defer rows.Close()
	scopes := make(map[string]struct{})
	for rows.Next() {
		var scope string
		if errScan := rows.Scan(&scope); errScan != nil {
			return nil, fmt.Errorf("读取计费日志涉及的 API Key：%w", errScan)
		}
		scopes[scope] = struct{}{}
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("读取计费日志涉及的 API Key：%w", errRows)
	}
	return scopes, nil
}

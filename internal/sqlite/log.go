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
			INSERT INTO usage_log (
				at, scope, auth_index, executor_type, reasoning_effort, service_tier,
				upstream_model, billing_model, failed, latency_ms, ttft_ms,
				accounting_quality, price_source, reasoning_tokens,
				total_usd, uncached_input_usd, cache_read_usd, cache_write_usd, output_usd,
				uncached_input_tokens, cache_read_tokens, cache_write_tokens, billed_output_tokens,
				tiered, long_context, threshold_input_tokens,
				applied_input_per_1m, applied_output_per_1m,
				applied_cache_read_per_1m, applied_cache_write_per_1m
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nanos(entry.At), entry.Scope, entry.AuthIndex, entry.ExecutorType, entry.ReasoningEffort, entry.ServiceTier,
			entry.UpstreamModel, entry.BillingModel, entry.Failed,
			entry.LatencyMS, entry.TTFTMS,
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

func pruneLog(exec execer, cutoff time.Time) error {
	if cutoff.IsZero() {
		return nil
	}
	if _, errPrune := exec("DELETE FROM usage_log WHERE at < ?", nanos(cutoff)); errPrune != nil {
		return fmt.Errorf("清理计费日志：%w", errPrune)
	}
	return nil
}

const logSource = `
	FROM usage_log l
	LEFT JOIN api_keys k ON k.scope = l.scope
	LEFT JOIN credentials c ON c.auth_index = l.auth_index
	WHERE l.at >= ?`

const logSearch = ` AND (
	instr(ulower(coalesce(k.label, '')), ulower(?)) > 0 OR
	instr(ulower(coalesce(k.preview, '')), ulower(?)) > 0 OR
	instr(ulower(l.scope), ulower(?)) > 0 OR
	instr(ulower(l.executor_type), ulower(?)) > 0 OR
	instr(ulower(l.reasoning_effort), ulower(?)) > 0 OR
	instr(ulower(l.service_tier), ulower(?)) > 0 OR
	instr(ulower(l.upstream_model), ulower(?)) > 0 OR
	instr(ulower(l.billing_model), ulower(?)) > 0 OR
	instr(ulower(coalesce(c.name, '')), ulower(?)) > 0)`

const visibleLogSearch = ` AND (
	instr(ulower(l.executor_type), ulower(?)) > 0 OR
	instr(ulower(l.reasoning_effort), ulower(?)) > 0 OR
	instr(ulower(l.service_tier), ulower(?)) > 0 OR
	instr(ulower(l.billing_model), ulower(?)) > 0 OR
	(instr(coalesce(c.provider, ''), '@') = 0 AND instr(ulower(coalesce(c.provider, '')), ulower(?)) > 0) OR
	instr(CAST(l.latency_ms AS TEXT), ?) > 0)`

func (d *DB) Logs(query billing.LogQuery, since time.Time) (billing.LogView, error) {
	view := billing.LogView{Entries: []billing.LogRow{}}
	where, args := logFilter(query, since)

	counts := d.db.QueryRow(`
		SELECT count(*),
			sum(CASE WHEN l.failed = 0 THEN 1 ELSE 0 END),
			sum(CASE WHEN l.failed != 0 THEN 1 ELSE 0 END)`+where, args...)
	var normal, failed sql.NullInt64
	if errCount := counts.Scan(&view.Statuses.All, &normal, &failed); errCount != nil {
		return billing.LogView{}, fmt.Errorf("统计计费日志：%w", errCount)
	}
	view.Statuses.Normal = int(normal.Int64)
	view.Statuses.Failed = int(failed.Int64)
	view.Total = view.Statuses.Total(query.Status)

	page := where
	switch query.Status {
	case billing.UsageStatusNormal:
		page += " AND l.failed = 0"
	case billing.UsageStatusFailed:
		page += " AND l.failed != 0"
	}
	limit := query.Limit
	if limit <= 0 {
		limit = -1
	}
	page += " ORDER BY l.id DESC LIMIT ? OFFSET ?"
	pageArgs := append(append([]any(nil), args...), limit, query.Offset)

	rows, errQuery := d.db.Query(`
		SELECT l.at, l.scope, l.auth_index, l.executor_type, l.reasoning_effort, l.service_tier,
			l.upstream_model, l.billing_model, l.failed, l.latency_ms, l.ttft_ms,
			l.accounting_quality, l.price_source, l.reasoning_tokens,
			l.total_usd, l.uncached_input_usd, l.cache_read_usd, l.cache_write_usd, l.output_usd,
			l.uncached_input_tokens, l.cache_read_tokens, l.cache_write_tokens, l.billed_output_tokens,
			l.tiered, l.long_context, l.threshold_input_tokens,
			l.applied_input_per_1m, l.applied_output_per_1m,
			l.applied_cache_read_per_1m, l.applied_cache_write_per_1m,
			coalesce(k.preview, ''), coalesce(k.label, ''), coalesce(c.name, ''), coalesce(c.provider, '')`+page, pageArgs...)
	if errQuery != nil {
		return billing.LogView{}, fmt.Errorf("读取计费日志：%w", errQuery)
	}
	defer func() { _ = rows.Close() }()
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
	if scope := strings.TrimSpace(query.Scope); scope != "" {
		where += " AND l.scope = ?"
		args = append(args, scope)
	}
	search := strings.TrimSpace(query.Search)
	if search == "" {
		return where, args
	}
	searchClause := logSearch
	if query.VisibleFieldsOnly {
		searchClause = visibleLogSearch
	}
	where += searchClause
	for range strings.Count(searchClause, "?") {
		args = append(args, search)
	}
	return where, args
}

func scanLogRow(rows *sql.Rows) (billing.LogRow, error) {
	var (
		row                  billing.LogRow
		at, failed           int64
		quality, priceSource string
	)
	if errScan := rows.Scan(&at, &row.Scope, &row.AuthIndex, &row.ExecutorType, &row.ReasoningEffort, &row.ServiceTier,
		&row.UpstreamModel, &row.BillingModel, &failed,
		&row.LatencyMS, &row.TTFTMS,
		&quality, &priceSource, &row.ReasoningTokens,
		&row.Cost.TotalUSD, &row.Cost.UncachedInputUSD, &row.Cost.CacheReadUSD,
		&row.Cost.CacheWriteUSD, &row.Cost.OutputUSD,
		&row.Cost.UncachedInputTokens, &row.Cost.CacheReadTokens,
		&row.Cost.CacheWriteTokens, &row.Cost.BilledOutputTokens,
		&row.Cost.Tiered, &row.Cost.LongContext, &row.Cost.ThresholdInputTokens,
		&row.Cost.AppliedInputPer1M, &row.Cost.AppliedOutputPer1M,
		&row.Cost.AppliedCacheReadPer1M, &row.Cost.AppliedCacheWritePer1M,
		&row.Preview, &row.Label, &row.Source, &row.Provider); errScan != nil {
		return billing.LogRow{}, fmt.Errorf("读取计费日志：%w", errScan)
	}
	row.At = timeAt(at)
	row.Failed = failed != 0
	row.AccountingQuality = billing.TokenAccountingQuality(quality)
	row.PriceSource = billing.PriceSource(priceSource)
	return row, nil
}

func (d *DB) ClearLogs() (int, error) {
	return d.clear("DELETE FROM usage_log", "计费日志")
}

func (d *DB) LoggedScopes(since time.Time) (map[string]struct{}, error) {
	rows, errQuery := d.db.Query("SELECT DISTINCT scope FROM usage_log WHERE at >= ?", nanos(since))
	if errQuery != nil {
		return nil, fmt.Errorf("读取计费日志涉及的 API Key：%w", errQuery)
	}
	defer func() { _ = rows.Close() }()
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

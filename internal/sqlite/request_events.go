package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

func appendRequestEvent(tx *sql.Tx, entry billing.RequestEvent) (int64, error) {
	result, errInsert := tx.Exec(`
		INSERT INTO request_events (
			at, scope, auth_index, provider, executor_type, reasoning_effort, service_tier,
			upstream_model, billing_model, failed, latency_ms, ttft_ms,
			accounting_quality, price_source, reasoning_tokens,
			total_usd, uncached_input_usd, cache_read_usd, cache_write_usd, output_usd,
			uncached_input_tokens, cache_read_tokens, cache_write_tokens, billed_output_tokens,
			tiered, long_context, threshold_input_tokens,
			applied_input_per_1m, applied_output_per_1m,
			applied_cache_read_per_1m, applied_cache_write_per_1m
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nanos(entry.At), entry.Scope, entry.AuthIndex, entry.Provider, entry.ExecutorType, entry.ReasoningEffort, entry.ServiceTier,
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
		return 0, fmt.Errorf("写入请求事件：%w", errInsert)
	}
	id, errID := result.LastInsertId()
	if errID != nil {
		return 0, fmt.Errorf("读取请求事件编号：%w", errID)
	}
	return id, nil
}

func (d *DB) requestEventCount() (int, error) {
	var count int
	if err := d.db.QueryRow("SELECT count(*) FROM request_events").Scan(&count); err != nil {
		return 0, fmt.Errorf("读取请求事件条数：%w", err)
	}
	return count, nil
}

func pruneRequestEvents(exec execer, cutoff time.Time) error {
	if cutoff.IsZero() {
		return nil
	}
	if _, errPrune := exec("DELETE FROM request_events WHERE at < ?", nanos(cutoff)); errPrune != nil {
		return fmt.Errorf("清理请求事件：%w", errPrune)
	}
	return nil
}

const requestEventSource = `
	FROM request_events r
	LEFT JOIN api_keys k ON k.scope = r.scope
	LEFT JOIN credentials c ON c.auth_index = r.auth_index
	WHERE r.at >= ?`

func (d *DB) RequestEvents(query billing.RequestEventQuery, since time.Time) (billing.RequestEventView, error) {
	view := billing.RequestEventView{Entries: []billing.RequestEventRow{}}
	where, args := requestEventFilter(query, since)
	if query.IncludeFilters {
		filters, errFilters := d.requestEventFilterValues(query, since)
		if errFilters != nil {
			return billing.RequestEventView{}, errFilters
		}
		view.Filters = filters
	}

	counts := d.db.QueryRow(`
		SELECT count(*),
			sum(CASE WHEN r.failed = 0 THEN 1 ELSE 0 END),
			sum(CASE WHEN r.failed != 0 THEN 1 ELSE 0 END)`+where, args...)
	var normal, failed sql.NullInt64
	if errCount := counts.Scan(&view.Statuses.All, &normal, &failed); errCount != nil {
		return billing.RequestEventView{}, fmt.Errorf("统计请求事件：%w", errCount)
	}
	view.Statuses.Normal = int(normal.Int64)
	view.Statuses.Failed = int(failed.Int64)
	view.Total = view.Statuses.All

	page := where
	if failed := query.Failed; failed != nil {
		if *failed {
			view.Total = view.Statuses.Failed
			page += " AND r.failed != 0"
		} else {
			view.Total = view.Statuses.Normal
			page += " AND r.failed = 0"
		}
	}
	limit := query.Limit
	if limit <= 0 {
		limit = -1
	}
	page += " ORDER BY r.at DESC, r.id DESC LIMIT ? OFFSET ?"
	pageArgs := append(append([]any(nil), args...), limit, query.Offset)

	rows, errQuery := d.db.Query(`
		SELECT r.at, r.scope, r.auth_index, coalesce(NULLIF(r.provider, ''), c.provider, ''),
			r.executor_type, r.reasoning_effort, r.service_tier,
			r.upstream_model, r.billing_model, r.failed, r.latency_ms, r.ttft_ms,
			r.accounting_quality, r.price_source, r.reasoning_tokens,
			r.total_usd, r.uncached_input_usd, r.cache_read_usd, r.cache_write_usd, r.output_usd,
			r.uncached_input_tokens, r.cache_read_tokens, r.cache_write_tokens, r.billed_output_tokens,
			r.tiered, r.long_context, r.threshold_input_tokens,
			r.applied_input_per_1m, r.applied_output_per_1m,
			r.applied_cache_read_per_1m, r.applied_cache_write_per_1m,
			coalesce(k.preview, ''), coalesce(k.label, ''), coalesce(c.name, '')`+page, pageArgs...)
	if errQuery != nil {
		return billing.RequestEventView{}, fmt.Errorf("读取请求事件：%w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		row, errScan := scanRequestEventRow(rows)
		if errScan != nil {
			return billing.RequestEventView{}, errScan
		}
		view.Entries = append(view.Entries, row)
	}
	if errRows := rows.Err(); errRows != nil {
		return billing.RequestEventView{}, fmt.Errorf("读取请求事件：%w", errRows)
	}
	return view, nil
}

func requestEventFilter(query billing.RequestEventQuery, since time.Time) (string, []any) {
	where, args := eventTimeFilter(requestEventSource, query.From, query.To, since)
	if scope := strings.TrimSpace(query.Scope); scope != "" {
		where += " AND r.scope = ?"
		args = append(args, scope)
	}
	if scope := strings.TrimSpace(query.KeyScope); scope != "" {
		where += " AND r.scope = ?"
		args = append(args, scope)
	}
	if model := strings.TrimSpace(query.Model); model != "" {
		where += " AND coalesce(NULLIF(r.billing_model, ''), r.upstream_model) = ?"
		args = append(args, model)
	}
	if source := strings.TrimSpace(query.Source); source != "" {
		where += " AND coalesce(c.name, '') = ?"
		args = append(args, source)
	}
	if executor := strings.TrimSpace(query.Executor); executor != "" {
		where += " AND r.executor_type = ?"
		args = append(args, executor)
	}
	if provider := strings.TrimSpace(query.Provider); provider != "" {
		where += " AND coalesce(NULLIF(r.provider, ''), c.provider, '') = ?"
		args = append(args, provider)
	}
	return where, args
}

func eventTimeFilter(source string, from, to, since time.Time) (string, []any) {
	if !from.IsZero() && from.After(since) {
		since = from
	}
	where := source
	args := []any{nanos(since)}
	if !to.IsZero() {
		where += " AND r.at < ?"
		args = append(args, nanos(to))
	}
	return where, args
}

func (d *DB) requestEventFilterValues(query billing.RequestEventQuery, since time.Time) (*billing.RequestEventFilterValues, error) {
	where, args := eventTimeFilter(requestEventSource, query.From, query.To, since)
	if scope := strings.TrimSpace(query.Scope); scope != "" {
		where += " AND r.scope = ?"
		args = append(args, scope)
	}
	rows, errQuery := d.db.Query(`
		WITH filtered AS (
			SELECT r.scope, coalesce(k.preview, '') AS preview, coalesce(k.label, '') AS label,
				coalesce(NULLIF(r.billing_model, ''), r.upstream_model) AS model,
				coalesce(c.name, '') AS source, r.executor_type AS executor,
				coalesce(NULLIF(r.provider, ''), c.provider, '') AS provider`+where+`
		)
		SELECT kind, value, preview, label FROM (
			SELECT 'key' AS kind, scope AS value, preview, label FROM filtered GROUP BY scope, preview, label
			UNION ALL
			SELECT 'model', model, '', '' FROM filtered WHERE model != '' GROUP BY model
			UNION ALL
			SELECT 'source', source, '', '' FROM filtered WHERE source != '' GROUP BY source
			UNION ALL SELECT 'executor', executor, '', '' FROM filtered WHERE executor != '' GROUP BY executor
			UNION ALL SELECT 'provider', provider, '', '' FROM filtered WHERE provider != '' GROUP BY provider
		) ORDER BY kind, CASE WHEN kind = 'key' AND label = '' THEN 1 ELSE 0 END,
			label COLLATE NOCASE, value COLLATE NOCASE`, args...)
	if errQuery != nil {
		return nil, fmt.Errorf("读取请求事件筛选项：%w", errQuery)
	}
	defer rows.Close()

	filters := &billing.RequestEventFilterValues{APIKeys: []billing.APIKeyFilterOption{}, Models: []string{}, Sources: []string{},
		Executors: []string{}, Providers: []string{}}
	for rows.Next() {
		var kind, value, preview, label string
		if errScan := rows.Scan(&kind, &value, &preview, &label); errScan != nil {
			return nil, fmt.Errorf("读取请求事件筛选项：%w", errScan)
		}
		switch kind {
		case "key":
			filters.APIKeys = append(filters.APIKeys, billing.APIKeyFilterOption{Scope: value, Preview: preview, Label: label})
		case "model":
			filters.Models = append(filters.Models, value)
		case "source":
			filters.Sources = append(filters.Sources, value)
		case "executor":
			filters.Executors = append(filters.Executors, value)
		case "provider":
			filters.Providers = append(filters.Providers, value)
		}
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("读取请求事件筛选项：%w", errRows)
	}
	return filters, nil
}

func scanRequestEventRow(rows *sql.Rows) (billing.RequestEventRow, error) {
	var (
		row                  billing.RequestEventRow
		at, failed           int64
		quality, priceSource string
	)
	if errScan := rows.Scan(&at, &row.Scope, &row.AuthIndex, &row.Provider,
		&row.ExecutorType, &row.ReasoningEffort, &row.ServiceTier,
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
		&row.Preview, &row.Label, &row.Source); errScan != nil {
		return billing.RequestEventRow{}, fmt.Errorf("读取请求事件：%w", errScan)
	}
	row.At = timeAt(at)
	row.Failed = failed != 0
	row.AccountingQuality = billing.TokenAccountingQuality(quality)
	row.PriceSource = billing.PriceSource(priceSource)
	return row, nil
}

func (d *DB) RequestEventScopes(since time.Time) (map[string]struct{}, error) {
	rows, errQuery := d.db.Query("SELECT DISTINCT scope FROM request_events WHERE at >= ?", nanos(since))
	if errQuery != nil {
		return nil, fmt.Errorf("读取请求事件涉及的 API Key：%w", errQuery)
	}
	defer rows.Close()
	scopes := make(map[string]struct{})
	for rows.Next() {
		var scope string
		if errScan := rows.Scan(&scope); errScan != nil {
			return nil, fmt.Errorf("读取请求事件涉及的 API Key：%w", errScan)
		}
		scopes[scope] = struct{}{}
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("读取请求事件涉及的 API Key：%w", errRows)
	}
	return scopes, nil
}

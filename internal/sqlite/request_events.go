package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

func appendRequestEvent(tx *sql.Tx, entry billing.RequestEvent) (int64, error) {
	execution, _ := json.Marshal(entry.Execution)
	// Only persistence encodes the multiplier into the price source.
	priceSource := string(entry.PriceSource)
	switch entry.PriceSource {
	case billing.PriceSourceCustom, billing.PriceSourceBuiltin, billing.PriceSourceReference:
		if entry.Cost.Multiplier == billing.CodexFastModeMultiplier {
			priceSource += ":x2.5"
		}
	}
	result, errInsert := tx.Exec(`
		INSERT INTO request_events (
			at, scope, auth_index, provider, account, executor_type, reasoning_effort, service_tier,
			response_service_tier, upstream_model, response_model, billing_model, failed, latency_ms, ttft_ms,
			accounting_quality, price_source, reasoning_tokens,
			currency, total_cost, uncached_input_cost, cache_read_cost, cache_write_cost, output_cost,
			uncached_input_tokens, cache_read_tokens, cache_write_tokens, billed_output_tokens,
			tiered, long_context, threshold_input_tokens,
			applied_input_per_1m, applied_output_per_1m,
			applied_cache_read_per_1m, applied_cache_write_per_1m,
			endpoint, transport, stream, execution_json, user_agent, x_forwarded_for
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nanos(entry.At), entry.Scope, entry.AuthIndex, entry.Provider, entry.Account, entry.ExecutorType, entry.ReasoningEffort, entry.ServiceTier,
		entry.ResponseServiceTier, entry.UpstreamModel, entry.ResponseModel, entry.BillingModel, entry.Failed,
		entry.LatencyMS, entry.TTFTMS,
		string(entry.AccountingQuality), priceSource, entry.ReasoningTokens,
		string(entry.Cost.Currency), entry.Cost.Total, entry.Cost.UncachedInput, entry.Cost.CacheRead,
		entry.Cost.CacheWrite, entry.Cost.Output,
		entry.Cost.UncachedInputTokens, entry.Cost.CacheReadTokens,
		entry.Cost.CacheWriteTokens, entry.Cost.BilledOutputTokens,
		entry.Cost.Tiered, entry.Cost.LongContext, entry.Cost.ThresholdInputTokens,
		entry.Cost.AppliedInputPer1M, entry.Cost.AppliedOutputPer1M,
		entry.Cost.AppliedCacheReadPer1M, entry.Cost.AppliedCacheWritePer1M,
		entry.Endpoint, entry.Transport, entry.Stream, string(execution), entry.UserAgent, entry.XForwardedFor)
	if errInsert != nil {
		return 0, fmt.Errorf("Write request event: %w", errInsert)
	}
	id, errID := result.LastInsertId()
	if errID != nil {
		return 0, fmt.Errorf("Read request event ID: %w", errID)
	}
	if entry.Quota != nil {
		if err := insertQuotaReport(tx, id, entry.AuthIndex, entry.At, *entry.Quota); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func (d *DB) requestEventCount() (int, error) {
	var count int
	if err := d.db.QueryRow("SELECT count(*) FROM request_events").Scan(&count); err != nil {
		return 0, fmt.Errorf("Read request event count: %w", err)
	}
	return count, nil
}

func pruneRequestEvents(exec execer, cutoff time.Time) error {
	if cutoff.IsZero() {
		return nil
	}
	if _, errPrune := exec("DELETE FROM request_events WHERE at < ?", nanos(cutoff)); errPrune != nil {
		return fmt.Errorf("Clean up request events: %w", errPrune)
	}
	return nil
}

// Model filters and their expression index must use the same expression.
const eventModelSQL = "coalesce(NULLIF(billing_model, ''), upstream_model)"

// A configured credential is named as configured; any other credential, or one
// since removed, by the provider that served it.
var requestEventSourceName = `coalesce((SELECT ` + sourceNameSQL("c.name") + `
	FROM config_credentials c WHERE r.auth_index != '' AND c.auth_index = r.auth_index AND c.name != ''),
	` + sourceNameSQL(`(CASE WHEN substr(r.provider, 1, 18) = 'openai-compatible-' THEN substr(r.provider, 19) ELSE r.provider END)`) + `)`

func sourceNameSQL(providerName string) string {
	return `CASE WHEN ` + providerName + ` = '' THEN r.account
		WHEN r.account = '' OR lower(r.account) = lower(` + providerName + `) THEN ` + providerName + `
		ELSE ` + providerName + ` || ' · ' || r.account END`
}

const requestEventSource = `
	FROM request_events r
	WHERE r.at >= ?`

func (d *DB) RequestEvents(query billing.RequestEventQuery, since time.Time) (billing.RequestEventView, error) {
	view := billing.RequestEventView{Entries: []billing.RequestEventRow{}}
	var err error
	view.SnapshotID, err = d.requestEventSnapshot(query.SnapshotID)
	if err != nil {
		return view, err
	}
	query.SnapshotID = &view.SnapshotID
	where, args := eventFilter(requestEventSource, query, since)
	if query.IncludeFilters {
		filters, errFilters := d.requestEventFilterValues(query, since)
		if errFilters != nil {
			return billing.RequestEventView{}, errFilters
		}
		view.Filters = filters
	}

	counts := d.db.QueryRow(`
		SELECT count(*),
			coalesce(sum(r.failed != 0), 0)`+where, args...)
	if errCount := counts.Scan(&view.Statuses.All, &view.Statuses.Failed); errCount != nil {
		return billing.RequestEventView{}, fmt.Errorf("Count request events: %w", errCount)
	}
	view.Statuses.Normal = view.Statuses.All - view.Statuses.Failed
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
	pageArgs := append(args, limit, query.Offset)

	// Apply pagination before loading metadata and usage details.
	rows, errQuery := d.db.Query(`WITH page AS MATERIALIZED (SELECT r.id `+page+`)
		SELECT r.id, r.at, r.scope, r.auth_index, r.provider, r.account,
			r.executor_type, r.reasoning_effort, r.service_tier, r.response_service_tier,
			r.upstream_model, r.response_model, r.billing_model, r.failed, r.latency_ms, r.ttft_ms,
			r.accounting_quality, r.price_source, r.reasoning_tokens,
			r.currency, r.total_cost, r.uncached_input_cost, r.cache_read_cost, r.cache_write_cost, r.output_cost,
			r.uncached_input_tokens, r.cache_read_tokens, r.cache_write_tokens, r.billed_output_tokens,
			r.tiered, r.long_context, r.threshold_input_tokens,
			r.applied_input_per_1m, r.applied_output_per_1m,
			r.applied_cache_read_per_1m, r.applied_cache_write_per_1m,
			coalesce(k.preview, ''), coalesce(k.label, ''), `+requestEventSourceName+`,
			coalesce(e.body, ''), r.endpoint, r.transport, r.stream, r.execution_json, r.user_agent, r.x_forwarded_for
		FROM page JOIN request_events r ON r.id = page.id
		LEFT JOIN api_keys k ON k.scope = r.scope
		LEFT JOIN request_errors e ON e.request_event_id = r.id
		ORDER BY r.at DESC, r.id DESC`, pageArgs...)
	if errQuery != nil {
		return billing.RequestEventView{}, fmt.Errorf("Read request events: %w", errQuery)
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
		return billing.RequestEventView{}, fmt.Errorf("Read request events: %w", errRows)
	}
	return view, nil
}

func eventFilter(source string, query billing.RequestEventQuery, since time.Time) (string, []any) {
	where, args := eventTimeFilter(source, query.From, query.To, since)
	if query.SnapshotID != nil {
		where += " AND r.id <= ?"
		args = append(args, *query.SnapshotID)
	}
	for _, filter := range []struct{ expression, value string }{
		{"r.scope", query.Scope},
		{"r.scope", query.KeyScope},
		{eventModelSQL, query.Model},
		{"(" + requestEventSourceName + ")", query.Source},
		{"r.executor_type", query.Executor},
		{"r.provider", query.Provider},
	} {
		if value := strings.TrimSpace(filter.value); value != "" {
			where += " AND " + filter.expression + " = ?"
			args = append(args, value)
		}
	}
	if query.KeyScopeEmpty {
		where += " AND r.scope = ''"
	}
	return where, args
}

// Usage arrives on completion, but rows sort by request start time. Pin the
// inserted ID range so late completions cannot shift subsequent pages.
func (d *DB) requestEventSnapshot(snapshot *int64) (int64, error) {
	if snapshot != nil {
		return *snapshot, nil
	}
	var id int64
	if err := d.db.QueryRow("SELECT coalesce(max(id), 0) FROM request_events").Scan(&id); err != nil {
		return 0, fmt.Errorf("Read request event snapshot: %w", err)
	}
	return id, nil
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
	where, args := eventFilter(requestEventSource, billing.RequestEventQuery{
		Scope: query.Scope, From: query.From, To: query.To, SnapshotID: query.SnapshotID,
	}, since)
	rows, errQuery := d.db.Query(`SELECT DISTINCT `+eventModelSQL+`,
		`+requestEventSourceName+`, r.executor_type, r.provider`+where, args...)
	if errQuery != nil {
		return nil, fmt.Errorf("Read request event filters: %w", errQuery)
	}
	defer rows.Close()

	models, sources, executors, providers := filterValues{}, filterValues{}, filterValues{}, filterValues{}
	for rows.Next() {
		var model, source, executor, provider string
		if errScan := rows.Scan(&model, &source, &executor, &provider); errScan != nil {
			return nil, fmt.Errorf("Read request event filters: %w", errScan)
		}
		models.add(model)
		sources.add(source)
		executors.add(executor)
		providers.add(provider)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("Read request event filters: %w", errRows)
	}
	return &billing.RequestEventFilterValues{
		Models: models.sorted(), Sources: sources.sorted(),
		Executors: executors.sorted(), Providers: providers.sorted(),
	}, nil
}

func scanRequestEventRow(rows *sql.Rows) (billing.RequestEventRow, error) {
	var (
		row                  billing.RequestEventRow
		at, failed           int64
		quality, priceSource string
		execution            string
	)
	if errScan := rows.Scan(&row.ID, &at, &row.Scope, &row.AuthIndex, &row.Provider, &row.Account,
		&row.ExecutorType, &row.ReasoningEffort, &row.ServiceTier, &row.ResponseServiceTier,
		&row.UpstreamModel, &row.ResponseModel, &row.BillingModel, &failed,
		&row.LatencyMS, &row.TTFTMS,
		&quality, &priceSource, &row.ReasoningTokens,
		&row.Cost.Currency, &row.Cost.Total, &row.Cost.UncachedInput, &row.Cost.CacheRead,
		&row.Cost.CacheWrite, &row.Cost.Output,
		&row.Cost.UncachedInputTokens, &row.Cost.CacheReadTokens,
		&row.Cost.CacheWriteTokens, &row.Cost.BilledOutputTokens,
		&row.Cost.Tiered, &row.Cost.LongContext, &row.Cost.ThresholdInputTokens,
		&row.Cost.AppliedInputPer1M, &row.Cost.AppliedOutputPer1M,
		&row.Cost.AppliedCacheReadPer1M, &row.Cost.AppliedCacheWritePer1M,
		&row.Preview, &row.Label, &row.Source, &row.ErrorBody, &row.Endpoint, &row.Transport, &row.Stream, &execution,
		&row.UserAgent, &row.XForwardedFor); errScan != nil {
		return billing.RequestEventRow{}, fmt.Errorf("Read request events: %w", errScan)
	}
	if err := json.Unmarshal([]byte(execution), &row.Execution); err != nil {
		return billing.RequestEventRow{}, fmt.Errorf("Read execution metadata: %w", err)
	}
	row.At = timeAt(at)
	row.Failed = failed != 0
	row.AccountingQuality = billing.TokenAccountingQuality(quality)
	switch priceSource {
	case "custom:x2.5", "builtin:x2.5", "reference:x2.5":
		priceSource = strings.TrimSuffix(priceSource, ":x2.5")
		row.Cost.Multiplier = billing.CodexFastModeMultiplier
	}
	row.PriceSource = billing.PriceSource(priceSource)
	return row, nil
}

func (d *DB) EventKeys(from, to, since time.Time) ([]billing.EventKey, error) {
	where, args := eventTimeFilter("FROM request_events r WHERE r.at >= ?", from, to, since)
	// Seek once per scope, including historical scopes absent from api_keys.
	rows, err := d.db.Query(`WITH RECURSIVE scopes(scope) AS (
		SELECT min(scope) FROM request_events
		UNION ALL
		SELECT (SELECT min(scope) FROM request_events WHERE scope > scopes.scope)
		FROM scopes WHERE scopes.scope IS NOT NULL
	) SELECT scopes.scope, CASE scopes.scope WHEN '' THEN '' ELSE coalesce(NULLIF(k.preview, ''), ?) END,
        coalesce(k.label, ''), coalesce(k.deleted_at, 0)
        FROM scopes
        LEFT JOIN api_keys k ON k.scope = scopes.scope
        WHERE scopes.scope IS NOT NULL AND EXISTS (SELECT 1 `+where+` AND r.scope = scopes.scope)
        ORDER BY scopes.scope = '', coalesce(NULLIF(k.label, ''), k.preview, '') COLLATE NOCASE, scopes.scope`,
		append([]any{billing.UnknownKeyPreview}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("Read event API key filters: %w", err)
	}
	defer rows.Close()
	keys := []billing.EventKey{}
	for rows.Next() {
		var key billing.EventKey
		var deletedAt int64
		if err := rows.Scan(&key.Scope, &key.Preview, &key.Label, &deletedAt); err != nil {
			return nil, fmt.Errorf("Read event API key filters: %w", err)
		}
		key.DeletedAt = timeAt(deletedAt)
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("Read event API key filters: %w", err)
	}
	return keys, nil
}

type filterValues map[string]struct{}

func (values filterValues) add(value string) {
	if value != "" {
		values[value] = struct{}{}
	}
}

func (values filterValues) sorted() []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := asciiLower(result[i]), asciiLower(result[j])
		if a == b {
			return result[i] < result[j]
		}
		return a < b
	})
	return result
}

// Match SQLite NOCASE, which folds ASCII only.
func asciiLower(value string) string {
	bytes := []byte(value)
	for i, c := range bytes {
		if c >= 'A' && c <= 'Z' {
			bytes[i] += 'a' - 'A'
		}
	}
	return string(bytes)
}

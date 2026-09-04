package sqlite

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

func appendRequestErrorEvent(tx *sql.Tx, entry billing.RequestErrorEvent) error {
	entry.Event.Failed = true
	requestEventID, err := appendRequestEvent(tx, entry.Event)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO request_errors
		(request_event_id, status_code, error_type, reason, body) VALUES (?, ?, ?, ?, ?)`,
		requestEventID, entry.Error.StatusCode, entry.Error.ErrorType, entry.Error.Reason, entry.Error.Body)
	if err != nil {
		return fmt.Errorf("写入错误事件：%w", err)
	}
	return nil
}

const requestErrorSource = `
	FROM request_errors e
	JOIN request_events r ON r.id = e.request_event_id
	LEFT JOIN api_keys k ON k.scope = r.scope
	LEFT JOIN credentials c ON c.auth_index = r.auth_index
	WHERE r.at >= ?`

func requestErrorFilter(query billing.RequestErrorQuery, since time.Time) (string, []any) {
	where, args := eventTimeFilter(requestErrorSource, query.From, query.To, since)
	if value := strings.TrimSpace(query.Scope); value != "" {
		where += " AND r.scope = ?"
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.KeyScope); value != "" {
		where += " AND r.scope = ?"
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.Model); value != "" {
		where += " AND coalesce(NULLIF(r.billing_model, ''), r.upstream_model) = ?"
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.Source); value != "" {
		where += " AND coalesce(c.name, '') = ?"
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.Executor); value != "" {
		where += " AND r.executor_type = ?"
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.Provider); value != "" {
		where += " AND coalesce(NULLIF(r.provider, ''), c.provider, '') = ?"
		args = append(args, value)
	}
	if query.StatusCode > 0 {
		where += " AND e.status_code = ?"
		args = append(args, query.StatusCode)
	}
	if value := strings.TrimSpace(query.ErrorType); value != "" {
		where += " AND e.error_type = ?"
		args = append(args, value)
	}
	return where, args
}

func (d *DB) RequestErrors(query billing.RequestErrorQuery, since time.Time) (billing.RequestErrorView, error) {
	view := billing.RequestErrorView{Entries: []billing.RequestErrorRow{}}
	where, args := requestErrorFilter(query, since)
	if query.IncludeFilters {
		filters, err := d.requestErrorFilterValues(query, since)
		if err != nil {
			return billing.RequestErrorView{}, err
		}
		view.Filters = filters
	}
	if err := d.db.QueryRow("SELECT count(*)"+where, args...).Scan(&view.Total); err != nil {
		return billing.RequestErrorView{}, fmt.Errorf("统计错误事件：%w", err)
	}
	limit := query.Limit
	if limit <= 0 {
		limit = -1
	}
	pageArgs := append(append([]any(nil), args...), limit, query.Offset)
	rows, err := d.db.Query(`SELECT r.at, r.scope, coalesce(k.preview, ''), coalesce(k.label, ''),
		r.auth_index, coalesce(c.name, ''), coalesce(NULLIF(r.provider, ''), c.provider, ''),
		r.executor_type, r.upstream_model, r.billing_model, r.latency_ms, r.ttft_ms,
		e.status_code, e.error_type, e.reason, e.body`+where+` ORDER BY r.at DESC, r.id DESC LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return billing.RequestErrorView{}, fmt.Errorf("读取错误事件：%w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row billing.RequestErrorRow
		var at int64
		if err := rows.Scan(&at, &row.Scope, &row.Preview, &row.Label, &row.AuthIndex, &row.Source,
			&row.Provider, &row.ExecutorType, &row.UpstreamModel, &row.BillingModel, &row.LatencyMS,
			&row.TTFTMS, &row.StatusCode, &row.ErrorType, &row.Reason, &row.Body); err != nil {
			return billing.RequestErrorView{}, fmt.Errorf("读取错误事件：%w", err)
		}
		row.At = timeAt(at)
		view.Entries = append(view.Entries, row)
	}
	if err := rows.Err(); err != nil {
		return billing.RequestErrorView{}, fmt.Errorf("读取错误事件：%w", err)
	}
	return view, nil
}

func (d *DB) requestErrorFilterValues(query billing.RequestErrorQuery, since time.Time) (*billing.RequestErrorFilterValues, error) {
	where, args := eventTimeFilter(requestErrorSource, query.From, query.To, since)
	if scope := strings.TrimSpace(query.Scope); scope != "" {
		where += " AND r.scope = ?"
		args = append(args, scope)
	}
	rows, err := d.db.Query(`WITH filtered AS (
		SELECT coalesce(NULLIF(r.billing_model, ''), r.upstream_model) model, coalesce(c.name, '') source,
		r.executor_type executor, coalesce(NULLIF(r.provider, ''), c.provider, '') provider,
		e.status_code, e.error_type`+where+`)
		SELECT kind, value FROM (
		SELECT 'model' kind, model value FROM filtered WHERE model != '' GROUP BY model
		UNION ALL SELECT 'source', source FROM filtered WHERE source != '' GROUP BY source
		UNION ALL SELECT 'executor', executor FROM filtered WHERE executor != '' GROUP BY executor
		UNION ALL SELECT 'provider', provider FROM filtered WHERE provider != '' GROUP BY provider
		UNION ALL SELECT 'status_code', CAST(status_code AS TEXT) FROM filtered WHERE status_code > 0 GROUP BY status_code
		UNION ALL SELECT 'error_type', error_type FROM filtered WHERE error_type != '' GROUP BY error_type
		) ORDER BY kind, value COLLATE NOCASE`, args...)
	if err != nil {
		return nil, fmt.Errorf("读取错误事件筛选项：%w", err)
	}
	defer rows.Close()
	result := &billing.RequestErrorFilterValues{Models: []string{}, Sources: []string{}, Executors: []string{}, Providers: []string{}, StatusCodes: []int{}, ErrorTypes: []string{}}
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			return nil, fmt.Errorf("读取错误事件筛选项：%w", err)
		}
		switch kind {
		case "model":
			result.Models = append(result.Models, value)
		case "source":
			result.Sources = append(result.Sources, value)
		case "executor":
			result.Executors = append(result.Executors, value)
		case "provider":
			result.Providers = append(result.Providers, value)
		case "status_code":
			if code, err := strconv.Atoi(value); err == nil {
				result.StatusCodes = append(result.StatusCodes, code)
			}
		case "error_type":
			result.ErrorTypes = append(result.ErrorTypes, value)
		}
	}
	return result, rows.Err()
}

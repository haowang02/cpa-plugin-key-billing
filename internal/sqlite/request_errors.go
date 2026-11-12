package sqlite

import (
	"database/sql"
	"fmt"
	"sort"
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
		(request_event_id, status_code, error_type, body) VALUES (?, ?, ?, ?)`,
		requestEventID, entry.Error.StatusCode, entry.Error.ErrorType, entry.Error.Body)
	if err != nil {
		return fmt.Errorf("Write error event: %w", err)
	}
	return nil
}

func requestErrorEventQuery(query billing.RequestErrorQuery) billing.RequestEventQuery {
	return billing.RequestEventQuery{
		Scope: query.Scope, KeyScope: query.KeyScope, KeyScopeEmpty: query.KeyScopeEmpty,
		Model: query.Model, Credential: query.Credential, Executor: query.Executor, Provider: query.Provider,
		From: query.From, To: query.To, SnapshotID: query.SnapshotID,
	}
}

func requestErrorFilter(query billing.RequestErrorQuery, since time.Time) (string, []any) {
	return requestErrorEventFilter(query, requestErrorEventQuery(query), since)
}

// events selects the request events; query adds only its error fields.
func requestErrorEventFilter(query billing.RequestErrorQuery, events billing.RequestEventQuery, since time.Time) (string, []any) {
	source := " FROM request_errors e"
	if query.StatusCode <= 0 && strings.TrimSpace(query.ErrorType) == "" && !query.ErrorTypeEmpty {
		// Keep large error bodies out of counts and ID-only pages.
		source += " INDEXED BY request_errors_event_type_status"
	}
	source += " JOIN request_events r ON r.id = e.request_event_id WHERE r.at >= ?"
	where, args := eventFilter(source, events, since)
	if query.StatusCode > 0 {
		where += " AND e.status_code = ?"
		args = append(args, query.StatusCode)
	}
	if query.ErrorTypeEmpty {
		where += " AND e.error_type = ''"
	} else if value := strings.TrimSpace(query.ErrorType); value != "" {
		where += " AND e.error_type = ?"
		args = append(args, value)
	}
	return where, args
}

func (d *DB) RequestErrors(query billing.RequestErrorQuery, since time.Time) (billing.RequestErrorView, error) {
	view := billing.RequestErrorView{Entries: []billing.RequestErrorRow{}}
	var err error
	view.SnapshotID, err = d.requestEventSnapshot(query.SnapshotID)
	if err != nil {
		return view, err
	}
	query.SnapshotID = &view.SnapshotID
	countQuery := query
	countQuery.ErrorType = ""
	countQuery.ErrorTypeEmpty = false
	countWhere, countArgs := requestErrorFilter(countQuery, since)
	countRows, err := d.db.Query("SELECT e.error_type, count(*)"+countWhere+" GROUP BY e.error_type", countArgs...)
	if err != nil {
		return billing.RequestErrorView{}, fmt.Errorf("Count error types: %w", err)
	}
	view.ErrorTypeCounts = map[string]int{}
	for countRows.Next() {
		var errorType string
		var count int
		if err := countRows.Scan(&errorType, &count); err != nil {
			countRows.Close()
			return billing.RequestErrorView{}, fmt.Errorf("Count error types: %w", err)
		}
		view.ErrorTypeCounts[errorType] = count
	}
	countErr := countRows.Err()
	countRows.Close()
	if countErr != nil {
		return billing.RequestErrorView{}, fmt.Errorf("Count error types: %w", countErr)
	}
	if query.ErrorTypeEmpty {
		view.Total = view.ErrorTypeCounts[""]
	} else if errorType := strings.TrimSpace(query.ErrorType); errorType != "" {
		view.Total = view.ErrorTypeCounts[errorType]
	} else {
		for _, count := range view.ErrorTypeCounts {
			view.Total += count
		}
	}
	where, args := requestErrorFilter(query, since)
	if query.IncludeFilters {
		filters, err := d.requestErrorFilterValues(query, since)
		if err != nil {
			return billing.RequestErrorView{}, err
		}
		view.Filters = filters
		if err := d.countEventFilters(&filters.RequestEventFilterValues, requestErrorEventQuery(query),
			func(selection billing.RequestEventQuery) (string, []any) {
				return requestErrorEventFilter(query, selection, since)
			}); err != nil {
			return billing.RequestErrorView{}, err
		}
	}
	limit := query.Limit
	if limit <= 0 {
		limit = -1
	}
	pageArgs := append(args, limit, query.Offset)
	rows, err := d.db.Query(`WITH page AS MATERIALIZED (
		SELECT r.id `+where+` ORDER BY r.at DESC, r.id DESC LIMIT ? OFFSET ?)
		SELECT r.id, r.at, r.scope, coalesce(k.preview, ''), coalesce(k.label, ''),
		r.auth_index, `+requestEventCredentialName+`, r.provider,
		r.executor_type, r.upstream_model, r.billing_model, r.latency_ms, r.ttft_ms,
		e.status_code, e.error_type, e.body
		FROM page JOIN request_events r ON r.id = page.id
		JOIN request_errors e ON e.request_event_id = r.id
		LEFT JOIN api_keys k ON k.scope = r.scope
		ORDER BY r.at DESC, r.id DESC`, pageArgs...)
	if err != nil {
		return billing.RequestErrorView{}, fmt.Errorf("Read error events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row billing.RequestErrorRow
		var at int64
		if err := rows.Scan(&row.ID, &at, &row.Scope, &row.Preview, &row.Label, &row.AuthIndex, &row.Credential,
			&row.Provider, &row.ExecutorType, &row.UpstreamModel, &row.BillingModel, &row.LatencyMS,
			&row.TTFTMS, &row.StatusCode, &row.ErrorType, &row.Body); err != nil {
			return billing.RequestErrorView{}, fmt.Errorf("Read error events: %w", err)
		}
		row.At = timeAt(at)
		view.Entries = append(view.Entries, row)
	}
	if err := rows.Err(); err != nil {
		return billing.RequestErrorView{}, fmt.Errorf("Read error events: %w", err)
	}
	return view, nil
}

func (d *DB) requestErrorFilterValues(query billing.RequestErrorQuery, since time.Time) (*billing.RequestErrorFilterValues, error) {
	where, args := requestErrorFilter(billing.RequestErrorQuery{
		Scope: query.Scope, From: query.From, To: query.To, SnapshotID: query.SnapshotID,
	}, since)
	rows, err := d.db.Query(`SELECT DISTINCT `+eventModelSQL+`, `+requestEventCredentialName+`,
		r.executor_type, r.provider,
		e.status_code, e.error_type`+where, args...)
	if err != nil {
		return nil, fmt.Errorf("Read error event filters: %w", err)
	}
	defer rows.Close()
	models, credentials, executors, providers := filterValues{}, filterValues{}, filterValues{}, filterValues{}
	errorTypes := filterValues{}
	codes := map[int]bool{}
	for rows.Next() {
		var model, credential, executor, provider, errorType string
		var statusCode int
		if err := rows.Scan(&model, &credential, &executor, &provider, &statusCode, &errorType); err != nil {
			return nil, fmt.Errorf("Read error event filters: %w", err)
		}
		models.add(model)
		credentials.add(credential)
		executors.add(executor)
		providers.add(provider)
		errorTypes.add(errorType)
		if statusCode > 0 {
			codes[statusCode] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("Read error event filters: %w", err)
	}
	result := &billing.RequestErrorFilterValues{
		RequestEventFilterValues: billing.RequestEventFilterValues{
			Models: models.sorted(), Credentials: credentials.sorted(),
			Executors: executors.sorted(), Providers: providers.sorted(),
		},
		ErrorTypes: errorTypes.sorted(), StatusCodes: make([]int, 0, len(codes)),
	}
	for code := range codes {
		result.StatusCodes = append(result.StatusCodes, code)
	}
	sort.Ints(result.StatusCodes)
	return result, nil
}

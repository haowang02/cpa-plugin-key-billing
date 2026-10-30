package plugin

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

const (
	defaultEventPageSize = 50
	maxEventPageSize     = 1000
)

func (a *App) listRequestEvents(req ManagementRequest, access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return viewJSON(access, http.StatusOK, billing.RequestEventView{Entries: []billing.RequestEventRow{}})
	}
	query := billing.RequestEventQuery{
		Scope: access.Scope, Model: strings.TrimSpace(req.Query.Get("model")),
		Source: strings.TrimSpace(req.Query.Get("source")), Executor: strings.TrimSpace(req.Query.Get("executor")),
		Provider: strings.TrimSpace(req.Query.Get("provider")),
		Limit:    defaultEventPageSize,
	}
	if !access.APIKey {
		query.KeyScope, query.KeyScopeEmpty = keyScopeFilter(req.Query)
	}
	switch raw := strings.TrimSpace(req.Query.Get("failed")); raw {
	case "":
	case "false", "true":
		failed := raw == "true"
		query.Failed = &failed
	default:
		return viewJSONError(access, http.StatusBadRequest, "invalid", "failed must be true or false")
	}
	if errQuery := requestPageParams(req.Query, &query.Offset, &query.Limit, &query.From, &query.To, &query.SnapshotID); errQuery != nil {
		return viewErrorResponse(access, errQuery)
	}
	if query.Source != "" {
		if !validSourceFilterToken(query.Source) {
			return viewJSONError(access, http.StatusBadRequest, "invalid_filter", "Invalid source filter; refresh the page")
		}
		probe, err := a.store.RequestEvents(billing.RequestEventQuery{
			Scope: access.Scope, From: query.From, To: query.To, SnapshotID: query.SnapshotID, IncludeFilters: true, Limit: 1,
		})
		if err != nil {
			return viewErrorResponse(access, err)
		}
		query.SnapshotID = &probe.SnapshotID
		if probe.Filters == nil {
			return viewJSONError(access, http.StatusBadRequest, "expired_filter", "Source filter expired; refresh the page")
		}
		var found bool
		query.Source, found = resolveSourceFilter(access.Scope, query.Source, probe.Filters.Sources)
		if !found {
			return viewJSONError(access, http.StatusBadRequest, "expired_filter", "Source filter expired; refresh the page")
		}
	}
	query.IncludeFilters = query.Offset == 0
	view, err := a.store.RequestEvents(query)
	if err != nil {
		return viewErrorResponse(access, err)
	}
	if access.APIKey {
		for i := range view.Entries {
			view.Entries[i].Scope = ""
			view.Entries[i].AuthIndex = ""
			if execution := view.Entries[i].Execution; execution != nil {
				copy := *execution
				copy.CredentialRef, copy.BaseURL = "", ""
				view.Entries[i].Execution = &copy
			}
			view.Entries[i].Preview = ""
			view.Entries[i].Label = ""
		}
	}
	if view.Filters != nil {
		view.Filters.SourceOptions = sourceFilterOptions(access.Scope, view.Filters.Sources)
	}
	return viewJSON(access, http.StatusOK, view)
}

func (a *App) listRequestErrors(req ManagementRequest, access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return viewJSON(access, http.StatusOK, billing.RequestErrorView{Entries: []billing.RequestErrorRow{}})
	}
	query := billing.RequestErrorQuery{
		Scope: access.Scope, Model: strings.TrimSpace(req.Query.Get("model")),
		Source: strings.TrimSpace(req.Query.Get("source")), Executor: strings.TrimSpace(req.Query.Get("executor")),
		Provider: strings.TrimSpace(req.Query.Get("provider")), ErrorType: strings.TrimSpace(req.Query.Get("error_type")),
		ErrorTypeEmpty: req.Query.Get("error_type_empty") == "true",
		Limit:          defaultEventPageSize,
	}
	if raw := strings.TrimSpace(req.Query.Get("status_code")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 100 || value > 599 {
			return viewJSONError(access, http.StatusBadRequest, "invalid", "HTTP status code must be an integer from 100 to 599")
		}
		query.StatusCode = value
	}
	if !access.APIKey {
		query.KeyScope, query.KeyScopeEmpty = keyScopeFilter(req.Query)
	}
	if err := requestPageParams(req.Query, &query.Offset, &query.Limit, &query.From, &query.To, &query.SnapshotID); err != nil {
		return viewErrorResponse(access, err)
	}
	if query.Source != "" {
		if !validSourceFilterToken(query.Source) {
			return viewJSONError(access, http.StatusBadRequest, "invalid_filter", "Invalid source filter; refresh the page")
		}
		probe, err := a.store.RequestErrors(billing.RequestErrorQuery{
			Scope: access.Scope, From: query.From, To: query.To, SnapshotID: query.SnapshotID, IncludeFilters: true, Limit: 1,
		})
		if err != nil {
			return viewErrorResponse(access, err)
		}
		query.SnapshotID = &probe.SnapshotID
		if probe.Filters == nil {
			return viewJSONError(access, http.StatusBadRequest, "expired_filter", "Source filter expired; refresh the page")
		}
		var found bool
		query.Source, found = resolveSourceFilter(access.Scope, query.Source, probe.Filters.Sources)
		if !found {
			return viewJSONError(access, http.StatusBadRequest, "expired_filter", "Source filter expired; refresh the page")
		}
	}
	query.IncludeFilters = query.Offset == 0
	view, err := a.store.RequestErrors(query)
	if err != nil {
		return viewErrorResponse(access, err)
	}
	if access.APIKey {
		for i := range view.Entries {
			view.Entries[i].Scope, view.Entries[i].AuthIndex = "", ""
			view.Entries[i].Preview, view.Entries[i].Label = "", ""
		}
	}
	if view.Filters != nil {
		view.Filters.SourceOptions = sourceFilterOptions(access.Scope, view.Filters.Sources)
	}
	return viewJSON(access, http.StatusOK, view)
}

func (a *App) eventKeys(req ManagementRequest) ManagementResponse {
	var from, to time.Time
	if err := timeRangeParams(req.Query, &from, &to); err != nil {
		return errorResponse(err)
	}
	keys, err := a.store.EventKeys(from, to)
	if err != nil {
		return errorResponse(err)
	}
	return JSONResponse(http.StatusOK, keys)
}

func (a *App) clearRequestEvents(req ManagementRequest) ManagementResponse {
	var input struct {
		OlderThanDays billing.RetentionDays `json:"older_than_days"`
	}
	if err := decodeStrict(req.Body, &input); err != nil {
		return errorResponse(err)
	}
	cleared, err := a.store.ClearRequestEvents(input.OlderThanDays)
	if err != nil {
		return errorResponse(err)
	}
	return JSONResponse(http.StatusOK, map[string]any{"cleared": cleared})
}

func requestPageParams(values url.Values, offset, limit *int, from, to *time.Time, snapshot **int64) error {
	if raw := strings.TrimSpace(values.Get("snapshot_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 0 {
			return &billing.Error{Kind: billing.KindInvalid, Msg: "snapshot_id must be a non-negative integer"}
		}
		*snapshot = &id
	}
	if errOffset := countParam(values, "offset", offset); errOffset != nil {
		return errOffset
	}
	if errLimit := countParam(values, "limit", limit); errLimit != nil {
		return errLimit
	}
	if err := timeRangeParams(values, from, to); err != nil {
		return err
	}
	if *limit < 1 || *limit > maxEventPageSize {
		return &billing.Error{Kind: billing.KindInvalid, Msg: "Limit must be an integer from 1 to 1000"}
	}
	return nil
}

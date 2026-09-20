package plugin

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/messages"
)

var emailMaskRegex = regexp.MustCompile(`([a-zA-Z0-9._-]+)@([a-zA-Z0-9.-]+\.[a-zA-Z0-9.-]+)`)

func maskEmailText(text string) string {
	return emailMaskRegex.ReplaceAllStringFunc(text, func(match string) string {
		parts := strings.SplitN(match, "@", 2)
		if len(parts) != 2 {
			return match
		}
		name := parts[0]
		if len(name) > 3 {
			name = name[:3]
		}
		return name + "***@" + parts[1]
	})
}

const (
	defaultEventPageSize = 50
	maxEventPageSize     = 1000
)

type viewAccess struct {
	APIKey  bool
	Scope   string
	Tracked bool
	Key     billing.KeyView
}

func (a *App) apiKeyViewAccess(req ManagementRequest) (viewAccess, bool) {
	scope, ok := accountScope(req.Headers)
	if !ok {
		return viewAccess{}, false
	}
	view, tracked := a.store.KeyViewForScope(scope)
	return viewAccess{APIKey: true, Scope: scope, Tracked: tracked, Key: view}, true
}

func (a *App) routeResource(req ManagementRequest, suffix string) ManagementResponse {
	if req.Method != http.MethodGet {
		return apiKeyJSONError(http.StatusNotFound, "not_found", "Resource route not found: "+req.Method+" "+req.Path)
	}
	var handler func(*App, ManagementRequest, viewAccess) ManagementResponse
	for _, endpoint := range resourceEndpoints {
		if endpoint.path == suffix {
			handler = endpoint.handle
			break
		}
	}
	if handler == nil {
		return apiKeyJSONError(http.StatusNotFound, "not_found", "Resource route not found: "+req.Method+" "+req.Path)
	}
	access, ok := a.apiKeyViewAccess(req)
	if !ok {
		return apiKeyUnauthorized()
	}
	return handler(a, req, access)
}

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
		query.KeyScope = strings.TrimSpace(req.Query.Get("api_key"))
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
	query.IncludeFilters = query.Offset == 0
	view, err := a.store.RequestEvents(query)
	if err != nil {
		return viewErrorResponse(access, err)
	}
	if access.APIKey {
		for i := range view.Entries {
			view.Entries[i].Scope = ""
			view.Entries[i].AuthIndex = ""
			view.Entries[i].Preview = ""
			view.Entries[i].Label = ""
			view.Entries[i].Source = maskEmailText(view.Entries[i].Source)
		}
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
		query.KeyScope = strings.TrimSpace(req.Query.Get("api_key"))
	}
	if err := requestPageParams(req.Query, &query.Offset, &query.Limit, &query.From, &query.To, &query.SnapshotID); err != nil {
		return viewErrorResponse(access, err)
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
			view.Entries[i].Source = maskEmailText(view.Entries[i].Source)
		}
	}
	return viewJSON(access, http.StatusOK, view)
}

func (a *App) analysis(req ManagementRequest, access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return viewJSON(access, http.StatusOK, billing.AnalysisView{
			UsageDistribution: billing.UsageDistribution{
				APIKeys: []billing.AnalysisComposition{}, Models: []billing.AnalysisComposition{}, Sources: []billing.AnalysisComposition{},
			},
		})
	}
	query := billing.RequestEventQuery{Scope: access.Scope}
	if !access.APIKey {
		query.KeyScope = strings.TrimSpace(req.Query.Get("api_key"))
	}
	if err := timeParam(req.Query, "from", &query.From); err != nil {
		return viewErrorResponse(access, err)
	}
	if err := timeParam(req.Query, "to", &query.To); err != nil {
		return viewErrorResponse(access, err)
	}
	if name := strings.TrimSpace(req.Query.Get("timezone")); name != "" {
		location, err := time.LoadLocation(name)
		if err != nil {
			return viewErrorResponse(access, &billing.Error{
				Kind: billing.KindInvalid, Msg: "Timezone must be a valid IANA identifier",
			})
		}
		query.Timezone = location
	}
	view, err := a.store.Analysis(query)
	if err != nil {
		return viewErrorResponse(access, err)
	}
	if access.APIKey || query.KeyScope != "" {
		view.UsageDistribution.APIKeys = []billing.AnalysisComposition{}
	}
	if access.APIKey {
		for i := range view.UsageDistribution.Sources {
			view.UsageDistribution.Sources[i].Key = maskEmailText(view.UsageDistribution.Sources[i].Key)
			view.UsageDistribution.Sources[i].Label = maskEmailText(view.UsageDistribution.Sources[i].Label)
		}
	}
	return viewJSON(access, http.StatusOK, view)
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
	if errFrom := timeParam(values, "from", from); errFrom != nil {
		return errFrom
	}
	if errTo := timeParam(values, "to", to); errTo != nil {
		return errTo
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(*to) {
		return &billing.Error{Kind: billing.KindInvalid, Msg: "Start time must be earlier than end time"}
	}
	if *limit < 1 || *limit > maxEventPageSize {
		return &billing.Error{Kind: billing.KindInvalid, Msg: "Limit must be an integer from 1 to 1000"}
	}
	return nil
}

func timeParam(query url.Values, name string, target *time.Time) error {
	raw := strings.TrimSpace(query.Get(name))
	if raw == "" {
		return nil
	}
	parsed, errParse := time.Parse(time.RFC3339Nano, raw)
	if errParse != nil {
		detail := messages.New("%s must be an RFC3339 timestamp", name)
		return &billing.Error{Kind: billing.KindInvalid, Msg: detail.Text, Detail: detail}
	}
	*target = parsed
	return nil
}

func countParam(query url.Values, name string, target *int) error {
	raw := strings.TrimSpace(query.Get(name))
	if raw == "" {
		return nil
	}
	parsed, errParse := strconv.Atoi(raw)
	if errParse != nil || parsed < 0 {
		detail := messages.New("%s must be a non-negative integer", name)
		return &billing.Error{Kind: billing.KindInvalid, Msg: detail.Text, Detail: detail}
	}
	*target = parsed
	return nil
}

func viewJSON(access viewAccess, status int, payload any) ManagementResponse {
	if access.APIKey {
		return apiKeyJSON(status, payload)
	}
	return JSONResponse(status, payload)
}

func viewJSONError(access viewAccess, status int, code, message string) ManagementResponse {
	if access.APIKey {
		return apiKeyJSONError(status, code, message)
	}
	return JSONError(status, code, message)
}

func viewDetailedError(access viewAccess, status int, code string, err error) ManagementResponse {
	response := jsonMessageError(status, code, messages.FromError(err))
	if access.APIKey {
		secureAPIKeyResponse(&response)
	}
	return response
}

func viewErrorResponse(access viewAccess, err error) ManagementResponse {
	response := errorResponse(err)
	if access.APIKey {
		secureAPIKeyResponse(&response)
	}
	return response
}

func (a *App) eventKeys(req ManagementRequest) ManagementResponse {
	var from, to time.Time
	if err := timeParam(req.Query, "from", &from); err != nil {
		return errorResponse(err)
	}
	if err := timeParam(req.Query, "to", &to); err != nil {
		return errorResponse(err)
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return JSONError(http.StatusBadRequest, "invalid", "Start time must be earlier than end time")
	}
	keys, err := a.store.EventKeys(from, to)
	if err != nil {
		return errorResponse(err)
	}
	return JSONResponse(http.StatusOK, keys)
}

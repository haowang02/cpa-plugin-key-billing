package plugin

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"cpa-key-billing/internal/billing"
)

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
		return apiKeyJSONError(http.StatusNotFound, "not_found", "资源路由不存在："+req.Method+" "+req.Path)
	}
	var handler func(*App, ManagementRequest, viewAccess) ManagementResponse
	for _, endpoint := range resourceEndpoints {
		if endpoint.path == suffix {
			handler = endpoint.handle
			break
		}
	}
	if handler == nil {
		return apiKeyJSONError(http.StatusNotFound, "not_found", "资源路由不存在："+req.Method+" "+req.Path)
	}
	access, ok := a.apiKeyViewAccess(req)
	if !ok {
		return apiKeyUnauthorized()
	}
	return handler(a, req, access)
}

func (a *App) listPrices(access viewAccess) ManagementResponse {
	if _, errCatalog := billing.EnsureBuiltinCatalog(); errCatalog != nil {
		return viewErrorResponse(access, errCatalog)
	}
	prices := a.store.PriceRows()
	return viewJSON(access, http.StatusOK, prices)
}

func (a *App) listRequestEvents(req ManagementRequest, access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return viewJSON(access, http.StatusOK, billing.RequestEventView{Entries: []billing.RequestEventRow{}})
	}
	query := billing.RequestEventQuery{
		Scope: access.Scope, Model: strings.TrimSpace(req.Query.Get("model")),
		Source: strings.TrimSpace(req.Query.Get("source")), Executor: strings.TrimSpace(req.Query.Get("executor")),
		Provider: strings.TrimSpace(req.Query.Get("provider")), Status: strings.TrimSpace(req.Query.Get("status")),
		Limit: defaultEventPageSize,
	}
	if !access.APIKey {
		query.KeyScope = strings.TrimSpace(req.Query.Get("api_key"))
	}
	if !billing.ValidRequestEventStatus(query.Status) {
		return viewJSONError(access, http.StatusBadRequest, "invalid", "status 无效："+query.Status)
	}
	if errQuery := requestPageParams(req.Query, &query.Offset, &query.Limit, &query.From, &query.To); errQuery != nil {
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
		}
		if view.Filters != nil {
			view.Filters.APIKeys = []billing.APIKeyFilterOption{}
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
		Limit: defaultEventPageSize,
	}
	if raw := strings.TrimSpace(req.Query.Get("status_code")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 100 || value > 599 {
			return viewJSONError(access, http.StatusBadRequest, "invalid", "status_code 必须是 100 到 599 之间的整数")
		}
		query.StatusCode = value
	}
	if !access.APIKey {
		query.KeyScope = strings.TrimSpace(req.Query.Get("api_key"))
	}
	if err := requestPageParams(req.Query, &query.Offset, &query.Limit, &query.From, &query.To); err != nil {
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
		}
		if view.Filters != nil {
			view.Filters.APIKeys = []billing.APIKeyFilterOption{}
		}
	}
	return viewJSON(access, http.StatusOK, view)
}

func (a *App) analysis(req ManagementRequest, access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return viewJSON(access, http.StatusOK, billing.AnalysisView{
			Summary: billing.AnalysisSummary{Cost: billing.AnalysisCostSummary{Available: true}},
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
				Kind: billing.KindInvalid, Msg: "timezone 不是有效的 IANA 时区",
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
	return viewJSON(access, http.StatusOK, view)
}

func requestPageParams(values url.Values, offset, limit *int, from, to *time.Time) error {
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
		return &billing.Error{Kind: billing.KindInvalid, Msg: "from 必须早于 to"}
	}
	if *limit < 1 || *limit > maxEventPageSize {
		return &billing.Error{Kind: billing.KindInvalid, Msg: "limit 必须是 1 到 1000 之间的整数"}
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
		return &billing.Error{Kind: billing.KindInvalid, Msg: name + " 必须是 RFC3339 时间"}
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
		return &billing.Error{Kind: billing.KindInvalid, Msg: name + " 必须是非负整数"}
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

func viewErrorResponse(access viewAccess, err error) ManagementResponse {
	response := errorResponse(err)
	if access.APIKey {
		secureAPIKeyResponse(&response)
	}
	return response
}

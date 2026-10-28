package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/messages"
)

const (
	defaultEventPageSize = 50
	maxEventPageSize     = 1000
)

func sourceFilterToken(scope, source string) string {
	digest := sha256.Sum256([]byte("filter:v1\x00source\x00" + scope + "\x00" + source))
	return hex.EncodeToString(digest[:])
}

func validSourceFilterToken(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func resolveSourceFilter(scope, token string, sources []string) (string, bool) {
	for _, source := range sources {
		if sourceFilterToken(scope, source) == token {
			return source, true
		}
	}
	return "", false
}

func sourceFilterOptions(scope string, sources []string) []billing.RequestSourceOption {
	options := make([]billing.RequestSourceOption, 0, len(sources))
	for _, source := range sources {
		options = append(options, billing.RequestSourceOption{
			Value: sourceFilterToken(scope, source),
			Label: source,
		})
	}
	return options
}

type viewAccess struct {
	APIKey     bool
	Scope      string
	Tracked    bool
	MaskEmails bool
	Key        billing.KeyView
}

// usageScope limits usage statistics to the presenting API key.
func (access viewAccess) usageScope() string {
	if access.APIKey {
		return access.Scope
	}
	return ""
}

func (a *App) apiKeyViewAccess(req ManagementRequest) (viewAccess, bool) {
	scope, ok := accountScope(req.Headers)
	if !ok {
		return viewAccess{}, false
	}
	view, tracked := a.store.KeyViewForScope(scope)
	return viewAccess{APIKey: true, Scope: scope, Tracked: tracked, MaskEmails: a.store.Config().MaskAPIKeyViewEmails, Key: view}, true
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

func (a *App) analysis(req ManagementRequest, access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return viewJSON(access, http.StatusOK, billing.AnalysisView{
			UsageDistribution: billing.UsageDistribution{
				APIKeys: []billing.AnalysisComposition{}, Models: []billing.AnalysisComposition{}, Sources: []billing.AnalysisComposition{},
			},
		})
	}
	query, err := analysisQuery(req, access)
	if err != nil {
		return viewErrorResponse(access, err)
	}
	view, err := a.store.Analysis(query)
	if err != nil {
		return viewErrorResponse(access, err)
	}
	if access.APIKey || query.KeyScope != "" || query.KeyScopeEmpty {
		view.UsageDistribution.APIKeys = []billing.AnalysisComposition{}
	}
	for i := range view.UsageDistribution.Sources {
		view.UsageDistribution.Sources[i].Filter = sourceFilterToken(access.Scope, view.UsageDistribution.Sources[i].Key)
	}
	return viewJSON(access, http.StatusOK, view)
}

func (a *App) analysisSpeed(req ManagementRequest, access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return viewJSON(access, http.StatusOK, billing.AnalysisSpeedView{Trend: []billing.SpeedTrendPoint{}})
	}
	query, err := analysisQuery(req, access)
	if err != nil {
		return viewErrorResponse(access, err)
	}
	if query.Model = strings.TrimSpace(req.Query.Get("model")); query.Model == "" {
		return viewErrorResponse(access, &billing.Error{Kind: billing.KindInvalid, Msg: "Model ID is required"})
	}
	view, err := a.store.AnalysisSpeed(query)
	if err != nil {
		return viewErrorResponse(access, err)
	}
	return viewJSON(access, http.StatusOK, view)
}

// usageModels breaks down by model the usage of the requests a query selects:
// a credential's, in a quota period when the query names one, or those of rows
// of the usage distribution, which a dimension and its values name. Source rows
// are named by their filters.
func (a *App) usageModels(req ManagementRequest, access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return apiKeyUnauthorized()
	}
	query := billing.UsageQuery{AuthIndex: strings.TrimSpace(req.Query.Get("auth_index")), Provider: strings.TrimSpace(req.Query.Get("provider")),
		ModelMatch: billing.ModelMatch{Include: req.Query["include"], Exclude: req.Query["exclude"]}}
	if err := timeRangeParams(req.Query, &query.From, &query.To); err != nil {
		return viewErrorResponse(access, err)
	}
	if req.Query.Has("credential_ref") {
		ref := req.Query.Get("credential_ref")
		credential, ok := a.visibleConfigCredentials(access)[ref]
		if !ok || credential.AuthIndex == "" {
			return viewJSONError(access, http.StatusNotFound, "not_found", "Configured credential does not exist")
		}
		query.AuthIndex = credential.AuthIndex
	} else if req.Query.Has("auth_index") {
		if access.APIKey {
			if selected, failure := a.resolveAuthFile(req, access); selected == nil {
				return failure
			}
		} else if query.AuthIndex == "" || len(query.AuthIndex) > 512 {
			return viewJSONError(access, http.StatusBadRequest, "invalid", "Invalid auth file identifier")
		}
	}
	if !access.APIKey {
		if key, empty := keyScopeFilter(req.Query); key != "" || empty {
			query.KeyScopes = []string{key}
		}
	}
	scope, values := access.usageScope(), req.Query["value"]
	switch dimension := req.Query.Get("dimension"); {
	case dimension == "":
	case len(values) == 0:
		return viewJSONError(access, http.StatusBadRequest, "invalid", "Name the usage distribution rows")
	case dimension == "api_keys" && !access.APIKey && query.KeyScopes == nil:
		query.KeyScopes = values
	case dimension == "models":
		query.Models = values
	case dimension == "sources":
		sources, err := a.store.UsageSources(scope, query)
		if err != nil {
			return viewErrorResponse(access, err)
		}
		for _, source := range sources {
			if slices.Contains(values, sourceFilterToken(access.Scope, source)) {
				query.Sources = append(query.Sources, source)
			}
		}
		if query.Sources == nil {
			return viewJSONError(access, http.StatusBadRequest, "expired_filter", "Source filter expired; refresh the page")
		}
	default:
		return viewJSONError(access, http.StatusBadRequest, "invalid", "Invalid usage distribution dimension")
	}
	models, err := a.store.UsageByModel(scope, query)
	if err != nil {
		return viewErrorResponse(access, err)
	}
	return viewJSON(access, http.StatusOK, map[string][]billing.ModelUsage{"models": models})
}

func analysisQuery(req ManagementRequest, access viewAccess) (billing.RequestEventQuery, error) {
	query := billing.RequestEventQuery{Scope: access.Scope}
	if !access.APIKey {
		query.KeyScope, query.KeyScopeEmpty = keyScopeFilter(req.Query)
	}
	if err := timeRangeParams(req.Query, &query.From, &query.To); err != nil {
		return query, err
	}
	if name := strings.TrimSpace(req.Query.Get("timezone")); name != "" {
		location, err := time.LoadLocation(name)
		if err != nil {
			return query, &billing.Error{Kind: billing.KindInvalid, Msg: "Timezone must be a valid IANA identifier"}
		}
		query.Timezone = location
	}
	return query, nil
}

func keyScopeFilter(values url.Values) (string, bool) {
	if values.Get("api_key_empty") == "true" {
		return "", true
	}
	return strings.TrimSpace(values.Get("api_key")), false
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

func timeRangeParams(query url.Values, from, to *time.Time) error {
	if err := timeParam(query, "from", from); err != nil {
		return err
	}
	if err := timeParam(query, "to", to); err != nil {
		return err
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(*to) {
		return &billing.Error{Kind: billing.KindInvalid, Msg: "Start time must be earlier than end time"}
	}
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
		handled := false
		if access.MaskEmails {
			payload, handled = maskAPIKeyPayload(payload)
		}
		response := apiKeyJSON(status, payload)
		return protectAPIKeyResponse(access, response, handled)
	}
	return JSONResponse(status, payload)
}

func viewJSONError(access viewAccess, status int, code, message string) ManagementResponse {
	if access.APIKey {
		return protectAPIKeyResponse(access, apiKeyJSONError(status, code, message), false)
	}
	return JSONError(status, code, message)
}

func viewDetailedError(access viewAccess, status int, code string, err error) ManagementResponse {
	response := jsonMessageError(status, code, messages.FromError(err))
	if access.APIKey {
		response = protectAPIKeyResponse(access, response, false)
	}
	return response
}

func viewErrorResponse(access viewAccess, err error) ManagementResponse {
	response := errorResponse(err)
	if access.APIKey {
		response = protectAPIKeyResponse(access, response, false)
	}
	return response
}

func protectAPIKeyResponse(access viewAccess, response ManagementResponse, handled bool) ManagementResponse {
	if access.APIKey {
		secureAPIKeyResponse(&response)
		if access.MaskEmails && !handled {
			response.Body = []byte(maskEmails(string(response.Body)))
		}
	}
	return response
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

package plugin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/messages"
)

type keyRow struct {
	billing.KeyView
	CredentialLabels map[string]string `json:"credential_labels"`
}

type routeRow struct {
	billing.RouteView
	CredentialLabels map[string]string `json:"credential_labels"`
}

func (a *App) keyRows() []keyRow {
	keys := a.store.KeyViews()
	rows := make([]keyRow, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, keyRow{KeyView: key, CredentialLabels: a.credentialLabels(key.RouteBindings.CredentialRefs())})
	}
	return rows
}

func (a *App) routeRows() []routeRow {
	routes := a.store.RouteViews()
	rows := make([]routeRow, 0, len(routes))
	for _, route := range routes {
		rows = append(rows, routeRow{RouteView: route, CredentialLabels: a.credentialLabels(route.Rule.CredentialRefs())})
	}
	return rows
}

func (a *App) createRoute(req ManagementRequest) ManagementResponse {
	var body struct {
		Name   string            `json:"name"`
		Rule   billing.RouteRule `json:"rule"`
		Scopes []string          `json:"scopes,omitempty"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	rule, errRule := billing.NormalizeRouteRule(body.Rule)
	if errRule != nil {
		return errorResponse(errRule)
	}
	route := billing.Route{Name: body.Name, Rule: rule}
	if response := a.validateNewCredentialRefs(rule.CredentialRefs(), nil); response != nil {
		return *response
	}
	stored, errCreate := a.store.CreateRoute(route, body.Scopes)
	if errCreate != nil {
		return errorResponse(errCreate)
	}
	return JSONResponse(http.StatusCreated, map[string]any{"route": stored})
}

func (a *App) updateRoute(req ManagementRequest) ManagementResponse {
	var body struct {
		billing.RoutePatch
		Scopes *[]string `json:"scopes,omitempty"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	patch := body.RoutePatch
	if patch.Rule != nil {
		rule, errRule := billing.NormalizeRouteRule(*patch.Rule)
		if errRule != nil {
			return errorResponse(errRule)
		}
		patch.Rule = &rule
		var existing []string
		if route, ok := a.store.Route(patch.ID); ok {
			existing = route.Rule.CredentialRefs()
		}
		if response := a.validateNewCredentialRefs(patch.Rule.CredentialRefs(), existing); response != nil {
			return *response
		}
	}
	stored, errUpdate := a.store.UpdateRoute(patch, body.Scopes)
	if errUpdate != nil {
		return errorResponse(errUpdate)
	}
	return JSONResponse(http.StatusOK, map[string]any{"route": stored})
}

func (a *App) deleteRoute(req ManagementRequest) ManagementResponse {
	id := strings.TrimSpace(req.Query.Get("id"))
	result, errDelete := a.store.DeleteRoute(id)
	if errDelete != nil {
		return errorResponse(errDelete)
	}
	return JSONResponse(http.StatusOK, result)
}

func (a *App) setKeyRoutes(req ManagementRequest) ManagementResponse {
	var body struct {
		Scope    string                `json:"scope"`
		Bindings billing.RouteBindings `json:"bindings"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	bindings, errBindings := billing.NormalizeRouteBindings(body.Bindings)
	if errBindings != nil {
		return errorResponse(errBindings)
	}
	var existing []string
	if key, ok := a.store.KeyViewForScope(body.Scope); ok {
		existing = key.RouteBindings.CredentialRefs()
	}
	if response := a.validateNewCredentialRefs(bindings.CredentialRefs(), existing); response != nil {
		return *response
	}
	if errApply := a.store.SetKeyRoutes(body.Scope, bindings); errApply != nil {
		return errorResponse(errApply)
	}
	return JSONResponse(http.StatusOK, struct{}{})
}

func (a *App) validateNewCredentialRefs(refs, existing []string) *ManagementResponse {
	old := make(map[string]struct{}, len(existing))
	for _, ref := range existing {
		old[strings.ToLower(strings.TrimSpace(ref))] = struct{}{}
	}
	newRefs := make([]string, 0, len(refs))
	for _, ref := range refs {
		if _, ok := old[strings.ToLower(strings.TrimSpace(ref))]; ok {
			continue
		}
		newRefs = append(newRefs, ref)
	}
	if len(newRefs) == 0 {
		return nil
	}
	if err := a.refreshCredentialInventory(); err != nil {
		response := jsonMessageError(http.StatusBadGateway, "host_unavailable", messages.New("Failed to load upstream credentials: %s", err.Error()))
		return &response
	}
	if missing := a.missingCredentialRef(newRefs); missing != "" {
		response := jsonMessageError(http.StatusBadRequest, "invalid", messages.New("Upstream credential no longer exists: %s", missing))
		return &response
	}
	return nil
}

func (a *App) listPluginLogs(req ManagementRequest) ManagementResponse {
	query := billing.PluginLogQuery{Limit: 100}
	if raw := strings.TrimSpace(req.Query.Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			return JSONError(http.StatusBadRequest, "invalid", "Limit must be an integer from 1 to 500")
		}
		query.Limit = value
	}
	if raw := strings.TrimSpace(req.Query.Get("before_id")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 {
			return JSONError(http.StatusBadRequest, "invalid", "Invalid pagination cursor")
		}
		query.BeforeID = value
	}
	if raw := strings.TrimSpace(req.Query.Get("since")); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return JSONError(http.StatusBadRequest, "invalid", "Start time must use RFC3339 format")
		}
		query.Since = value
	}
	levels := strings.TrimSpace(req.Query.Get("level"))
	if levels != "" && levels != "all" {
		for _, raw := range strings.Split(levels, ",") {
			level := billing.PluginLogLevel(strings.TrimSpace(raw))
			if level != billing.PluginLogDebug && level != billing.PluginLogInfo && level != billing.PluginLogError {
				return JSONError(http.StatusBadRequest, "invalid", "Invalid log level")
			}
			query.Levels = append(query.Levels, level)
		}
	}
	page, err := a.store.PluginLogsPage(query)
	if err != nil {
		return errorResponse(err)
	}
	return JSONResponse(http.StatusOK, page)
}

func (a *App) clearPluginLogs() ManagementResponse {
	cleared, errClear := a.store.ClearPluginLogs()
	if errClear != nil {
		return errorResponse(errClear)
	}
	return JSONResponse(http.StatusOK, map[string]any{"cleared": cleared})
}

func (a *App) bindKey(req ManagementRequest) ManagementResponse {
	var body struct {
		Scope  string `json:"scope"`
		PlanID string `json:"plan_id"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	if errBind := a.store.BindKey(body.Scope, body.PlanID); errBind != nil {
		return errorResponse(errBind)
	}
	return JSONResponse(http.StatusOK, struct{}{})
}

func (a *App) unbindKey(req ManagementRequest) ManagementResponse {
	var body struct {
		Scope string `json:"scope"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	if errUnbind := a.store.UnbindKey(body.Scope); errUnbind != nil {
		return errorResponse(errUnbind)
	}
	return JSONResponse(http.StatusOK, struct{}{})
}

func (a *App) resetKeys(req ManagementRequest) ManagementResponse {
	var body billing.ResetRequest
	if err := decodeStrict(req.Body, &body); err != nil {
		return errorResponse(err)
	}
	result, err := a.store.ResetQuota(body)
	if err != nil {
		return errorResponse(err)
	}
	return JSONResponse(http.StatusOK, result)
}

func (a *App) labelKey(req ManagementRequest) ManagementResponse {
	var body struct {
		Scope string `json:"scope"`
		Label string `json:"label"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	if errLabel := a.store.SetLabel(body.Scope, body.Label); errLabel != nil {
		return errorResponse(errLabel)
	}
	return JSONResponse(http.StatusOK, struct{}{})
}

func (a *App) setKeyConcurrency(req ManagementRequest) ManagementResponse {
	var body struct {
		Scope string `json:"scope"`
		Limit int    `json:"concurrency_limit"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	if errSet := a.store.SetConcurrencyLimit(body.Scope, body.Limit); errSet != nil {
		return errorResponse(errSet)
	}
	return JSONResponse(http.StatusOK, struct{}{})
}

// The UI supplies the configured keys; only their hashes and masks are retained.
func (a *App) syncKeys(req ManagementRequest) ManagementResponse {
	var body struct {
		Keys       []string `json:"keys"`
		AllowEmpty bool     `json:"allow_empty"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	result, errSync := a.store.SyncKeys(body.Keys, body.AllowEmpty)
	if errSync != nil {
		return errorResponse(errSync)
	}
	if result.Added > 0 || result.Deleted > 0 {
		a.store.AddPluginLog(billing.PluginLogInfo, "CLIProxyAPI API keys synced: %d added, %d deleted",
			result.Added, result.Deleted)
	}
	live := make(map[string]struct{})
	for _, key := range a.store.KeyViews() {
		if key.DeletedAt.IsZero() {
			live[key.Scope] = struct{}{}
		}
	}
	a.scheduler.prune(live)
	return JSONResponse(http.StatusOK, result)
}

func decodeStrict(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(body)))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(target); errDecode != nil {
		detail := messages.New("Invalid request body: %v", errDecode)
		return &billing.Error{Kind: billing.KindInvalid, Msg: detail.Text, Detail: detail}
	}
	if errTrailing := decoder.Decode(&struct{}{}); errTrailing != io.EOF {
		return &billing.Error{Kind: billing.KindInvalid, Msg: "The request body must contain exactly one JSON value"}
	}
	return nil
}

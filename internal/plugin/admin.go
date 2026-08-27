package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"cpa-key-billing/internal/billing"
)

const (
	defaultLogPageSize = 50
	maxLogPageSize     = 1000
)

func (a *App) putPrices(req ManagementRequest) ManagementResponse {
	var rule billing.PriceRule
	if errDecode := decodeStrict(req.Body, &rule); errDecode != nil {
		return errorResponse(errDecode)
	}
	stored, errUpsert := a.store.UpsertPrice(rule)
	if errUpsert != nil {
		return errorResponse(errUpsert)
	}
	return JSONResponse(http.StatusOK, map[string]any{"price": stored})
}

func (a *App) searchPriceCatalog(req ManagementRequest) ManagementResponse {
	if _, errCatalog := billing.EnsureBuiltinCatalog(); errCatalog != nil {
		return errorResponse(errCatalog)
	}
	limit := 20
	if raw := strings.TrimSpace(req.Query.Get("limit")); raw != "" {
		parsed, errParse := strconv.Atoi(raw)
		if errParse != nil || parsed < 1 || parsed > 50 {
			return JSONError(http.StatusBadRequest, "invalid", "limit 必须是 1 到 50 之间的整数")
		}
		limit = parsed
	}
	return JSONResponse(http.StatusOK, map[string]any{
		"models": billing.SearchCatalog(req.Query.Get("q"), limit),
	})
}

// The plugin cannot enumerate models itself: the host exposes no callback for
// it, and /v1/models sits behind a downstream API key rather than the
// management key. The browser holds both, so it does the read — the same
// division of labour as the API key sync.
func (a *App) syncModels(req ManagementRequest) ManagementResponse {
	if _, errCatalog := billing.EnsureBuiltinCatalog(); errCatalog != nil {
		return errorResponse(errCatalog)
	}
	var body struct {
		Models []string `json:"models"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	result, errSync := a.store.SyncModels(body.Models)
	if errSync != nil {
		return errorResponse(errSync)
	}
	return JSONResponse(http.StatusOK, result)
}

// One reload, one round trip: every management call is separately authenticated
// through the host. The two logs stay out of it because they grow with traffic
// rather than with configuration, and each is scoped to its own tab and page.
type overviewResponse struct {
	Status billing.Status     `json:"status"`
	Keys   []billing.KeyView  `json:"keys"`
	Plans  []billing.Plan     `json:"plans"`
	Prices []billing.PriceRow `json:"prices"`
	// The all-models group is not here: it holds no models, because it means
	// every model there will ever be. The panel offers it as a checkbox of its
	// own, and a key that selected it carries no groups at all.
	ModelGroups []billing.ModelGroup `json:"model_groups"`
	Stats       billing.StatsView    `json:"stats"`
}

func (a *App) overview() ManagementResponse {
	if _, errCatalog := billing.EnsureBuiltinCatalog(); errCatalog != nil {
		return errorResponse(errCatalog)
	}
	directory := a.store.KeyDirectory()
	return JSONResponse(http.StatusOK, overviewResponse{
		Status:      a.store.Status(),
		Keys:        directory.Keys,
		Plans:       a.store.Plans(),
		Prices:      a.store.PriceTable().Models,
		ModelGroups: a.store.ModelGroups(),
		Stats:       billing.StatsFrom(directory),
	})
}

func (a *App) refreshPriceCatalog() ManagementResponse {
	result, errRefresh := a.store.RefreshPriceCatalog()
	if errRefresh != nil {
		a.store.Event(billing.EventError, "更新 models.dev 参考价目录失败：%v", errRefresh)
		return errorResponse(errRefresh)
	}
	a.store.Event(billing.EventInfo, "已更新 models.dev 参考价目录，%d 条定价随之调整。", result.UpdatedModels)
	return JSONResponse(http.StatusOK, result)
}

func (a *App) resetPrices() ManagementResponse {
	if _, errCatalog := billing.EnsureBuiltinCatalog(); errCatalog != nil {
		return errorResponse(errCatalog)
	}
	return JSONResponse(http.StatusOK, map[string]any{"restored": a.store.ResetPrices()})
}

func (a *App) createPlan(req ManagementRequest) ManagementResponse {
	var body struct {
		billing.Plan
		Scopes []string `json:"scopes,omitempty"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	stored, errCreate := a.store.CreatePlanWithBindings(body.Plan, body.Scopes)
	if errCreate != nil {
		return errorResponse(errCreate)
	}
	return JSONResponse(http.StatusCreated, map[string]any{"plan": stored})
}

func (a *App) updatePlan(req ManagementRequest) ManagementResponse {
	var body struct {
		billing.PlanPatch
		Scopes *[]string `json:"scopes,omitempty"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	patch := body.PlanPatch
	if strings.TrimSpace(patch.ID) == "" {
		patch.ID = req.Query.Get("id")
	}
	stored, errUpdate := a.store.UpdatePlanWithBindings(patch, body.Scopes)
	if errUpdate != nil {
		return errorResponse(errUpdate)
	}
	return JSONResponse(http.StatusOK, map[string]any{"plan": stored})
}

func (a *App) createModelGroup(req ManagementRequest) ManagementResponse {
	var group billing.ModelGroup
	if errDecode := decodeStrict(req.Body, &group); errDecode != nil {
		return errorResponse(errDecode)
	}
	stored, errCreate := a.store.CreateModelGroup(group)
	if errCreate != nil {
		return errorResponse(errCreate)
	}
	return JSONResponse(http.StatusCreated, map[string]any{"model_group": stored})
}

func (a *App) updateModelGroup(req ManagementRequest) ManagementResponse {
	var patch billing.ModelGroupPatch
	if errDecode := decodeStrict(req.Body, &patch); errDecode != nil {
		return errorResponse(errDecode)
	}
	if strings.TrimSpace(patch.ID) == "" {
		patch.ID = req.Query.Get("id")
	}
	stored, errUpdate := a.store.UpdateModelGroup(patch)
	if errUpdate != nil {
		return errorResponse(errUpdate)
	}
	return JSONResponse(http.StatusOK, map[string]any{"model_group": stored})
}

func (a *App) deleteModelGroup(req ManagementRequest) ManagementResponse {
	id := strings.TrimSpace(req.Query.Get("id"))
	released, errDelete := a.store.DeleteModelGroup(id)
	if errDelete != nil {
		return errorResponse(errDelete)
	}
	return JSONResponse(http.StatusOK, map[string]any{"deleted": id, "released_keys": released})
}

// An empty selection is the all-models grant, so a body naming neither groups
// nor models is a valid request rather than a missing one.
func (a *App) setKeyModels(req ManagementRequest) ManagementResponse {
	var body struct {
		Scope  string   `json:"scope"`
		Groups []string `json:"groups,omitempty"`
		Models []string `json:"models,omitempty"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	if errApply := a.store.SetKeyModels(body.Scope, body.Groups, body.Models); errApply != nil {
		return errorResponse(errApply)
	}
	return JSONResponse(http.StatusOK, struct{}{})
}

func (a *App) clearLogs() ManagementResponse {
	cleared, errClear := a.store.ClearLogs()
	if errClear != nil {
		return errorResponse(errClear)
	}
	return JSONResponse(http.StatusOK, map[string]any{"cleared": cleared})
}

func (a *App) listEvents() ManagementResponse {
	events, errEvents := a.store.Events()
	if errEvents != nil {
		return errorResponse(errEvents)
	}
	return JSONResponse(http.StatusOK, map[string]any{"events": events})
}

func (a *App) clearEvents() ManagementResponse {
	cleared, errClear := a.store.ClearEvents()
	if errClear != nil {
		return errorResponse(errClear)
	}
	return JSONResponse(http.StatusOK, map[string]any{"cleared": cleared})
}

func (a *App) deletePlan(req ManagementRequest) ManagementResponse {
	id := strings.TrimSpace(req.Query.Get("id"))
	unbound, errDelete := a.store.DeletePlan(id)
	if errDelete != nil {
		return errorResponse(errDelete)
	}
	return JSONResponse(http.StatusOK, map[string]any{"deleted": id, "unbound_keys": unbound})
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

func (a *App) resetKey(req ManagementRequest) ManagementResponse {
	var body struct {
		Scope string `json:"scope"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	if errReset := a.store.ResetCycle(body.Scope); errReset != nil {
		return errorResponse(errReset)
	}
	return JSONResponse(http.StatusOK, struct{}{})
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

// The plugin never fetches it: doing so would mean holding a management
// credential and a base URL, both of which are avoidable when the only client
// that needs this is already authenticated in the operator's browser. The
// plaintext is hashed into caller scopes and dropped; it is never persisted.
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
	if result.Added > 0 || result.Removed > 0 {
		a.store.Event(billing.EventInfo, "已同步 CLIProxyAPI 的 API Key 列表：新增 %d 个，移除 %d 个。",
			result.Added, result.Removed)
	}
	return JSONResponse(http.StatusOK, result)
}

func (a *App) listLogs(req ManagementRequest) ManagementResponse {
	query := billing.LogQuery{
		Search: req.Query.Get("q"),
		Status: strings.TrimSpace(req.Query.Get("status")),
		Limit:  defaultLogPageSize,
	}
	if !billing.ValidLogStatus(query.Status) {
		return JSONError(http.StatusBadRequest, "invalid", "status 无效："+query.Status)
	}
	if errOffset := countParam(req.Query, "offset", &query.Offset); errOffset != nil {
		return errorResponse(errOffset)
	}
	if errLimit := countParam(req.Query, "limit", &query.Limit); errLimit != nil {
		return errorResponse(errLimit)
	}
	if query.Limit < 1 || query.Limit > maxLogPageSize {
		return errorResponse(&billing.Error{Kind: billing.KindInvalid, Msg: "limit 必须是 1 到 1000 之间的整数"})
	}
	view, errLogs := a.store.Logs(query)
	if errLogs != nil {
		return errorResponse(errLogs)
	}
	return JSONResponse(http.StatusOK, view)
}

// An absent parameter leaves the target at whatever the query already means.
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

func decodeStrict(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(body)))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(target); errDecode != nil {
		return &billing.Error{Kind: billing.KindInvalid, Msg: fmt.Sprintf("请求内容无效：%v", errDecode)}
	}
	if errTrailing := decoder.Decode(&struct{}{}); errTrailing != io.EOF {
		return &billing.Error{Kind: billing.KindInvalid, Msg: "请求内容无效：只能包含一个 JSON 值"}
	}
	return nil
}

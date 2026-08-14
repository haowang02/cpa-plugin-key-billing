package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"cpa-key-billing/internal/billing"
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
	Stats  billing.StatsView  `json:"stats"`
}

func (a *App) overview() ManagementResponse {
	if _, errCatalog := billing.EnsureBuiltinCatalog(); errCatalog != nil {
		return errorResponse(errCatalog)
	}
	directory := a.store.KeyDirectory()
	return JSONResponse(http.StatusOK, overviewResponse{
		Status: a.store.Status(),
		Keys:   directory.Keys,
		Plans:  a.store.Plans(),
		Prices: a.store.PriceTable().Models,
		Stats:  billing.StatsFrom(directory),
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

func (a *App) clearLogs() ManagementResponse {
	return JSONResponse(http.StatusOK, map[string]any{"cleared": a.store.ClearLogs()})
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
		Keys []string `json:"keys"`
		// APIKeys mirrors the field name of CPA's GET /v0/management/api-keys
		// so the UI can forward that response verbatim.
		APIKeys    []string `json:"api-keys"`
		AllowEmpty bool     `json:"allow_empty"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	keys := append(body.Keys, body.APIKeys...)
	result, errSync := a.store.SyncKeys(keys, body.AllowEmpty)
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
		Search:  req.Query.Get("q"),
		Outcome: strings.TrimSpace(req.Query.Get("outcome")),
	}
	if !billing.ValidLogOutcome(query.Outcome) {
		return JSONError(http.StatusBadRequest, "invalid", "outcome 无效："+query.Outcome)
	}
	if errOffset := countParam(req.Query, "offset", &query.Offset); errOffset != nil {
		return errorResponse(errOffset)
	}
	if errLimit := countParam(req.Query, "limit", &query.Limit); errLimit != nil {
		return errorResponse(errLimit)
	}
	return JSONResponse(http.StatusOK, a.store.Logs(query))
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
	return nil
}

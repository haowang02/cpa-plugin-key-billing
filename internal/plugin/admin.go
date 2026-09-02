package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// The UI supplies /v1/models because the plugin has no model-list callback.
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

type accessResponse struct {
	Role        string               `json:"role"`
	Keys        []billing.KeyView    `json:"keys"`
	Plans       []billing.Plan       `json:"plans"`
	ModelGroups []billing.ModelGroup `json:"model_groups"`
}

func (a *App) access() ManagementResponse {
	return JSONResponse(http.StatusOK, accessResponse{
		Role:        "management",
		Keys:        a.store.KeyViews(),
		Plans:       a.store.Plans(),
		ModelGroups: a.store.ModelGroups(),
	})
}

func (a *App) refreshPriceCatalog() ManagementResponse {
	result, errRefresh := a.store.RefreshPriceCatalog()
	if errRefresh != nil {
		a.store.AddPluginLog(billing.PluginLogError, "更新 models.dev 参考价目录失败：%v", errRefresh)
		return errorResponse(errRefresh)
	}
	a.store.AddPluginLog(billing.PluginLogInfo, "已更新 models.dev 参考价目录，%d 条定价随之调整。", result.UpdatedModels)
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
	stored, errUpdate := a.store.UpdatePlanWithBindings(body.PlanPatch, body.Scopes)
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

func (a *App) listPluginLogs() ManagementResponse {
	entries, err := a.store.PluginLogs()
	if err != nil {
		return errorResponse(err)
	}
	return JSONResponse(http.StatusOK, map[string]any{"entries": entries})
}

func (a *App) clearPluginLogs() ManagementResponse {
	cleared, errClear := a.store.ClearPluginLogs()
	if errClear != nil {
		return errorResponse(errClear)
	}
	return JSONResponse(http.StatusOK, map[string]any{"cleared": cleared})
}

func (a *App) resetAllKeys() ManagementResponse {
	return JSONResponse(http.StatusOK, struct {
		Reset int `json:"reset"`
	}{Reset: a.store.ResetAllCycles()})
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
	if result.Added > 0 || result.Removed > 0 {
		a.store.AddPluginLog(billing.PluginLogInfo, "已同步 CLIProxyAPI 的 API Key 列表：新增 %d 个，移除 %d 个。",
			result.Added, result.Removed)
	}
	return JSONResponse(http.StatusOK, result)
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

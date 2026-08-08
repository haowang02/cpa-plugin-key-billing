package plugin

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"cpa-key-billing/internal/billing"
)

const (
	managementBase = "/v0/management/plugins/" + PluginID
	resourceBase   = "/v0/resource/plugins/" + PluginID
	resourceUIPath = "/ui"
)

//go:embed ui.html
var uiHTML []byte

const (
	routeStatus         = "/status"
	routePrices         = "/prices"
	routePriceCatalog   = "/prices/catalog"
	routeCatalogRefresh = "/prices/catalog/refresh"
	routePricesReset    = "/prices/reset"
	routePricesSync     = "/prices/sync"
	routePlans          = "/plans"
	routeKeys           = "/keys"
	routeKeysBind       = "/keys/bind"
	routeKeysUnbind     = "/keys/unbind"
	routeKeysReset      = "/keys/reset"
	routeKeysLabel      = "/keys/label"
	routeKeysSync       = "/keys/sync"
	routeStats          = "/stats"
	routeLogs           = "/logs"
	routeData           = "/data"
)

// managementRegistration declares every route this plugin owns.
//
// Management routes are authenticated by CPA and must be exact paths: the host
// rejects ':' and '*', so record identifiers travel in the query string or the
// body. The single resource route is what the panel renders as a sidebar entry.
func managementRegistration() ManagementRegistrationResponse {
	return ManagementRegistrationResponse{
		Routes: []ManagementRoute{
			{Method: http.MethodGet, Path: managementBase + routeStatus, Description: "查看运行状态和诊断计数。"},

			{Method: http.MethodGet, Path: managementBase + routePrices, Description: "查看模型定价。"},
			{Method: http.MethodGet, Path: managementBase + routePriceCatalog, Description: "搜索模型参考价。"},
			{Method: http.MethodPost, Path: managementBase + routeCatalogRefresh, Description: "从 models.dev 更新参考价目录。"},
			{Method: http.MethodPut, Path: managementBase + routePrices, Description: "更新模型定价。"},
			{Method: http.MethodPost, Path: managementBase + routePricesReset, Description: "恢复模型参考价。"},
			{Method: http.MethodPost, Path: managementBase + routePricesSync, Description: "同步代理模型。"},

			{Method: http.MethodGet, Path: managementBase + routePlans, Description: "查看订阅计划及其 Key 绑定数量。"},
			{Method: http.MethodPost, Path: managementBase + routePlans, Description: "新建订阅计划。"},
			{Method: http.MethodPatch, Path: managementBase + routePlans, Description: "更新订阅计划。"},
			{Method: http.MethodDelete, Path: managementBase + routePlans, Description: "删除订阅计划并解除相关 Key 的绑定。"},

			{Method: http.MethodGet, Path: managementBase + routeKeys, Description: "查看 API Key 的订阅、限额和用量。"},
			{Method: http.MethodDelete, Path: managementBase + routeKeys, Description: "删除指定 API Key 的全部计费数据。"},
			{Method: http.MethodPost, Path: managementBase + routeKeysBind, Description: "将 API Key 绑定到订阅计划。"},
			{Method: http.MethodPost, Path: managementBase + routeKeysUnbind, Description: "解除 API Key 的订阅计划。"},
			{Method: http.MethodPost, Path: managementBase + routeKeysReset, Description: "重置 API Key 的订阅额度。"},
			{Method: http.MethodPost, Path: managementBase + routeKeysLabel, Description: "设置 API Key 备注。"},
			{Method: http.MethodPost, Path: managementBase + routeKeysSync, Description: "同步 CLIProxyAPI 中的 API Key 列表。"},

			{Method: http.MethodGet, Path: managementBase + routeStats, Description: "查看全局用量汇总。"},
			{Method: http.MethodGet, Path: managementBase + routeLogs, Description: "查看最近的逐请求计费记录。"},
			{Method: http.MethodDelete, Path: managementBase + routeLogs, Description: "清空计费日志。"},
			{Method: http.MethodDelete, Path: managementBase + routeData, Description: "重新初始化插件数据。"},
		},
		Resources: []ResourceRoute{
			{Path: resourceBase + resourceUIPath, Menu: MenuLabel, Description: MenuDescription},
		},
	}
}

// handleManagement dispatches both authenticated Management API calls and the
// unauthenticated browser resource GETs, which CPA routes through this same
// method.
//
// Note that the host HTML-escapes every string in a JSON management response
// (internal/pluginhost/management.go). Values that survive a round trip through
// the UI must therefore be entity-decoded on read, or "A & B" becomes
// "A &amp;amp; B" after two saves.
func (a *App) handleManagement(raw []byte) ([]byte, error) {
	var req ManagementRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("解析管理请求：%w", errUnmarshal)
	}
	path := strings.TrimRight(req.Path, "/")
	if path == "" {
		path = req.Path
	}

	if req.Method == http.MethodGet && strings.HasPrefix(path, resourceBase) {
		return OKEnvelope(a.serveResource(strings.TrimPrefix(path, resourceBase)))
	}
	if !strings.HasPrefix(path, managementBase) {
		return OKEnvelope(JSONError(http.StatusNotFound, "not_found", "管理路由不存在："+req.Method+" "+req.Path))
	}
	return OKEnvelope(a.routeManagement(req, strings.TrimPrefix(path, managementBase)))
}

func (a *App) routeManagement(req ManagementRequest, suffix string) ManagementResponse {
	switch req.Method + " " + suffix {
	case http.MethodGet + " " + routeStatus:
		return JSONResponse(http.StatusOK, a.store.Status(PluginName, Version))

	case http.MethodGet + " " + routePrices:
		return a.priceTable()
	case http.MethodGet + " " + routePriceCatalog:
		return a.searchPriceCatalog(req)
	case http.MethodPost + " " + routeCatalogRefresh:
		return a.refreshPriceCatalog()
	case http.MethodPut + " " + routePrices:
		return a.putPrices(req)
	case http.MethodPost + " " + routePricesReset:
		return a.resetPrices()
	case http.MethodPost + " " + routePricesSync:
		return a.syncModels(req)

	case http.MethodGet + " " + routePlans:
		return JSONResponse(http.StatusOK, map[string]any{"plans": a.store.PlanViews()})
	case http.MethodPost + " " + routePlans:
		return a.createPlan(req)
	case http.MethodPatch + " " + routePlans:
		return a.updatePlan(req)
	case http.MethodDelete + " " + routePlans:
		return a.deletePlan(req)

	case http.MethodGet + " " + routeKeys:
		return JSONResponse(http.StatusOK, a.store.KeyDirectory())
	case http.MethodDelete + " " + routeKeys:
		return a.forgetKeys(req)
	case http.MethodPost + " " + routeKeysBind:
		return a.bindKeys(req)
	case http.MethodPost + " " + routeKeysUnbind:
		return a.unbindKeys(req)
	case http.MethodPost + " " + routeKeysReset:
		return a.resetKeys(req)
	case http.MethodPost + " " + routeKeysLabel:
		return a.labelKey(req)
	case http.MethodPost + " " + routeKeysSync:
		return a.syncKeys(req)

	case http.MethodGet + " " + routeStats:
		return JSONResponse(http.StatusOK, a.store.Stats())
	case http.MethodGet + " " + routeLogs:
		return a.listLogs(req)
	case http.MethodDelete + " " + routeLogs:
		return a.clearLogs()
	case http.MethodDelete + " " + routeData:
		return a.clearAllData()

	default:
		return JSONError(http.StatusNotFound, "not_found", "管理路由不存在："+req.Method+" "+req.Path)
	}
}

func (a *App) serveResource(suffix string) ManagementResponse {
	switch suffix {
	case "", "/", resourceUIPath:
		return ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":  []string{"text/html; charset=utf-8"},
				"Cache-Control": []string{"no-store"},
			},
			Body: uiHTML,
		}
	default:
		return JSONError(http.StatusNotFound, "not_found", "页面资源不存在："+suffix)
	}
}

// errorResponse maps a billing-domain failure onto an HTTP status. An
// unclassified error is a bug rather than bad input, so it reports 500.
func errorResponse(err error) ManagementResponse {
	switch billing.KindOf(err) {
	case billing.KindInvalid:
		return JSONError(http.StatusBadRequest, string(billing.KindInvalid), err.Error())
	case billing.KindNotFound:
		return JSONError(http.StatusNotFound, string(billing.KindNotFound), err.Error())
	case billing.KindConflict:
		return JSONError(http.StatusConflict, string(billing.KindConflict), err.Error())
	default:
		return JSONError(http.StatusInternalServerError, "internal_error", err.Error())
	}
}

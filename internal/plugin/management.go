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
	managementBase              = "/v0/management/plugins/" + PluginID
	resourceBase                = "/v0/resource/plugins/" + PluginID
	resourceUIPath              = "/ui"
	resourceAccountOverviewPath = "/account/overview"
	resourceAccountPricesPath   = "/account/prices"
	resourceAccountLogsPath     = "/account/logs"
)

//go:embed ui.html
var uiHTML []byte

const (
	routeOverview        = "/overview"
	routeStatus          = "/status"
	routePrices          = "/prices"
	routePriceCatalog    = "/prices/catalog"
	routeCatalogRefresh  = "/prices/catalog/refresh"
	routePricesReset     = "/prices/reset"
	routePricesSync      = "/prices/sync"
	routePlans           = "/plans"
	routeModelGroups     = "/model-groups"
	routeKeysModels      = "/keys/models"
	routeKeysBind        = "/keys/bind"
	routeKeysUnbind      = "/keys/unbind"
	routeKeysReset       = "/keys/reset"
	routeKeysResetAll    = "/keys/reset-all"
	routeKeysLabel       = "/keys/label"
	routeKeysConcurrency = "/keys/concurrency"
	routeKeysSync        = "/keys/sync"
	routeStats           = "/stats"
	routeLogs            = "/logs"
	routeEvents          = "/events"
)

// Management routes must be exact paths because the host rejects ':' and '*'.
// Only the UI resource declares a menu entry; account resources are direct
// read-only endpoints.
func managementRegistration() ManagementRegistrationResponse {
	return ManagementRegistrationResponse{
		Routes: []ManagementRoute{
			{Method: http.MethodGet, Path: managementBase + routeOverview, Description: "查看运行状态、API Key、订阅计划、模型定价和用量汇总。"},
			{Method: http.MethodGet, Path: managementBase + routeStatus, Description: "查看插件运行状态。"},

			{Method: http.MethodGet, Path: managementBase + routePriceCatalog, Description: "搜索模型参考价。"},
			{Method: http.MethodPost, Path: managementBase + routeCatalogRefresh, Description: "从 models.dev 更新参考价目录。"},
			{Method: http.MethodPut, Path: managementBase + routePrices, Description: "更新模型定价。"},
			{Method: http.MethodPost, Path: managementBase + routePricesReset, Description: "恢复模型参考价。"},
			{Method: http.MethodPost, Path: managementBase + routePricesSync, Description: "同步代理模型。"},

			{Method: http.MethodPost, Path: managementBase + routePlans, Description: "新建订阅计划。"},
			{Method: http.MethodPatch, Path: managementBase + routePlans, Description: "更新订阅计划。"},
			{Method: http.MethodDelete, Path: managementBase + routePlans, Description: "删除订阅计划并解除相关 Key 的绑定。"},

			{Method: http.MethodPost, Path: managementBase + routeModelGroups, Description: "新建模型分组。"},
			{Method: http.MethodPatch, Path: managementBase + routeModelGroups, Description: "更新模型分组。"},
			{Method: http.MethodDelete, Path: managementBase + routeModelGroups, Description: "删除模型分组并解除相关 Key 的绑定。"},

			{Method: http.MethodPost, Path: managementBase + routeKeysModels, Description: "设置 API Key 可用的模型分组和模型。"},
			{Method: http.MethodPost, Path: managementBase + routeKeysBind, Description: "将 API Key 绑定到订阅计划。"},
			{Method: http.MethodPost, Path: managementBase + routeKeysUnbind, Description: "解除 API Key 的订阅计划。"},
			{Method: http.MethodPost, Path: managementBase + routeKeysReset, Description: "重置 API Key 的订阅额度。"},
			{Method: http.MethodPost, Path: managementBase + routeKeysResetAll, Description: "重置所有周期性计划 API Key 的订阅额度。"},
			{Method: http.MethodPost, Path: managementBase + routeKeysLabel, Description: "设置 API Key 备注。"},
			{Method: http.MethodPost, Path: managementBase + routeKeysConcurrency, Description: "设置 API Key 最大并发请求数。"},
			{Method: http.MethodPost, Path: managementBase + routeKeysSync, Description: "同步 CLIProxyAPI 中的 API Key 列表。"},

			{Method: http.MethodGet, Path: managementBase + routeStats, Description: "查看全局用量汇总。"},
			{Method: http.MethodGet, Path: managementBase + routeLogs, Description: "分页查看用量计费记录。"},
			{Method: http.MethodDelete, Path: managementBase + routeLogs, Description: "清空计费日志。"},
			{Method: http.MethodGet, Path: managementBase + routeEvents, Description: "查看插件运行日志。"},
			{Method: http.MethodDelete, Path: managementBase + routeEvents, Description: "清空插件运行日志。"},
		},
		Resources: []ResourceRoute{
			{Path: resourceBase + resourceUIPath, Menu: MenuLabel, Description: MenuDescription},
			{Path: resourceBase + resourceAccountOverviewPath},
			{Path: resourceBase + resourceAccountPricesPath},
			{Path: resourceBase + resourceAccountLogsPath},
		},
	}
}

type pluginStatus struct {
	Enabled bool `json:"enabled"`
}

// CPA routes authenticated Management calls and unauthenticated resource GETs
// through this same method.
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

	if req.Method == http.MethodGet && path == resourceBase+resourceUIPath {
		return OKEnvelope(ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":           []string{"text/html; charset=utf-8"},
				"Cache-Control":          []string{"private, no-store"},
				"Pragma":                 []string{"no-cache"},
				"Referrer-Policy":        []string{"no-referrer"},
				"X-Content-Type-Options": []string{"nosniff"},
				"Content-Security-Policy": []string{
					"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; " +
						"img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'",
				},
			},
			Body: uiHTML,
		})
	}
	if req.Method == http.MethodGet && path == resourceBase+resourceAccountOverviewPath {
		return OKEnvelope(a.accountOverview(req))
	}
	if req.Method == http.MethodGet && path == resourceBase+resourceAccountPricesPath {
		return OKEnvelope(a.accountPrices(req))
	}
	if req.Method == http.MethodGet && path == resourceBase+resourceAccountLogsPath {
		return OKEnvelope(a.accountLogs(req))
	}
	if path != managementBase && !strings.HasPrefix(path, managementBase+"/") {
		return OKEnvelope(JSONError(http.StatusNotFound, "not_found", "管理路由不存在："+req.Method+" "+req.Path))
	}
	return OKEnvelope(a.routeManagement(req, strings.TrimPrefix(path, managementBase)))
}

func (a *App) routeManagement(req ManagementRequest, suffix string) ManagementResponse {
	switch req.Method + " " + suffix {
	case http.MethodGet + " " + routeOverview:
		return a.overview()
	case http.MethodGet + " " + routeStatus:
		return JSONResponse(http.StatusOK, pluginStatus{Enabled: a.store.Enabled()})

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

	case http.MethodPost + " " + routePlans:
		return a.createPlan(req)
	case http.MethodPatch + " " + routePlans:
		return a.updatePlan(req)
	case http.MethodDelete + " " + routePlans:
		return a.deletePlan(req)

	case http.MethodPost + " " + routeModelGroups:
		return a.createModelGroup(req)
	case http.MethodPatch + " " + routeModelGroups:
		return a.updateModelGroup(req)
	case http.MethodDelete + " " + routeModelGroups:
		return a.deleteModelGroup(req)

	case http.MethodPost + " " + routeKeysModels:
		return a.setKeyModels(req)
	case http.MethodPost + " " + routeKeysBind:
		return a.bindKey(req)
	case http.MethodPost + " " + routeKeysUnbind:
		return a.unbindKey(req)
	case http.MethodPost + " " + routeKeysReset:
		return a.resetKey(req)
	case http.MethodPost + " " + routeKeysResetAll:
		return JSONResponse(http.StatusOK, struct {
			Reset int `json:"reset"`
		}{Reset: a.store.ResetAllCycles()})
	case http.MethodPost + " " + routeKeysLabel:
		return a.labelKey(req)
	case http.MethodPost + " " + routeKeysConcurrency:
		return a.setKeyConcurrency(req)
	case http.MethodPost + " " + routeKeysSync:
		return a.syncKeys(req)

	case http.MethodGet + " " + routeStats:
		return JSONResponse(http.StatusOK, a.store.Stats())
	case http.MethodGet + " " + routeLogs:
		return a.listLogs(req)
	case http.MethodDelete + " " + routeLogs:
		return a.clearLogs()
	case http.MethodGet + " " + routeEvents:
		return a.listEvents()
	case http.MethodDelete + " " + routeEvents:
		return a.clearEvents()
	default:
		return JSONError(http.StatusNotFound, "not_found", "管理路由不存在："+req.Method+" "+req.Path)
	}
}

// An unclassified domain error is a bug rather than bad input, so it reports 500.
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

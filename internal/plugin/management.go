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
	routeStatus          = "/status"
	routeAccess          = "/access"
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
	routeEvents          = "/events"
	routeErrors          = "/errors"
	routeAnalysis        = "/analysis"
	routePluginLogs      = "/plugin-logs"
	routeAuthFiles       = "/auth-files"
	routeAuthQuota       = "/auth-files/quota"
)

type managementEndpoint struct {
	method, path, description string
	handle                    func(*App, ManagementRequest) ManagementResponse
}

var managementEndpoints = []managementEndpoint{
	{http.MethodGet, routeStatus, "查看插件运行状态。", func(a *App, _ ManagementRequest) ManagementResponse {
		return JSONResponse(http.StatusOK, pluginStatus{Role: "management", Enabled: a.store.Enabled()})
	}},
	{http.MethodGet, routeAccess, "查看 API Key、订阅计划和模型分组。", func(a *App, _ ManagementRequest) ManagementResponse { return a.access() }},
	{http.MethodGet, routePrices, "查看模型定价。", func(a *App, _ ManagementRequest) ManagementResponse { return a.listPrices(viewAccess{}) }},
	{http.MethodGet, routePriceCatalog, "搜索模型参考价。", func(a *App, req ManagementRequest) ManagementResponse { return a.searchPriceCatalog(req) }},
	{http.MethodPost, routeCatalogRefresh, "从 models.dev 更新参考价目录。", func(a *App, _ ManagementRequest) ManagementResponse { return a.refreshPriceCatalog() }},
	{http.MethodPut, routePrices, "更新模型定价。", func(a *App, req ManagementRequest) ManagementResponse { return a.putPrices(req) }},
	{http.MethodPost, routePricesReset, "恢复模型参考价。", func(a *App, _ ManagementRequest) ManagementResponse { return a.resetPrices() }},
	{http.MethodPost, routePricesSync, "同步代理模型。", func(a *App, req ManagementRequest) ManagementResponse { return a.syncModels(req) }},
	{http.MethodPost, routePlans, "新建订阅计划。", func(a *App, req ManagementRequest) ManagementResponse { return a.createPlan(req) }},
	{http.MethodPatch, routePlans, "更新订阅计划。", func(a *App, req ManagementRequest) ManagementResponse { return a.updatePlan(req) }},
	{http.MethodDelete, routePlans, "删除订阅计划并解除相关 Key 的绑定。", func(a *App, req ManagementRequest) ManagementResponse { return a.deletePlan(req) }},
	{http.MethodPost, routeModelGroups, "新建模型分组。", func(a *App, req ManagementRequest) ManagementResponse { return a.createModelGroup(req) }},
	{http.MethodPatch, routeModelGroups, "更新模型分组。", func(a *App, req ManagementRequest) ManagementResponse { return a.updateModelGroup(req) }},
	{http.MethodDelete, routeModelGroups, "删除模型分组并解除相关 Key 的绑定。", func(a *App, req ManagementRequest) ManagementResponse { return a.deleteModelGroup(req) }},
	{http.MethodPost, routeKeysModels, "设置 API Key 可用的模型分组和模型。", func(a *App, req ManagementRequest) ManagementResponse { return a.setKeyModels(req) }},
	{http.MethodPost, routeKeysBind, "将 API Key 绑定到订阅计划。", func(a *App, req ManagementRequest) ManagementResponse { return a.bindKey(req) }},
	{http.MethodPost, routeKeysUnbind, "解除 API Key 的订阅计划。", func(a *App, req ManagementRequest) ManagementResponse { return a.unbindKey(req) }},
	{http.MethodPost, routeKeysReset, "重置 API Key 的订阅额度。", func(a *App, req ManagementRequest) ManagementResponse { return a.resetKey(req) }},
	{http.MethodPost, routeKeysResetAll, "重置所有周期性计划 API Key 的订阅额度。", func(a *App, _ ManagementRequest) ManagementResponse { return a.resetAllKeys() }},
	{http.MethodPost, routeKeysLabel, "设置 API Key 备注。", func(a *App, req ManagementRequest) ManagementResponse { return a.labelKey(req) }},
	{http.MethodPost, routeKeysConcurrency, "设置 API Key 最大并发请求数。", func(a *App, req ManagementRequest) ManagementResponse { return a.setKeyConcurrency(req) }},
	{http.MethodPost, routeKeysSync, "同步 CLIProxyAPI 中的 API Key 列表。", func(a *App, req ManagementRequest) ManagementResponse { return a.syncKeys(req) }},
	{http.MethodGet, routeEvents, "分页查看请求事件。", func(a *App, req ManagementRequest) ManagementResponse {
		return a.listRequestEvents(req, viewAccess{})
	}},
	{http.MethodGet, routeErrors, "分页查看错误事件。", func(a *App, req ManagementRequest) ManagementResponse {
		return a.listRequestErrors(req, viewAccess{})
	}},
	{http.MethodGet, routeAnalysis, "查看用量分布。", func(a *App, req ManagementRequest) ManagementResponse { return a.analysis(req, viewAccess{}) }},
	{http.MethodGet, routePluginLogs, "查看插件运行日志。", func(a *App, _ ManagementRequest) ManagementResponse { return a.listPluginLogs() }},
	{http.MethodDelete, routePluginLogs, "清空插件运行日志。", func(a *App, _ ManagementRequest) ManagementResponse { return a.clearPluginLogs() }},
	{http.MethodGet, routeAuthFiles, "查看认证文件。", func(a *App, _ ManagementRequest) ManagementResponse { return a.authFiles(viewAccess{}) }},
	{http.MethodGet, routeAuthQuota, "按需查看认证文件限额。", func(a *App, req ManagementRequest) ManagementResponse { return a.authQuota(req, viewAccess{}) }},
}

type resourceEndpoint struct {
	path   string
	handle func(*App, ManagementRequest, viewAccess) ManagementResponse
}

var resourceEndpoints = []resourceEndpoint{
	{routeStatus, func(a *App, _ ManagementRequest, access viewAccess) ManagementResponse {
		return a.accountStatus(access)
	}},
	{routeAccess, func(a *App, _ ManagementRequest, access viewAccess) ManagementResponse {
		return a.accountAccess(access)
	}},
	{routePrices, func(a *App, _ ManagementRequest, access viewAccess) ManagementResponse { return a.listPrices(access) }},
	{routeAnalysis, func(a *App, req ManagementRequest, access viewAccess) ManagementResponse {
		return a.analysis(req, access)
	}},
	{routeEvents, func(a *App, req ManagementRequest, access viewAccess) ManagementResponse {
		return a.listRequestEvents(req, access)
	}},
	{routeErrors, func(a *App, req ManagementRequest, access viewAccess) ManagementResponse {
		return a.listRequestErrors(req, access)
	}},
	{routeAuthFiles, func(a *App, _ ManagementRequest, access viewAccess) ManagementResponse { return a.authFiles(access) }},
	{routeAuthQuota, func(a *App, req ManagementRequest, access viewAccess) ManagementResponse {
		return a.authQuota(req, access)
	}},
}

func managementRegistration() ManagementRegistrationResponse {
	registration := ManagementRegistrationResponse{
		Routes:    make([]ManagementRoute, 0, len(managementEndpoints)),
		Resources: make([]ResourceRoute, 1, len(resourceEndpoints)+1),
	}
	registration.Resources[0] = ResourceRoute{Path: resourceBase + resourceUIPath, Menu: MenuLabel, Description: MenuDescription}
	for _, endpoint := range managementEndpoints {
		registration.Routes = append(registration.Routes, ManagementRoute{
			Method: endpoint.method, Path: managementBase + endpoint.path, Description: endpoint.description,
		})
	}
	for _, endpoint := range resourceEndpoints {
		registration.Resources = append(registration.Resources, ResourceRoute{Path: resourceBase + endpoint.path})
	}
	return registration
}

type pluginStatus struct {
	Role    string `json:"role"`
	Enabled bool   `json:"enabled"`
}

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
					"default-src 'none'; script-src 'unsafe-inline' https://cdn.jsdelivr.net; style-src 'unsafe-inline'; connect-src 'self'; " +
						"img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'",
				},
			},
			Body: uiHTML,
		})
	}
	if path != resourceBase && strings.HasPrefix(path, resourceBase+"/") {
		return OKEnvelope(a.routeResource(req, strings.TrimPrefix(path, resourceBase)))
	}
	if path != managementBase && !strings.HasPrefix(path, managementBase+"/") {
		return OKEnvelope(JSONError(http.StatusNotFound, "not_found", "管理路由不存在："+req.Method+" "+req.Path))
	}
	return OKEnvelope(a.routeManagement(req, strings.TrimPrefix(path, managementBase)))
}

func (a *App) routeManagement(req ManagementRequest, suffix string) ManagementResponse {
	for _, endpoint := range managementEndpoints {
		if req.Method == endpoint.method && suffix == endpoint.path {
			return endpoint.handle(a, req)
		}
	}
	return JSONError(http.StatusNotFound, "not_found", "管理路由不存在："+req.Method+" "+req.Path)
}

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

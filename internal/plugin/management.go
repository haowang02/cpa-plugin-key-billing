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
	routeRoutes          = "/routes"
	routeKeysRoutes      = "/keys/routes"
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
	{http.MethodGet, routeAccess, "查看 API Key、订阅计划和路由规则。", func(a *App, _ ManagementRequest) ManagementResponse { return a.access() }},
	{http.MethodGet, routePrices, "查看模型定价。", func(a *App, _ ManagementRequest) ManagementResponse { return a.listPrices(viewAccess{}) }},
	{http.MethodGet, routePriceCatalog, "搜索模型参考价。", (*App).searchPriceCatalog},
	{http.MethodPost, routeCatalogRefresh, "从 models.dev 更新参考价目录。", func(a *App, _ ManagementRequest) ManagementResponse { return a.refreshPriceCatalog() }},
	{http.MethodPut, routePrices, "更新模型定价。", (*App).putPrices},
	{http.MethodPost, routePricesReset, "恢复模型参考价。", func(a *App, _ ManagementRequest) ManagementResponse { return a.resetPrices() }},
	{http.MethodPost, routePricesSync, "同步模型价格目录。", (*App).syncPriceCatalog},
	{http.MethodPost, routePlans, "新建订阅计划。", (*App).createPlan},
	{http.MethodPatch, routePlans, "更新订阅计划。", (*App).updatePlan},
	{http.MethodDelete, routePlans, "删除订阅计划并解除相关 Key 的绑定。", (*App).deletePlan},
	{http.MethodPost, routeRoutes, "新建路由规则。", (*App).createRoute},
	{http.MethodPatch, routeRoutes, "更新路由规则。", (*App).updateRoute},
	{http.MethodDelete, routeRoutes, "删除路由规则并移除相关绑定。", (*App).deleteRoute},
	{http.MethodPut, routeKeysRoutes, "替换一个 API Key 的路由绑定。", (*App).setKeyRoutes},
	{http.MethodPost, routeKeysBind, "将 API Key 绑定到订阅计划。", (*App).bindKey},
	{http.MethodPost, routeKeysUnbind, "解除 API Key 的订阅计划。", (*App).unbindKey},
	{http.MethodPost, routeKeysReset, "重置 API Key 的订阅额度。", (*App).resetKey},
	{http.MethodPost, routeKeysResetAll, "重置所有周期性计划 API Key 的订阅额度。", func(a *App, _ ManagementRequest) ManagementResponse { return a.resetAllKeys() }},
	{http.MethodPost, routeKeysLabel, "设置 API Key 备注。", (*App).labelKey},
	{http.MethodPost, routeKeysConcurrency, "设置 API Key 最大并发请求数。", (*App).setKeyConcurrency},
	{http.MethodPost, routeKeysSync, "同步 CLIProxyAPI 中的 API Key 列表。", (*App).syncKeys},
	{http.MethodGet, routeEvents, "分页查看请求事件。", func(a *App, req ManagementRequest) ManagementResponse {
		return a.listRequestEvents(req, viewAccess{})
	}},
	{http.MethodGet, routeErrors, "分页查看错误事件。", func(a *App, req ManagementRequest) ManagementResponse {
		return a.listRequestErrors(req, viewAccess{})
	}},
	{http.MethodGet, routeAnalysis, "查看用量分布。", func(a *App, req ManagementRequest) ManagementResponse { return a.analysis(req, viewAccess{}) }},
	{http.MethodGet, routePluginLogs, "分页查看插件运行日志。", (*App).listPluginLogs},
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
	{routeAnalysis, (*App).analysis},
	{routeEvents, (*App).listRequestEvents},
	{routeErrors, (*App).listRequestErrors},
	{routeAuthFiles, func(a *App, _ ManagementRequest, access viewAccess) ManagementResponse { return a.authFiles(access) }},
	{routeAuthQuota, (*App).authQuota},
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
					"default-src 'none'; script-src 'unsafe-inline' https://cdn.jsdelivr.net; style-src 'unsafe-inline' https://cdn.jsdelivr.net; " +
						"font-src https://cdn.jsdelivr.net; connect-src 'self'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'",
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

package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Mutation views use local state, never host discovery.
func (a *App) mutateWithView(req ManagementRequest, path string, handle func(*App, ManagementRequest) ManagementResponse) ManagementResponse {
	var input struct {
		Data   json.RawMessage `json:"data"`
		Models []string        `json:"models,omitempty"`
	}
	if err := decodeStrict(req.Body, &input); err != nil {
		return errorResponse(err)
	}
	for _, model := range input.Models {
		if len(strings.TrimSpace(model)) > 1024 {
			return JSONError(http.StatusBadRequest, "invalid", "Model ID is too long")
		}
	}
	req.Body = input.Data
	response := handle(a, req)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response
	}
	view := map[string]any{}
	switch path {
	case routePlans, routeRoutes, routeKeysBind, routeKeysUnbind, routeKeysReset, routeKeysTemporaryQuota, routeKeysLabel, routeKeysConcurrency, routeKeysRoutes:
		view["keys"] = a.keyRows()
		if path == routePlans {
			view["plans"] = a.store.Plans()
		}
		if path == routeRoutes || path == routeKeysRoutes {
			view["routes"] = a.routeRows()
		}
	case routePrices, routeReferencePricesRefresh:
		prices, err := a.store.ModelPriceRows(input.Models, true)
		if err != nil {
			// The write has committed. A display-read failure must not turn it into
			// a reported write failure or invite the caller to repeat the mutation.
			view["error"] = err.Error()
		} else {
			view["prices"] = prices
		}
		view["metadata"] = a.store.ReferencePriceMetadata()
	case routePluginLogs:
		view["logs_cleared"] = true
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(response.Body, &result); err != nil || result == nil {
		return response
	}
	result["view"], _ = json.Marshal(view)
	return JSONResponse(response.StatusCode, result)
}

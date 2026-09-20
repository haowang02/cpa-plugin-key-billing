package plugin

import (
	"net/http"

	"cpa-key-billing/internal/billing"
)

func (a *App) setTemporaryQuota(req ManagementRequest) ManagementResponse {
	var body billing.TemporaryQuotaRequest
	if err := decodeStrict(req.Body, &body); err != nil {
		return errorResponse(err)
	}
	window, err := a.store.SetTemporaryQuota(body)
	if err != nil {
		return errorResponse(err)
	}
	return JSONResponse(http.StatusOK, map[string]any{"window": window})
}

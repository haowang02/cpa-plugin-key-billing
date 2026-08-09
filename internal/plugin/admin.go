package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// syncModels receives the model list the admin UI reads from the proxy, so the
// price table always lines up with what clients can actually ask for.
//
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

func (a *App) priceTable() ManagementResponse {
	if _, errCatalog := billing.EnsureBuiltinCatalog(); errCatalog != nil {
		return errorResponse(errCatalog)
	}
	return JSONResponse(http.StatusOK, a.store.PriceTable())
}

func (a *App) refreshPriceCatalog() ManagementResponse {
	result, errRefresh := a.store.RefreshPriceCatalog()
	if errRefresh != nil {
		return errorResponse(errRefresh)
	}
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

func (a *App) bindKeys(req ManagementRequest) ManagementResponse {
	var body struct {
		Scope  string `json:"scope"`
		PlanID string `json:"plan_id"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	bound, errBind := a.store.BindKeys([]string{body.Scope}, body.PlanID)
	if errBind != nil {
		return errorResponse(errBind)
	}
	return JSONResponse(http.StatusOK, map[string]any{"bound": bound, "plan_id": strings.TrimSpace(body.PlanID)})
}

func (a *App) unbindKeys(req ManagementRequest) ManagementResponse {
	var body struct {
		Scope string `json:"scope"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	unbound, errUnbind := a.store.UnbindKeys([]string{body.Scope})
	if errUnbind != nil {
		return errorResponse(errUnbind)
	}
	return JSONResponse(http.StatusOK, map[string]any{"unbound": unbound})
}

func (a *App) resetKeys(req ManagementRequest) ManagementResponse {
	var body struct {
		Scope string `json:"scope"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	reset, errReset := a.store.ResetCycles([]string{body.Scope})
	if errReset != nil {
		return errorResponse(errReset)
	}
	return JSONResponse(http.StatusOK, map[string]any{"reset": reset})
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
	return JSONResponse(http.StatusOK, map[string]any{"scope": strings.TrimSpace(body.Scope), "label": strings.TrimSpace(body.Label)})
}

// syncKeys receives the plaintext key list the admin UI reads from CPA's own
// Management API on the same origin.
//
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
	return JSONResponse(http.StatusOK, result)
}

func (a *App) listLogs(req ManagementRequest) ManagementResponse {
	limit := 0
	if raw := strings.TrimSpace(req.Query.Get("limit")); raw != "" {
		if parsed, errParse := strconv.Atoi(raw); errParse == nil && parsed > 0 {
			limit = parsed
		}
	}
	return JSONResponse(http.StatusOK, a.store.Logs(limit))
}

func decodeStrict(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(body)))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(target); errDecode != nil {
		return &billing.Error{Kind: billing.KindInvalid, Msg: fmt.Sprintf("请求内容无效：%v", errDecode)}
	}
	return nil
}

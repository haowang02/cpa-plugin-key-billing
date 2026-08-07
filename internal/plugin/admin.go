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

// scopeSelector is the shared body shape of the key mutation routes. Both
// spellings are accepted so a single-key action does not have to wrap its
// argument in an array.
type scopeSelector struct {
	Scope  string   `json:"scope,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
}

func (s scopeSelector) all() []string {
	scopes := make([]string, 0, len(s.Scopes)+1)
	if strings.TrimSpace(s.Scope) != "" {
		scopes = append(scopes, s.Scope)
	}
	return append(scopes, s.Scopes...)
}

// putPrices accepts either one model's price or a whole table.
//
// A row editor saves one and a bulk import saves many; both shapes are natural
// to write, so both are accepted rather than forcing the caller to adapt.
func (a *App) putPrices(req ManagementRequest) ManagementResponse {
	body := bytes.TrimSpace(req.Body)
	if len(body) == 0 {
		return JSONError(http.StatusBadRequest, "invalid", "请提供一条模型价格或完整价格列表")
	}

	if body[0] == '[' {
		var rules []billing.PriceRule
		if errDecode := decodeStrict(body, &rules); errDecode != nil {
			return errorResponse(errDecode)
		}
		stored, errReplace := a.store.ReplacePrices(rules)
		if errReplace != nil {
			return errorResponse(errReplace)
		}
		return JSONResponse(http.StatusOK, map[string]any{"replaced": len(stored)})
	}

	var wrapper struct {
		Rules []billing.PriceRule `json:"rules"`
	}
	if errDecode := json.Unmarshal(body, &wrapper); errDecode == nil && wrapper.Rules != nil {
		stored, errReplace := a.store.ReplacePrices(wrapper.Rules)
		if errReplace != nil {
			return errorResponse(errReplace)
		}
		return JSONResponse(http.StatusOK, map[string]any{"replaced": len(stored)})
	}

	var rule billing.PriceRule
	if errDecode := decodeStrict(body, &rule); errDecode != nil {
		return errorResponse(errDecode)
	}
	stored, errUpsert := a.store.UpsertPrice(rule)
	if errUpsert != nil {
		return errorResponse(errUpsert)
	}
	return JSONResponse(http.StatusOK, map[string]any{"price": stored})
}

func (a *App) searchPriceCatalog(req ManagementRequest) ManagementResponse {
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

func (a *App) createPlan(req ManagementRequest) ManagementResponse {
	var plan billing.Plan
	if errDecode := decodeStrict(req.Body, &plan); errDecode != nil {
		return errorResponse(errDecode)
	}
	stored, errCreate := a.store.CreatePlan(plan)
	if errCreate != nil {
		return errorResponse(errCreate)
	}
	return JSONResponse(http.StatusCreated, map[string]any{"plan": stored})
}

func (a *App) updatePlan(req ManagementRequest) ManagementResponse {
	var patch billing.PlanPatch
	if errDecode := decodeStrict(req.Body, &patch); errDecode != nil {
		return errorResponse(errDecode)
	}
	if strings.TrimSpace(patch.ID) == "" {
		patch.ID = req.Query.Get("id")
	}
	stored, errUpdate := a.store.UpdatePlan(patch)
	if errUpdate != nil {
		return errorResponse(errUpdate)
	}
	return JSONResponse(http.StatusOK, map[string]any{"plan": stored})
}

func (a *App) deletePlan(req ManagementRequest) ManagementResponse {
	id := strings.TrimSpace(req.Query.Get("id"))
	if id == "" {
		var body struct {
			ID string `json:"id"`
		}
		if len(bytes.TrimSpace(req.Body)) > 0 {
			if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
				return errorResponse(errDecode)
			}
		}
		id = strings.TrimSpace(body.ID)
	}
	unbound, errDelete := a.store.DeletePlan(id)
	if errDelete != nil {
		return errorResponse(errDelete)
	}
	return JSONResponse(http.StatusOK, map[string]any{"deleted": id, "unbound_keys": unbound})
}

func (a *App) bindKeys(req ManagementRequest) ManagementResponse {
	var body struct {
		scopeSelector
		PlanID string `json:"plan_id"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	bound, errBind := a.store.BindKeys(body.all(), body.PlanID)
	if errBind != nil {
		return errorResponse(errBind)
	}
	return JSONResponse(http.StatusOK, map[string]any{"bound": bound, "plan_id": strings.TrimSpace(body.PlanID)})
}

func (a *App) unbindKeys(req ManagementRequest) ManagementResponse {
	scopes, errDecode := decodeScopes(req)
	if errDecode != nil {
		return errorResponse(errDecode)
	}
	unbound, errUnbind := a.store.UnbindKeys(scopes)
	if errUnbind != nil {
		return errorResponse(errUnbind)
	}
	return JSONResponse(http.StatusOK, map[string]any{"unbound": unbound})
}

func (a *App) resetKeys(req ManagementRequest) ManagementResponse {
	scopes, errDecode := decodeScopes(req)
	if errDecode != nil {
		return errorResponse(errDecode)
	}
	reset, errReset := a.store.ResetCycles(scopes)
	if errReset != nil {
		return errorResponse(errReset)
	}
	return JSONResponse(http.StatusOK, map[string]any{"reset": reset})
}

func (a *App) forgetKeys(req ManagementRequest) ManagementResponse {
	scopes, errDecode := decodeScopes(req)
	if errDecode != nil {
		return errorResponse(errDecode)
	}
	if scope := strings.TrimSpace(req.Query.Get("scope")); scope != "" {
		scopes = append(scopes, scope)
	}
	removed, errForget := a.store.ForgetKeys(scopes)
	if errForget != nil {
		return errorResponse(errForget)
	}
	return JSONResponse(http.StatusOK, map[string]any{"forgotten": removed})
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
	keys := append(append([]string{}, body.Keys...), body.APIKeys...)
	result, errSync := a.store.SyncKeys(keys, body.AllowEmpty)
	if errSync != nil {
		return errorResponse(errSync)
	}
	return JSONResponse(http.StatusOK, result)
}

// listLogs answers the billing log route. ?limit= trims the response to the
// newest N entries; anything unparseable is treated as absent, because a bad
// limit is a reason to show the whole log rather than to fail the page that
// asked for it.
func (a *App) listLogs(req ManagementRequest) ManagementResponse {
	limit := 0
	if raw := strings.TrimSpace(req.Query.Get("limit")); raw != "" {
		if parsed, errParse := strconv.Atoi(raw); errParse == nil && parsed > 0 {
			limit = parsed
		}
	}
	return JSONResponse(http.StatusOK, a.store.Logs(limit))
}

func decodeScopes(req ManagementRequest) ([]string, error) {
	var selector scopeSelector
	if len(bytes.TrimSpace(req.Body)) > 0 {
		if errDecode := decodeStrict(req.Body, &selector); errDecode != nil {
			return nil, errDecode
		}
	}
	return selector.all(), nil
}

// decodeStrict parses a request body and rejects unknown fields, so a typo in
// an admin call fails loudly instead of silently doing half of what was meant.
func decodeStrict(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(body)))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(target); errDecode != nil {
		return &billing.Error{Kind: billing.KindInvalid, Msg: fmt.Sprintf("请求内容无效：%v", errDecode)}
	}
	return nil
}

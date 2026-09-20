package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func temporaryQuotaApp(t *testing.T) (*App, billing.TemporaryQuotaRequest) {
	t.Helper()
	app := configuredAccountApp(t)
	scope := billing.CallerScope(accountTestKeyA)
	plan, err := app.store.CreatePlanWithBindings(billing.Plan{ID: "credit", Name: "临时额度测试", Windows: []billing.QuotaWindow{{
		Name: "周限", AmountUSD: 600, TokenLimit: 10000, RequestLimit: 100,
		PeriodSeconds: 604800, CycleAnchorAt: time.Now().UTC().Truncate(time.Second).Add(24 * time.Hour),
	}}}, []string{scope, billing.CallerScope(accountTestKeyB)})
	if err != nil {
		t.Fatal(err)
	}
	view, _ := app.store.KeyViewForScope(scope)
	return app, billing.TemporaryQuotaRequest{Scope: scope, PlanID: plan.ID, WindowID: plan.Windows[0].ID,
		Revision: view.Windows[0].CreditRevision, Quota: &billing.TemporaryQuota{AmountUSD: 100, TokenLimit: 2000, RequestLimit: 20}}
}

func TestTemporaryQuotaManagementAndAccountViews(t *testing.T) {
	app, req := temporaryQuotaApp(t)
	var result struct {
		Window billing.QuotaWindowView `json:"window"`
		View   struct {
			Keys []billing.KeyView `json:"keys"`
		} `json:"view"`
	}
	callOK(t, app, http.MethodPut, routeKeysTemporaryQuota, url.Values{"view": {"1"}}, map[string]any{"data": req}, http.StatusOK, &result)
	if len(result.View.Keys) != 2 || result.Window.Dimensions[0].Limit != "700" || result.Window.CreditRevision == req.Revision {
		t.Fatalf("mutation view missing updated quota: %+v", result)
	}
	for _, test := range []struct{ key, limit string }{{accountTestKeyA, "700"}, {accountTestKeyB, "600"}} {
		response := callAccount(t, app, routeSubscription, test.key, nil)
		var account accountSubscriptionResponse
		if err := json.Unmarshal(response.Body, &account); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || account.Subscription.Windows[0].Dimensions[0].Limit != json.Number(test.limit) {
			t.Fatalf("account credit isolation failed: %s", response.Body)
		}
	}
	if response := callManagement(t, app, http.MethodPut, routeKeysTemporaryQuota, nil, req); response.StatusCode != http.StatusConflict {
		t.Fatalf("stale edit status = %d", response.StatusCode)
	}
	req.Revision = result.Window.CreditRevision
	req.Quota = &billing.TemporaryQuota{}
	result.Window = billing.QuotaWindowView{}
	callOK(t, app, http.MethodPut, routeKeysTemporaryQuota, nil, req, http.StatusOK, &result)
	if result.Window.Dimensions[0].Limit != "600" || result.Window.Dimensions[0].TemporaryLimit != "" {
		t.Fatal("revocation did not restore the base limit")
	}
}

func TestTemporaryQuotaStrictInputAndResourceIsolation(t *testing.T) {
	app, req := temporaryQuotaApp(t)
	valid := string(mustMarshal(t, req))
	for _, body := range []string{
		`{}`, valid + `{}`, strings.Replace(valid, `"quota":{`, `"quota":{"unexpected":1,`, 1),
		strings.Replace(valid, `"amount_usd":100`, `"amount_usd":-1`, 1),
		strings.Replace(valid, `"token_limit":2000`, `"token_limit":1.5`, 1),
		strings.Replace(valid, `"request_limit":20`, `"request_limit":9007199254740992`, 1),
		strings.Replace(valid, `"quota":{"amount_usd":100,"token_limit":2000,"request_limit":20}`, `"quota":null`, 1),
	} {
		if response := callManagement(t, app, http.MethodPut, routeKeysTemporaryQuota, nil, body); response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid input status = %d, body = %s", response.StatusCode, body)
		}
	}
	// Ordinary API keys use resource routes, which must never register writes.
	raw, err := app.HandleMethod(MethodManagementHandle, mustMarshal(t, ManagementRequest{
		Method: http.MethodPut, Path: resourceBase + routeKeysTemporaryQuota,
		Headers: http.Header{"Authorization": {"Bearer " + accountTestKeyA}}, Body: mustMarshal(t, req),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var response ManagementResponse
	decodeResult(t, raw, &response)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("resource mutation status = %d", response.StatusCode)
	}
	view, _ := app.store.KeyViewForScope(req.Scope)
	if view.Windows[0].Dimensions[0].Limit != "600" {
		t.Fatal("invalid request changed credit")
	}
}

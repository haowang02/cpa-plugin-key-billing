package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cpa-key-billing/internal/billing"
)

func TestDownloadErrorsPreserveCauseWithoutURLSecrets(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.Shutdown)
	app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
		if method == hostAuthModels {
			return json.RawMessage(`{"credentials":[]}`), nil
		}
		if method != hostHTTPDo {
			t.Fatalf("unexpected host call %s", method)
		}
		return nil, &url.Error{Op: "Get", URL: "https://dummy:dummy-password@example.invalid/?key=dummy-secret", Err: context.DeadlineExceeded}
	})
	if _, err := app.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{
		ConfigYAML: testConfigYAML(t, true),
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.RefreshReferencePrices(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	message := app.store.ReferencePriceMetadata().LastError
	if !strings.Contains(message, "context deadline exceeded") || strings.Contains(message, "dummy-") {
		t.Fatalf("failure metadata lost diagnostics or disclosed URL: %s", message)
	}
}

func TestPriceAdmissionAndDeleteWithoutInventory(t *testing.T) {
	app := newConfiguredApp(t)
	intercept := func(format, model string) RequestInterceptResponse {
		t.Helper()
		raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{SourceFormat: format, Model: model, RequestedModel: model}))
		if err != nil {
			t.Fatal(err)
		}
		var result RequestInterceptResponse
		decodeResult(t, raw, &result)
		return result
	}
	for _, format := range []string{"openai", "openai-response", "claude", "gemini"} {
		result := intercept(format, "unpriced-dummy")
		var payload struct {
			Error struct{ Type, Code, Message string }
		}
		if err := json.Unmarshal(result.ResponseBody, &payload); err != nil {
			t.Fatal(err)
		}
		if !result.Terminate || result.StatusCode != 503 || payload.Error.Type != "cpa_key_billing_error" || payload.Error.Code != "model_price_error" || payload.Error.Message != "Model unpriced-dummy has no configured price" {
			t.Fatal(format, result, payload)
		}
	}
	for _, model := range []string{"unpriced-dummy", "gpt-4o"} {
		callOK(t, app, http.MethodPut, routePrices, nil, billing.CustomPrice{ModelID: model}, 200, nil)
		if result := intercept("openai", model); result.Terminate {
			t.Fatal("configured price rejected", result)
		}
		price, _, err := app.store.ResolveModelPrice(model, model, false)
		if err != nil || price.Source != billing.PriceSourceCustom || price.InputPer1M != 0 || price.OutputPer1M != 0 {
			t.Fatal(price, err)
		}
		callOK(t, app, http.MethodDelete, routePrices, url.Values{"model_id": {model}}, nil, 200, nil)
		if result := intercept("openai", model); result.Terminate != (model == "unpriced-dummy") {
			t.Fatal("delete did not fall back to reference/missing", result)
		}
	}
	callOK(t, app, http.MethodPut, routePrices, nil, billing.CustomPrice{ModelID: "gpt-*"}, 200, nil)

	raw, err := app.HandleMethod(MethodPluginReconfigure, mustMarshal(t, LifecycleRequest{
		ConfigYAML: append(testConfigYAML(t, true), "allow_unpriced_models: true\n"...),
	}))
	if err != nil {
		t.Fatal(err)
	}
	decodeResult(t, raw, nil)
	if result := intercept("openai", "unpriced-dummy"); result.Terminate {
		t.Fatal("allowed unpriced model was refused", result)
	}
}

func TestBuiltinPriceAdmissionAndListing(t *testing.T) {
	app := newConfiguredApp(t)
	for _, test := range []struct {
		model      string
		input, out float64
	}{
		{model: "codex-auto-review", input: 0, out: 0},
		{model: "gpt-image-1.5", input: 5, out: 32},
	} {
		price, model, err := app.store.ResolveModelPrice(test.model, test.model, true)
		if err != nil || model != test.model || price.Source != billing.PriceSourceBuiltin || price.InputPer1M != test.input || price.OutputPer1M != test.out {
			t.Fatalf("builtin admission price = %+v, model=%q, error=%v", price, model, err)
		}
		rows := readPrices(t, app, test.model)
		if len(rows) != 1 || rows[0].Source != billing.PriceSourceBuiltin || rows[0].InputPer1M != test.input || rows[0].OutputPer1M != test.out {
			t.Fatalf("builtin listing = %+v", rows)
		}
	}
}

func TestUsageAfterPriceDeletionKeepsTokensAndZeroCost(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed=%t", failed), func(t *testing.T) {
			app := newConfiguredApp(t)
			model := "house-model-without-reference"
			_, err := app.store.UpsertPrice(billing.CustomPrice{
				ModelID:    model,
				PriceRates: billing.PriceRates{InputPer1M: 5, OutputPer1M: 10},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := RequestInterceptRequest{
				Model: model, RequestedModel: model, SourceFormat: "openai",
			}
			raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, request))
			if err != nil {
				t.Fatal(err)
			}
			var admission RequestInterceptResponse
			decodeResult(t, raw, &admission)
			if admission.Terminate {
				t.Fatal("priced request was refused")
			}
			if err := app.store.DeletePrice(model); err != nil {
				t.Fatal(err)
			}
			publishUsageRecord(t, app, UsageRecord{
				Model: model, Alias: model, APIKey: "sk-dummy-deleted-price",
				Provider: "openai", RequestedAt: app.store.Now(), Failed: failed,
				Detail: UsageDetail{InputTokens: 100, OutputTokens: 25, TotalTokens: 125},
			})
			events := requestEventEntries(t, app)
			if len(events) != 1 {
				t.Fatalf("usage event lost: %+v", events)
			}
			event := events[0]
			if event.Failed != failed || event.PriceSource != billing.PriceSourceNone ||
				event.Cost.Total != 0 || event.Cost.UncachedInputTokens != 100 ||
				event.Cost.BilledOutputTokens != 25 {
				t.Fatalf("unpriced usage was not retained at zero cost: %+v", event)
			}
		})
	}
}

func TestPriceListBatchesAndAccountCannotIncludeUnrequestedCustomPrices(t *testing.T) {
	app := newConfiguredApp(t)
	if _, err := app.store.UpsertPrice(billing.CustomPrice{ModelID: "retired"}); err != nil {
		t.Fatal(err)
	}
	var rows []billing.PriceRow
	query := url.Values{"model": {"gpt-4o"}, "include_custom": {"false"}}
	callOK(t, app, http.MethodGet, routePrices, query, nil, 200, &rows)
	if len(rows) != 1 || rows[0].Source != billing.PriceSourceReference || !rows[0].InModels {
		t.Fatalf("batch included unrelated custom prices: %+v", rows)
	}
	query.Set("include_custom", "true")
	callOK(t, app, http.MethodGet, routePrices, query, nil, 200, &rows)
	if len(rows) != 2 {
		t.Fatalf("admin union=%+v", rows)
	}
	response := callAccount(t, app, routePrices, accountTestKeyA, query)
	if err := json.Unmarshal(response.Body, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ModelID != "gpt-4o" {
		t.Fatalf("account expanded custom prices: %+v", rows)
	}
	query.Set("include_custom", "invalid")
	callOK(t, app, http.MethodGet, routePrices, query, nil, 400, nil)
}

func TestReferencePriceAdmissionAndUsageWithoutCustomPrices(t *testing.T) {
	app := newConfiguredApp(t)
	for i := range 2 {
		raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
			SourceFormat: "openai", Model: "gpt-4o", RequestedModel: "gpt-4o",
		}))
		if err != nil {
			t.Fatal(err)
		}
		var admission RequestInterceptResponse
		decodeResult(t, raw, &admission)
		if admission.Terminate {
			t.Fatalf("reference price rejected: %s", admission.ResponseBody)
		}
		publishUsageRecord(t, app, UsageRecord{
			Model: "gpt-4o", Alias: "gpt-4o", Provider: "openai", APIKey: "sk-dummy-reference",
			RequestedAt: app.store.Now(), Detail: UsageDetail{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
		})
		entries := requestEventEntries(t, app)
		if len(entries) != i+1 || entries[0].PriceSource != billing.PriceSourceReference || entries[0].Cost.Total <= 0 {
			t.Fatalf("reference usage not billed: %+v", entries)
		}
	}
}

func TestCustomPriceIdentityAcrossListingAdmissionAndUsage(t *testing.T) {
	for _, test := range []struct {
		name       string
		requested  string
		customID   string
		billingID  string
		wantSource billing.PriceSource
		wantRate   float64
	}{
		{"thinking option", "gpt-4o(xhigh)", "gpt-4o", "gpt-4o", billing.PriceSourceCustom, 123},
		{"configured suffix", "gpt-4o(xhigh)", "gpt-4o(xhigh)", "gpt-4o(xhigh)", billing.PriceSourceCustom, 123},
		{"upstream custom price", "codex/gpt-4o", "gpt-4o", "codex/gpt-4o", billing.PriceSourceReference, 5},
		{"prefixed custom price", "codex/gpt-4o(xhigh)", "codex/gpt-4o", "codex/gpt-4o", billing.PriceSourceCustom, 123},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := newConfiguredApp(t)
			if _, err := app.store.UpsertPrice(billing.CustomPrice{
				ModelID: test.customID, PriceRates: billing.PriceRates{InputPer1M: 123, OutputPer1M: 456},
			}); err != nil {
				t.Fatal(err)
			}
			var rows []billing.PriceRow
			callOK(t, app, http.MethodGet, routePrices,
				url.Values{"model": {test.requested}, "include_custom": {"false"}}, nil, 200, &rows)
			if len(rows) != 1 || rows[0].ModelID != test.requested || rows[0].Source != test.wantSource || rows[0].InputPer1M != test.wantRate {
				t.Fatalf("display price = %+v", rows)
			}
			if test.wantSource == billing.PriceSourceCustom && rows[0].CustomPriceModelID != test.customID {
				t.Fatalf("custom price mutation target = %q, want %q", rows[0].CustomPriceModelID, test.customID)
			}
			price, model, err := app.store.ResolveModelPrice(test.requested, test.requested, true)
			if err != nil || model != test.billingID || price.Source != test.wantSource || price.InputPer1M != test.wantRate {
				t.Fatalf("admission price = %+v, model = %q, error = %v", price, model, err)
			}
			publishUsageRecord(t, app, UsageRecord{
				Model: "gpt-4o", Alias: test.requested, Provider: "openai", APIKey: "sk-dummy-price-identity",
				RequestedAt: app.store.Now(), Detail: UsageDetail{InputTokens: 1000, TotalTokens: 1000},
			})
			events := requestEventEntries(t, app)
			if len(events) != 1 || events[0].BillingModel != test.billingID || events[0].PriceSource != test.wantSource || events[0].Cost.AppliedInputPer1M != test.wantRate {
				t.Fatalf("usage price = %+v", events)
			}
			assertCostClose(t, events[0].Cost.Total, test.wantRate/1000)
		})
	}
}

func TestReferencePriceSearchAcceptsCPAModelID(t *testing.T) {
	app := newConfiguredApp(t)
	var result struct {
		Prices []billing.ReferencePrice `json:"prices"`
	}
	callOK(t, app, http.MethodGet, routePrices+"/reference",
		url.Values{"q": {"codex/gpt-4o(xhigh)"}, "limit": {"1"}}, nil, 200, &result)
	if len(result.Prices) != 1 || result.Prices[0].ProviderID != "openai" || result.Prices[0].ModelID != "gpt-4o" || !result.Prices[0].IsCanonical {
		t.Fatalf("reference search = %+v", result.Prices)
	}
}

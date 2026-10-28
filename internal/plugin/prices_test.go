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
}

func reconfigureUnpricedModels(t *testing.T, app *App, statePath string, enabled bool) {
	t.Helper()
	raw, err := app.HandleMethod(MethodPluginReconfigure, mustMarshal(t, LifecycleRequest{
		ConfigYAML: []byte(fmt.Sprintf("enabled: true\nstate_file: %q\nallow_unpriced_models: %t\n", statePath, enabled)),
	}))
	if err != nil {
		t.Fatal(err)
	}
	decodeResult(t, raw, nil)
	if app.store.AllowUnpricedModels() != enabled {
		t.Fatalf("allow_unpriced_models was not updated to %t", enabled)
	}
}

func interceptUnpricedModel(t *testing.T, app *App, requestID string) RequestInterceptResponse {
	t.Helper()
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		RequestID: requestID, SourceFormat: "openai", Model: "unpriced-dummy", RequestedModel: "unpriced-dummy",
		Metadata: map[string]any{MetadataCallerScope: flowScope(), MetadataRequestPath: "/v1/chat/completions"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var response RequestInterceptResponse
	decodeResult(t, raw, &response)
	return response
}

func TestAllowUnpricedModelsReconfigure(t *testing.T) {
	app, statePath := newAppWithPriceAndState(t, true)
	for _, enabled := range []bool{false, true, false} {
		reconfigureUnpricedModels(t, app, statePath, enabled)
		for _, format := range []string{"openai", "openai-response", "claude", "gemini"} {
			for _, helper := range []bool{false, true} {
				metadata := map[string]any{}
				if helper {
					metadata[MetadataSource] = SourcePluginHostModelCallback
				}
				for _, model := range []string{"unpriced-dummy", "gpt-4o", flowModel, "codex-auto-review"} {
					raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
						SourceFormat: format, Model: model, RequestedModel: model, Metadata: metadata,
					}))
					if err != nil {
						t.Fatal(err)
					}
					var response RequestInterceptResponse
					decodeResult(t, raw, &response)
					wantRejected := model == "unpriced-dummy" && !enabled
					if response.Terminate != wantRejected || wantRejected && response.StatusCode != http.StatusServiceUnavailable {
						t.Fatalf("enabled=%t, format=%s, helper=%t, model=%s: %+v", enabled, format, helper, model, response)
					}
				}
			}
		}
	}
}

func TestAllowUnpricedModelsKeepsUsageAndQuota(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed=%t", failed), func(t *testing.T) {
			app, statePath := newAppWithPriceAndState(t, true)
			reconfigureUnpricedModels(t, app, statePath, true)
			if _, err := app.store.SyncKeys([]string{testAPIKey}, false); err != nil {
				t.Fatal(err)
			}
			if _, err := app.store.CreatePlanWithBindings(billing.Plan{ID: "unpriced-plan", Windows: []billing.QuotaWindow{
				{Name: "Usage", AmountUSD: 1, TokenLimit: 125, RequestLimit: 1, PeriodSeconds: 3600},
			}}, []string{flowScope()}); err != nil {
				t.Fatal(err)
			}
			if response := interceptUnpricedModel(t, app, "unpriced-usage"); response.Terminate {
				t.Fatalf("unpriced request was refused: %+v", response)
			}
			publishUsageRecord(t, app, UsageRecord{
				Model: "unpriced-dummy", Alias: "unpriced-dummy", APIKey: testAPIKey,
				Provider: "openai", RequestedAt: app.store.Now(), Failed: failed,
				Failure: UsageFailure{StatusCode: 502, Body: `{"error":{"message":"dummy upstream failure","type":"server_error"}}`},
				Detail:  UsageDetail{InputTokens: 100, OutputTokens: 25, TotalTokens: 125},
			})
			completeRequest(t, app, "unpriced-usage")
			events := requestEventEntries(t, app)
			if len(events) != 1 {
				t.Fatalf("usage event lost: %+v", events)
			}
			event := events[0]
			if event.Failed != failed || event.PriceSource != billing.PriceSourceNone || event.Cost.TotalUSD != 0 ||
				event.Cost.UncachedInputTokens != 100 || event.Cost.BilledOutputTokens != 25 {
				t.Fatalf("unpriced usage was not retained at zero cost: %+v", event)
			}
			if failed {
				failures, err := app.store.RequestErrors(billing.RequestErrorQuery{})
				if err != nil || len(failures.Entries) != 1 || failures.Entries[0].StatusCode != 502 ||
					!strings.Contains(failures.Entries[0].Body, "dummy upstream failure") || failures.Entries[0].ErrorType != "server_error" {
					t.Fatalf("upstream failure details lost: %+v, error=%v", failures, err)
				}
			}
			key, ok := app.store.KeyViewForScope(flowScope())
			if !ok || len(key.Windows) != 1 || len(key.Windows[0].Dimensions) != 3 {
				t.Fatalf("quota view = %+v", key)
			}
			wantRequests := "1"
			if failed {
				wantRequests = "0"
			}
			wantUsage := map[billing.QuotaMetric]string{billing.QuotaAmount: "0", billing.QuotaTokens: "125", billing.QuotaRequests: wantRequests}
			for _, dimension := range key.Windows[0].Dimensions {
				if dimension.Used.String() != wantUsage[dimension.Metric] {
					t.Fatalf("quota %s used=%s, want %s", dimension.Metric, dimension.Used, wantUsage[dimension.Metric])
				}
			}
			if response := interceptUnpricedModel(t, app, "unpriced-after-quota"); !response.Terminate || response.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("unpriced request bypassed exhausted quota: %+v", response)
			}
		})
	}
}

func TestAllowUnpricedModelsPreservesAdmissionControls(t *testing.T) {
	for _, control := range []string{"model", "concurrency", "amount_quota", "credential"} {
		t.Run(control, func(t *testing.T) {
			app, statePath := newAppWithPriceAndState(t, true)
			reconfigureUnpricedModels(t, app, statePath, true)
			if _, err := app.store.SyncKeys([]string{testAPIKey}, false); err != nil {
				t.Fatal(err)
			}
			wantStatus := http.StatusTooManyRequests
			wantMessage := "concurrency limit reached"
			switch control {
			case "model":
				if err := app.store.SetKeyRoutes(flowScope(), billing.RouteBindings{RouteRule: billing.RouteRule{DeniedModels: []string{"unpriced-dummy"}}}); err != nil {
					t.Fatal(err)
				}
				wantStatus, wantMessage = http.StatusForbidden, "denied by a routing rule"
			case "concurrency":
				if err := app.store.SetConcurrencyLimit(flowScope(), 1); err != nil {
					t.Fatal(err)
				}
				if response := interceptUnpricedModel(t, app, "unpriced-active"); response.Terminate {
					t.Fatalf("first request refused: %+v", response)
				}
			case "amount_quota":
				if _, err := app.store.CreatePlanWithBindings(billing.Plan{ID: "amount-plan", Windows: []billing.QuotaWindow{
					{Name: "Amount", AmountUSD: 1, PeriodSeconds: 3600},
				}}, []string{flowScope()}); err != nil {
					t.Fatal(err)
				}
				requestID := admit(t, app, "openai", "/v1/chat/completions")
				billUsage(t, app, 0, 0, 0, 500_000, 0)
				completeRequest(t, app, requestID)
				wantMessage = "quota exhausted"
			case "credential":
				if err := app.store.SetKeyRoutes(flowScope(), billing.RouteBindings{RouteRule: billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("allowed-dummy")}}}); err != nil {
					t.Fatal(err)
				}
				if response := interceptUnpricedModel(t, app, "unpriced-credential"); response.Terminate {
					t.Fatalf("request refused before credential selection: %+v", response)
				}
				raw, err := app.HandleMethod(MethodRequestInterceptAfter, mustMarshal(t, RequestInterceptRequest{
					RequestID: "unpriced-credential", SourceFormat: "openai", Model: "unpriced-dummy", RequestedModel: "unpriced-dummy",
					Metadata: map[string]any{MetadataCallerScope: flowScope(), MetadataSelectedAuth: "denied-dummy"},
				}))
				if err != nil {
					t.Fatal(err)
				}
				var response RequestInterceptResponse
				decodeResult(t, raw, &response)
				if !response.Terminate || response.StatusCode != http.StatusServiceUnavailable ||
					!strings.Contains(string(response.ResponseBody), "routing_credential_mismatch") {
					t.Fatalf("unpriced request bypassed credential restrictions: %+v", response)
				}
				completeRequest(t, app, "unpriced-credential")
				return
			}
			response := interceptUnpricedModel(t, app, "unpriced-blocked")
			if !response.Terminate || response.StatusCode != wantStatus || !strings.Contains(string(response.ResponseBody), wantMessage) {
				t.Fatalf("unpriced request bypassed %s control: %+v", control, response)
			}
			if control == "concurrency" {
				completeRequest(t, app, "unpriced-active")
				if response := interceptUnpricedModel(t, app, "unpriced-released"); response.Terminate {
					t.Fatalf("request still blocked after releasing concurrency: %+v", response)
				}
				completeRequest(t, app, "unpriced-released")
			}
			if key, _ := app.store.KeyViewForScope(flowScope()); key.CurrentConcurrency != 0 {
				t.Fatalf("rejected request retained a concurrency slot: %+v", key)
			}
		})
	}
}

type priceAdmissionRepository struct {
	billing.Repository
	references *priceAdmissionReferenceRepository
}

func (repo *priceAdmissionRepository) OpenReferencePrices() (billing.ReferencePriceRepository, error) {
	references, err := repo.Repository.OpenReferencePrices()
	if err != nil {
		return nil, err
	}
	repo.references = &priceAdmissionReferenceRepository{ReferencePriceRepository: references}
	return repo.references, nil
}

type priceAdmissionReferenceRepository struct {
	billing.ReferencePriceRepository
	lookupErr error
}

func (repo *priceAdmissionReferenceRepository) ReferencePriceCandidates(ctx context.Context, keys []string) ([]billing.ReferencePrice, error) {
	if repo.lookupErr != nil {
		return nil, repo.lookupErr
	}
	return repo.ReferencePriceRepository.ReferencePriceCandidates(ctx, keys)
}

func TestAllowUnpricedModelsDoesNotBypassPriceStorageErrors(t *testing.T) {
	var repo *priceAdmissionRepository
	store := billing.NewStore(func(path string) (billing.Repository, error) {
		base, err := openRepository(path)
		if err != nil {
			return nil, err
		}
		repo = &priceAdmissionRepository{Repository: base}
		return repo, nil
	}, func(context.Context) ([]byte, error) {
		return []byte(`{"providers":{"dummy":{"id":"dummy","models":{"priced-dummy":{"id":"priced-dummy","cost":{"input":1,"output":2}}}}}}`), nil
	})
	app := newApp(store)
	t.Cleanup(app.Shutdown)
	config := append(testConfigYAML(t, true), []byte("allow_unpriced_models: true\n")...)
	raw, err := app.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{ConfigYAML: config}))
	if err != nil {
		t.Fatal(err)
	}
	decodeResult(t, raw, nil)
	if !app.store.ReferencePriceMetadata().Usable {
		t.Fatal("reference price fixture was not loaded")
	}
	repo.references.lookupErr = errors.New("dummy reference price storage failure")
	response := interceptUnpricedModel(t, app, "unpriced-storage-error")
	if !response.Terminate || response.StatusCode != http.StatusServiceUnavailable ||
		!strings.Contains(string(response.ResponseBody), "price_storage_error") {
		t.Fatalf("unpriced policy bypassed a price storage error: %+v", response)
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
				event.Cost.TotalUSD != 0 || event.Cost.UncachedInputTokens != 100 ||
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
		if len(entries) != i+1 || entries[0].PriceSource != billing.PriceSourceReference || entries[0].Cost.TotalUSD <= 0 {
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
			assertCostClose(t, events[0].Cost.TotalUSD, test.wantRate/1000)
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

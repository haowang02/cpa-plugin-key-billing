package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"cpa-key-billing/internal/billing"
)

func restrictApp(t *testing.T, models ...string) *App {
	t.Helper()
	app := newAppWithPrice(t, true)
	if _, errSync := app.store.SyncKeys([]string{testAPIKey}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	_, errCreate := app.store.CreateRoute(billing.Route{Name: "基础", Rule: billing.RouteRule{Models: models}}, []string{flowScope()})
	if errCreate != nil {
		t.Fatalf("CreateRoute error = %v", errCreate)
	}
	return app
}

func interceptModel(t *testing.T, app *App, clientFormat, model string) RequestInterceptResponse {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		SourceFormat: clientFormat, Model: model, RequestedModel: model, Metadata: map[string]any{
			MetadataCallerScope: flowScope(),
			MetadataRequestPath: "/v1/chat/completions",
		},
	}))
	if errHandle != nil {
		t.Fatalf("request.intercept_before error = %v", errHandle)
	}
	var response RequestInterceptResponse
	decodeResult(t, raw, &response)
	return response
}

func TestForbiddenModelIsRefusedInEveryClientFormat(t *testing.T) {
	app := restrictApp(t, "chat/fast")

	cases := []struct {
		format string
		want   map[string]string
	}{
		{"openai", map[string]string{"type": "permission_error", "code": "insufficient_quota"}},
		{"claude", map[string]string{"type": "permission_error"}},
		{"gemini-cli", map[string]string{"type": "permission_error", "code": "insufficient_quota"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.format, func(t *testing.T) {
			response := interceptModel(t, app, testCase.format, flowModel)
			if !response.Terminate || response.StatusCode != http.StatusForbidden {
				t.Fatalf("response = %+v, want a terminating 403", response)
			}
			if retry := response.ResponseHeaders.Get("Retry-After"); retry != "" {
				t.Fatalf("Retry-After = %q, want none on a permanent refusal", retry)
			}
			var payload struct {
				Error map[string]any `json:"error"`
			}
			if errUnmarshal := json.Unmarshal(response.ResponseBody, &payload); errUnmarshal != nil {
				t.Fatalf("decode body: %v (raw=%s)", errUnmarshal, response.ResponseBody)
			}
			for field, want := range testCase.want {
				if got, _ := payload.Error[field].(string); got != want {
					t.Fatalf("error.%s = %q, want %q (body=%s)", field, got, want, response.ResponseBody)
				}
			}
			message, _ := payload.Error["message"].(string)
			for _, want := range []string{flowModel, "chat/fast"} {
				if !strings.Contains(message, want) {
					t.Fatalf("message = %q, want it to name %q", message, want)
				}
			}
		})
	}
}

func TestForbiddenModelLeavesTheSubscriptionUntouched(t *testing.T) {
	app := restrictApp(t, "chat/fast")
	if _, errCreate := app.store.CreatePlanWithBindings(billing.Plan{
		ID: "daily", Name: "Daily 1", AmountUSD: 1, PeriodSeconds: 86400,
	}, nil); errCreate != nil {
		t.Fatalf("CreatePlanWithBindings error = %v", errCreate)
	}
	if errBind := app.store.BindKey(flowScope(), "daily"); errBind != nil {
		t.Fatalf("BindKey error = %v", errBind)
	}

	if response := interceptModel(t, app, "openai", flowModel); !response.Terminate {
		t.Fatal("a model the key may not call was admitted")
	}
	for _, key := range app.store.KeyViews() {
		if key.Scope == flowScope() && (!key.CycleEndAt.IsZero() || key.SpentUSD != 0) {
			t.Fatalf("key = %+v, want its cycle left inactive", key)
		}
	}
}

func TestGrantedModelIsBilledNormally(t *testing.T) {
	app := restrictApp(t, flowModel, "chat/fast")

	billOneRequest(t, app, testAPIKey, 1_000)
	entries := requestEventEntries(t, app)
	if len(entries) != 1 || entries[0].BillingModel != flowModel {
		t.Fatalf("request events = %+v, want the granted model billed", entries)
	}
}

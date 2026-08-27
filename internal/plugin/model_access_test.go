package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"cpa-key-billing/internal/billing"
)

// restrictApp grants the test key one model that is not the one flow requests
// make, so every admitted request in this file is one the key may not call.
func restrictApp(t *testing.T, models ...string) *App {
	t.Helper()
	app := newAppWithPrice(t, true)
	if _, errSync := app.store.SyncKeys([]string{testAPIKey}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	group, errCreate := app.store.CreateModelGroup(billing.ModelGroup{Name: "基础", Models: models})
	if errCreate != nil {
		t.Fatalf("CreateModelGroup error = %v", errCreate)
	}
	if errSet := app.store.SetKeyModels(flowScope(), []string{group.ID}, nil); errSet != nil {
		t.Fatalf("SetKeyModels error = %v", errSet)
	}
	return app
}

func onlyModelBlockEvent(t *testing.T, app *App) billing.Event {
	t.Helper()
	events, errEvents := app.store.Events()
	if errEvents != nil {
		t.Fatalf("Events error = %v", errEvents)
	}
	for _, event := range events {
		if strings.HasPrefix(event.Message, "模型拦截：") {
			return event
		}
	}
	t.Fatalf("events = %+v, want a model block", events)
	return billing.Event{}
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

// A refused model is answered in the client's own error shape, with the type and
// code CLIProxyAPI uses for a 403, so its SDK surfaces a permission problem
// rather than a parse failure.
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
			// Nothing to wait for: only an operator can change this answer.
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

// The request never reached an upstream, so it is billed nothing and logged
// nowhere but the plugin log — the same treatment an exhausted quota gets.
func TestForbiddenModelIsReportedAndNotBilled(t *testing.T) {
	app := restrictApp(t, "chat/fast")

	if response := interceptModel(t, app, "openai", flowModel); !response.Terminate {
		t.Fatal("a model the key may not call was admitted")
	}
	if entries := logEntries(t, app); len(entries) != 0 {
		t.Fatalf("billing log = %+v, want a refused request left out of it", entries)
	}
	event := onlyModelBlockEvent(t, app)
	if event.Level != billing.EventInfo {
		t.Fatalf("level = %q, want information: enforcement working is not a fault", event.Level)
	}
	for _, want := range []string{"模型拦截：", "/v1/chat/completions", flowModel, "chat/fast"} {
		if !strings.Contains(event.Message, want) {
			t.Fatalf("message = %q, want it to name %q", event.Message, want)
		}
	}
}

// A refused model must not open a subscription period. The window would then be
// counted against a request that never ran, and a never-reset plan would have
// handed out its only budget to nothing at all.
func TestForbiddenModelLeavesTheSubscriptionUntouched(t *testing.T) {
	app := restrictApp(t, "chat/fast")
	if _, errCreate := app.store.CreatePlanWithBindings(billing.Plan{
		ID: "daily", Name: "Daily 1", AmountUSD: 1, Period: billing.Period{Kind: billing.PeriodDaily},
	}, nil); errCreate != nil {
		t.Fatalf("CreatePlanWithBindings error = %v", errCreate)
	}
	if errBind := app.store.BindKey(flowScope(), "daily"); errBind != nil {
		t.Fatalf("BindKey error = %v", errBind)
	}

	if response := interceptModel(t, app, "openai", flowModel); !response.Terminate {
		t.Fatal("a model the key may not call was admitted")
	}
	for _, key := range app.store.KeyDirectory().Keys {
		if key.Scope == flowScope() && (!key.CycleEndAt.IsZero() || key.SpentUSD != 0) {
			t.Fatalf("key = %+v, want its cycle left inactive", key)
		}
	}
}

func TestGrantedModelIsBilledNormally(t *testing.T) {
	app := restrictApp(t, flowModel, "chat/fast")

	billOneRequest(t, app, testAPIKey, 1_000)
	entries := logEntries(t, app)
	if len(entries) != 1 || entries[0].BillingModel != flowModel {
		t.Fatalf("billing log = %+v, want the granted model billed", entries)
	}
}

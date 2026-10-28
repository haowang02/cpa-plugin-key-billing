package plugin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"cpa-key-billing/internal/billing"
)

func TestRegisterDeclaresExpectedCapabilities(t *testing.T) {
	app := newConfiguredApp(t)
	reconfigure := func(hostErr error) Registration {
		app.SetHostCaller(func(string, any) (json.RawMessage, error) { return json.RawMessage(`{"credentials":[]}`), hostErr })
		raw, errHandle := app.HandleMethod(MethodPluginReconfigure, mustMarshal(t, LifecycleRequest{
			ConfigYAML: testConfigYAML(t, true),
		}))
		if errHandle != nil {
			t.Fatalf("plugin.reconfigure error = %v", errHandle)
		}
		var registration Registration
		decodeResult(t, raw, &registration)
		return registration
	}

	// Hosts without host.auth.models must never route responses to the plugin.
	if reconfigure(errors.New("host_call_failed：unsupported host callback host.auth.models")).Capabilities.ResponseInterceptor {
		t.Fatal("response_interceptor declared on a host without host.auth.models")
	}
	registration := reconfigure(nil)
	// The host refuses to load a plugin declaring more than it implements.
	if registration.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", registration.SchemaVersion, SchemaVersion)
	}
	caps := registration.Capabilities
	if !caps.RequestInterceptor || !caps.ResponseInterceptor || !caps.RequestLifecyclePlugin || !caps.UsagePlugin || !caps.ManagementAPI || !caps.Scheduler || !caps.SchedulerAcrossPriorities || !caps.QuotaProvider {
		t.Fatalf("capabilities = %+v, want every hook billing depends on", caps)
	}
	if registration.Metadata.Name != PluginName || registration.Metadata.Version != Version {
		t.Fatalf("metadata = %+v", registration.Metadata)
	}
	if len(registration.Metadata.ConfigFields) == 0 {
		t.Fatal("ConfigFields is empty, the panel needs them to render the config form")
	}
	fields := make(map[string]ConfigField, len(registration.Metadata.ConfigFields))
	for _, field := range registration.Metadata.ConfigFields {
		fields[field.Name] = field
		if field.Default == nil {
			t.Fatalf("missing default for %s", field.Name)
		}
	}
	if fields["state_file"].Type != "string" || fields["debug"].Type != "boolean" ||
		fields["codex_fast_mode_billing"].Type != "boolean" || fields["allow_unpriced_models"].Type != "boolean" ||
		fields["mask_api_key_view_emails"].Type != "boolean" || fields["allow_api_key_quota_reset"].Type != "boolean" ||
		fields["cny_per_usd"].Type != "number" || fields["cny_per_usd"].Default != float64(7) ||
		fields["routing"].Type != "object" || fields["enabled"].Name != "" {
		t.Fatalf("ConfigFields = %+v", registration.Metadata.ConfigFields)
	}
	routing, ok := fields["routing"].Default.(map[string]any)
	if !ok || routing["strategy"] != "round-robin" || routing["session-affinity"] != false || routing["session-affinity-ttl"] != "1h" || len(routing) != 3 {
		t.Fatalf("routing metadata defaults = %+v", fields["routing"].Default)
	}
}

func TestUnknownMethodReturnsErrorEnvelopeNotAnError(t *testing.T) {
	app := newConfiguredApp(t)
	raw, errHandle := app.HandleMethod("does.not.exist", nil)
	if errHandle != nil {
		t.Fatalf("unknown method returned a transport error = %v, want an error envelope", errHandle)
	}
	var envelope Envelope
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("decode envelope: %v", errUnmarshal)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "unknown_method" {
		t.Fatalf("envelope = %+v, want an unknown_method error", envelope)
	}
}

func TestConfigureReportsReferencePricePreloadFailure(t *testing.T) {
	requests := 0
	app := NewApp()
	app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
		if method == hostAuthModels {
			return json.RawMessage(`{"credentials":[]}`), nil
		}
		request := payload.(hostHTTPRequest)
		if method != hostHTTPDo || request.Method != http.MethodGet || request.URL != billing.ModelsDevPricesURL || len(request.Headers) != 0 {
			t.Fatalf("unexpected reference price request: %s %+v", method, request)
		}
		requests++
		return mustJSONRaw(t, hostHTTPResponse{StatusCode: http.StatusServiceUnavailable}), nil
	})
	t.Cleanup(app.Shutdown)
	raw, errHandle := app.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{
		ConfigYAML: testConfigYAML(t, true),
	}))
	if errHandle != nil {
		t.Fatalf("plugin.register error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
	if requests != 1 {
		t.Fatalf("reference price preload requests = %d, want 1", requests)
	}
	page, errEvents := app.store.PluginLogsPage(billing.PluginLogQuery{Limit: 500})
	if errEvents != nil {
		t.Fatal(errEvents)
	}
	events := page.Entries
	if len(events) == 0 || events[0].Level != billing.PluginLogError ||
		!strings.Contains(events[0].Message, "Failed to sync models.dev reference prices") {
		t.Fatalf("events = %+v, want the preload failure", events)
	}
}

func TestManagementRegistrationExposesOnlyCurrentEndpoints(t *testing.T) {
	registration := managementRegistration()
	wantRoutes := map[string]bool{}
	for _, value := range []string{
		"GET /keys", "GET /plans", "GET /routes", "GET /credentials", "GET /currency", "GET /prices", "GET /prices/reference",
		"POST /prices/reference/refresh", "PUT /prices", "DELETE /prices", "GET /prices/reference/status",
		"POST /plans", "PATCH /plans", "DELETE /plans",
		"POST /routes", "PATCH /routes", "DELETE /routes", "PUT /keys/routes",
		"POST /keys/bind", "POST /keys/unbind", "POST /keys/reset",
		"POST /keys/label", "POST /keys/concurrency", "POST /keys/sync",
		"POST /credentials/sync",
		"GET /analysis", "GET /analysis/speed", "GET /events", "GET /events/keys", "GET /errors",
		"GET /plugin-logs", "DELETE /plugin-logs", "GET /auth-files", "GET /auth-files/quota",
		"POST /auth-files/quota/reset", "GET /auth-files/history", "GET /usage/models",
		"GET /ai-providers", "POST /ai-providers/balance", "POST /ai-providers/note",
	} {
		wantRoutes[value] = false
	}
	if len(registration.Routes) != len(wantRoutes) {
		t.Fatalf("routes = %d, want %d: %+v", len(registration.Routes), len(wantRoutes), registration.Routes)
	}
	for _, route := range registration.Routes {
		if !strings.HasPrefix(route.Path, managementBase+"/") || strings.ContainsAny(route.Path, ":*") {
			t.Fatalf("invalid management route: %+v", route)
		}
		key := route.Method + " " + strings.TrimPrefix(route.Path, managementBase)
		if _, ok := wantRoutes[key]; !ok {
			t.Fatalf("unexpected management route %q", key)
		}
		if wantRoutes[key] {
			t.Fatalf("duplicate management route %q", key)
		}
		wantRoutes[key] = true
	}

	wantResources := map[string]bool{
		"/ui": false, "/profile": false, "/subscription": false, "/routing": false, "/currency": false, "/prices": false,
		"/analysis": false, "/analysis/speed": false, "/events": false, "/errors": false,
		"/auth-files": false, "/auth-files/quota": false, "/auth-files/quota/reset": false, "/auth-files/history": false,
		"/usage/models": false, "/ai-providers": false,
	}
	if len(registration.Resources) != len(wantResources) {
		t.Fatalf("resources = %d, want %d: %+v", len(registration.Resources), len(wantResources), registration.Resources)
	}
	menuCount := 0
	for _, resource := range registration.Resources {
		path := strings.TrimPrefix(resource.Path, resourceBase)
		if _, ok := wantResources[path]; !ok {
			t.Fatalf("unexpected resource route %q", path)
		}
		wantResources[path] = true
		if resource.Menu != "" {
			menuCount++
			if path != "/ui" || resource.Menu != MenuLabel {
				t.Fatalf("invalid menu resource: %+v", resource)
			}
		}
	}
	if menuCount != 1 {
		t.Fatalf("menu resources = %d, want 1", menuCount)
	}
}

func TestManagementRejectsLookalikeRoutePrefixes(t *testing.T) {
	app := newTestApp(t)
	for _, path := range []string{managementBase + "-other/keys", resourceBase + "-other/ui"} {
		raw, errHandle := app.handleManagement(mustMarshal(t, ManagementRequest{
			Method: http.MethodGet,
			Path:   path,
		}))
		if errHandle != nil {
			t.Fatalf("handleManagement(%q): %v", path, errHandle)
		}
		var envelope Envelope
		if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		var response ManagementResponse
		if errUnmarshal := json.Unmarshal(envelope.Result, &response); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("handleManagement(%q) status = %d", path, response.StatusCode)
		}
	}
}

func TestHandleMethodRecoversFromPanic(t *testing.T) {
	app := newTestApp(t)
	t.Cleanup(app.Shutdown)
	app.SetHostCaller(func(string, any) (json.RawMessage, error) { panic("host callback failed") })
	_, errHandle := app.HandleMethod(MethodManagementHandle, mustMarshal(t, ManagementRequest{
		Method: http.MethodGet, Path: managementBase + routeAuthFiles,
	}))
	if errHandle == nil {
		t.Fatal("a panicking handler returned no error")
	}
}

package plugin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cpa-key-billing/internal/billing"
)

func TestRegisterDeclaresExpectedCapabilities(t *testing.T) {
	app := newConfiguredApp(t)
	raw, errHandle := app.HandleMethod(MethodPluginReconfigure, mustMarshal(t, LifecycleRequest{
		ConfigYAML: testConfigYAML(t, true),
	}))
	if errHandle != nil {
		t.Fatalf("plugin.reconfigure error = %v", errHandle)
	}
	var registration Registration
	decodeResult(t, raw, &registration)

	// The host refuses to load a plugin declaring more than it implements.
	if registration.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", registration.SchemaVersion, SchemaVersion)
	}
	caps := registration.Capabilities
	if !caps.RequestInterceptor || !caps.RequestLifecyclePlugin || !caps.UsagePlugin || !caps.ManagementAPI || !caps.Scheduler {
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
	}
	if fields["state_file"].Type != "string" || fields["enabled"].Type != "boolean" {
		t.Fatalf("ConfigFields = %+v", registration.Metadata.ConfigFields)
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

func TestConfigureReportsCatalogPreloadFailure(t *testing.T) {
	t.Cleanup(func() {
		if _, errCatalog := billing.EnsureBuiltinCatalog(); errCatalog != nil {
			t.Errorf("restore test catalog: %v", errCatalog)
		}
	})
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("CPA_KEY_BILLING_CATALOG_CACHE", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("CPA_KEY_BILLING_CATALOG_URL", server.URL)

	app := NewApp()
	t.Cleanup(app.Shutdown)
	raw, errHandle := app.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{
		ConfigYAML: testConfigYAML(t, true),
	}))
	if errHandle != nil {
		t.Fatalf("plugin.register error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
	if requests != 1 {
		t.Fatalf("catalog preload requests = %d, want 1", requests)
	}
	page, errEvents := app.store.PluginLogsPage(billing.PluginLogQuery{Limit: 500})
	if errEvents != nil {
		t.Fatal(errEvents)
	}
	events := page.Entries
	if len(events) == 0 || events[0].Level != billing.PluginLogError ||
		!strings.Contains(events[0].Message, "加载 models.dev 参考价目录失败") {
		t.Fatalf("events = %+v, want the preload failure", events)
	}
}

func TestManagementRegistrationExposesOnlyCurrentEndpoints(t *testing.T) {
	registration := managementRegistration()
	wantRoutes := map[string]bool{}
	for _, value := range []string{
		"GET /access", "GET /prices", "GET /prices/catalog",
		"POST /prices/catalog/refresh", "PUT /prices", "POST /prices/reset", "POST /prices/sync",
		"POST /plans", "PATCH /plans", "DELETE /plans",
		"POST /routes", "PATCH /routes", "DELETE /routes", "PUT /keys/routes",
		"POST /keys/bind", "POST /keys/unbind", "POST /keys/reset",
		"POST /keys/label", "POST /keys/concurrency", "POST /keys/sync",
		"POST /credentials/sync",
		"GET /analysis", "GET /events", "GET /errors",
		"GET /plugin-logs", "DELETE /plugin-logs", "GET /auth-files", "GET /auth-files/quota",
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
		"/ui": false, "/access": false, "/prices": false,
		"/analysis": false, "/events": false, "/errors": false,
		"/auth-files": false, "/auth-files/quota": false,
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
	app := NewApp()
	for _, path := range []string{managementBase + "-other/access", resourceBase + "-other/ui"} {
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
	app := NewApp()
	t.Cleanup(app.Shutdown)
	app.store = nil
	_, errHandle := app.HandleMethod(MethodManagementHandle, mustMarshal(t, ManagementRequest{
		Method: http.MethodGet,
		Path:   managementBase + routeAccess,
	}))
	if errHandle == nil {
		t.Fatal("a panicking handler returned no error")
	}
}

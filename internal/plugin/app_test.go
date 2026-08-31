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
	if !caps.RequestInterceptor || !caps.RequestLifecyclePlugin || !caps.UsagePlugin || !caps.ManagementAPI {
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
	events, errEvents := app.store.Events()
	if errEvents != nil {
		t.Fatal(errEvents)
	}
	if len(events) == 0 || events[0].Level != billing.EventError ||
		!strings.Contains(events[0].Message, "加载 models.dev 参考价目录失败") {
		t.Fatalf("events = %+v, want the preload failure", events)
	}
}

func TestManagementRegistrationDeclaresOneMenuResource(t *testing.T) {
	app := newConfiguredApp(t)
	raw, errHandle := app.HandleMethod(MethodManagementRegister, []byte(`{}`))
	if errHandle != nil {
		t.Fatalf("management.register error = %v", errHandle)
	}
	var registration ManagementRegistrationResponse
	decodeResult(t, raw, &registration)

	// The panel routes plugin pages by menu index, so exactly one resource may
	// declare a menu even though the API Key JSON reads are also registered as
	// browser resources.
	var menuResources []ResourceRoute
	for _, resource := range registration.Resources {
		if resource.Menu != "" {
			menuResources = append(menuResources, resource)
		}
	}
	if len(menuResources) != 1 {
		t.Fatalf("Resources = %+v, want exactly one menu entry", registration.Resources)
	}
	resource := menuResources[0]
	if resource.Menu != MenuLabel {
		t.Fatalf("Menu = %q, want %q", resource.Menu, MenuLabel)
	}
	if resource.Path != resourceBase+resourceUIPath {
		t.Fatalf("Path = %q, want %q", resource.Path, resourceBase+resourceUIPath)
	}
	wantResources := map[string]bool{
		resourceBase + resourceUIPath:              false,
		resourceBase + resourceAccountOverviewPath: false,
		resourceBase + resourceAccountPricesPath:   false,
		resourceBase + resourceAccountLogsPath:     false,
	}
	for _, item := range registration.Resources {
		if _, expected := wantResources[item.Path]; expected {
			wantResources[item.Path] = true
		}
	}
	for path, found := range wantResources {
		if !found {
			t.Errorf("resource %q is not registered", path)
		}
	}

	// The host rejects ':' and '*' in route paths and resolves them under
	// /v0/management, so every declared route must be an exact path there.
	if len(registration.Routes) == 0 {
		t.Fatal("Routes is empty")
	}
	for _, route := range registration.Routes {
		if !strings.HasPrefix(route.Path, managementBase+"/") {
			t.Fatalf("route %q is outside %q", route.Path, managementBase)
		}
		if strings.ContainsAny(route.Path, ":*") {
			t.Fatalf("route %q uses a path parameter, which the host rejects", route.Path)
		}
	}
}

func TestManagementRejectsLookalikeRoutePrefixes(t *testing.T) {
	app := NewApp()
	for _, path := range []string{managementBase + "-other/status", resourceBase + "-other/ui"} {
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
		Path:   managementBase + "/status",
	}))
	if errHandle == nil {
		t.Fatal("a panicking handler returned no error")
	}
}

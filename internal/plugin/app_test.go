package plugin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// decodeResult unwraps a successful envelope into v.
func decodeResult(t *testing.T, raw []byte, v any) {
	t.Helper()
	var envelope Envelope
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("decode envelope: %v (raw=%s)", errUnmarshal, raw)
	}
	if !envelope.OK {
		t.Fatalf("envelope reports failure: %+v", envelope.Error)
	}
	if v == nil {
		return
	}
	if errUnmarshal := json.Unmarshal(envelope.Result, v); errUnmarshal != nil {
		t.Fatalf("decode result: %v (raw=%s)", errUnmarshal, envelope.Result)
	}
}

// newConfiguredApp returns an app that has completed plugin.register against a
// temp state file, mirroring what the host does at load time.
func newConfiguredApp(t *testing.T) *App {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.Shutdown)
	configYAML := "enabled: true\npriority: 100\nstate_file: \"" + filepath.Join(t.TempDir(), "state.json") + "\"\n"
	raw, errHandle := app.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{
		ConfigYAML:    []byte(configYAML),
		SchemaVersion: SchemaVersion,
	}))
	if errHandle != nil {
		t.Fatalf("plugin.register error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
	return app
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	return raw
}

func TestRegisterDeclaresExpectedCapabilities(t *testing.T) {
	app := newConfiguredApp(t)
	raw, errHandle := app.HandleMethod(MethodPluginReconfigure, mustMarshal(t, LifecycleRequest{
		ConfigYAML:    []byte("enabled: true\n"),
		SchemaVersion: SchemaVersion,
	}))
	if errHandle != nil {
		t.Fatalf("plugin.reconfigure error = %v", errHandle)
	}
	var registration Registration
	decodeResult(t, raw, &registration)

	if registration.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", registration.SchemaVersion, SchemaVersion)
	}
	caps := registration.Capabilities
	if !caps.RequestInterceptor || !caps.RequestLifecyclePlugin ||
		!caps.ResponseInterceptor || !caps.StreamChunkInterceptor || !caps.ManagementAPI {
		t.Fatalf("capabilities = %+v, want every integration point declared", caps)
	}
	if registration.Metadata.Name != PluginName || registration.Metadata.Version != Version {
		t.Fatalf("metadata = %+v", registration.Metadata)
	}
	if len(registration.Metadata.ConfigFields) == 0 {
		t.Fatal("ConfigFields is empty, the panel needs them to render the config form")
	}
}

// TestRegisterRejectsOldHostSchema guards the plugin's core promise: request
// termination only exists from schema 2, so loading against an older host would
// account for spend while enforcing nothing.
func TestRegisterRejectsOldHostSchema(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.Shutdown)
	_, errHandle := app.HandleMethod(MethodPluginRegister, mustMarshal(t, LifecycleRequest{
		ConfigYAML:    []byte("enabled: true\n"),
		SchemaVersion: MinHostSchemaVersion - 1,
	}))
	if errHandle == nil {
		t.Fatal("plugin.register succeeded on an old host schema, want an error")
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

func TestManagementRegistrationDeclaresOneMenuResource(t *testing.T) {
	app := newConfiguredApp(t)
	raw, errHandle := app.HandleMethod(MethodManagementRegister, []byte(`{}`))
	if errHandle != nil {
		t.Fatalf("management.register error = %v", errHandle)
	}
	var registration ManagementRegistrationResponse
	decodeResult(t, raw, &registration)

	// The panel routes plugin pages by menu index, so exactly one entry keeps
	// bookmarked URLs stable across releases.
	if len(registration.Resources) != 1 {
		t.Fatalf("Resources = %+v, want exactly one menu entry", registration.Resources)
	}
	resource := registration.Resources[0]
	if resource.Menu != MenuLabel {
		t.Fatalf("Menu = %q, want %q", resource.Menu, MenuLabel)
	}
	if resource.Path != resourceBase+resourceUIPath {
		t.Fatalf("Path = %q, want %q", resource.Path, resourceBase+resourceUIPath)
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
		if route.Menu != "" {
			t.Fatalf("route %q declares a legacy Menu, which would turn it into an unauthenticated resource", route.Path)
		}
	}
}

func TestHandleMethodRecoversFromPanic(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.Shutdown)
	// A nil store would panic inside Status; the recover in HandleMethod must
	// turn that into an error rather than taking the proxy down.
	app.store = nil
	_, errHandle := app.HandleMethod(MethodManagementHandle, mustMarshal(t, ManagementRequest{
		Method: http.MethodGet,
		Path:   managementBase + "/status",
	}))
	if errHandle == nil {
		t.Fatal("a panicking handler returned no error")
	}
}

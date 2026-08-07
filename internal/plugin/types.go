// Package plugin mirrors the CLIProxyAPI plugin RPC contract and dispatches
// host calls into the billing domain.
//
// The types here intentionally duplicate CLIProxyAPI's sdk/pluginapi structs
// instead of importing them, so this plugin does not couple its build to a
// specific CPA release. Field names must match what the host marshals: the host
// structs carry no JSON tags, so Go's default (exported field name) encoding
// applies and the tags below are PascalCase. Capability and lifecycle payloads
// do carry explicit snake_case tags on the host side and are mirrored as such.
package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
)

const (
	// ABIVersion is the native C ABI shape understood by the host loader.
	ABIVersion uint32 = 1
	// SchemaVersion is the RPC JSON contract this plugin speaks. Version 2 is
	// required because request termination (RequestInterceptResponse.Terminate)
	// was introduced there.
	SchemaVersion uint32 = 2
	// MinHostSchemaVersion is the oldest host contract this plugin can run on.
	MinHostSchemaVersion uint32 = 2
)

// Plugin identity. PluginID doubles as the dynamic library file name, the
// `plugins.configs` key, and the Management/resource route segment.
const (
	PluginID   = "cpa-key-billing"
	PluginName = "cpa-key-billing"
	Version    = "0.1.0"

	// MenuLabel is the sidebar entry rendered by the CPA management panel.
	// Deliberately avoids 配额/额度, which the panel already uses for upstream
	// account quota.
	MenuLabel       = "API Key 计费"
	MenuDescription = "管理下游 API Key 的模型计费、订阅限额和用量统计"

	GitHubRepository = "https://github.com/router-for-me/CLIProxyAPI"
)

const (
	MethodPluginRegister    = "plugin.register"
	MethodPluginReconfigure = "plugin.reconfigure"

	MethodRequestInterceptBefore = "request.intercept_before"
	MethodRequestInterceptAfter  = "request.intercept_after"
	MethodRequestComplete        = "request.complete"

	MethodResponseInterceptAfter       = "response.intercept_after"
	MethodResponseInterceptStreamChunk = "response.intercept_stream_chunk"

	MethodManagementRegister = "management.register"
	MethodManagementHandle   = "management.handle"
)

// StreamChunkHeaderInitIndex marks the header-only initialization call the host
// makes before any stream payload arrives.
const StreamChunkHeaderInitIndex = -1

// Metadata keys the host places in Options.Metadata and forwards to interceptors.
const (
	// MetadataCallerScope is sha256("cli-proxy-api:caller-scope:v1\x00"+apiKey)
	// in hex. It is the only downstream-key identifier available at interception
	// time, and it covers keys presented via query string as well as headers.
	MetadataCallerScope = "caller_scope"
	// MetadataSource marks requests the host issues on behalf of a plugin.
	MetadataSource = "source"
	// SourcePluginHostModelCallback is the MetadataSource value for nested
	// plugin-initiated executions, which must not be billed again.
	SourcePluginHostModelCallback = "plugin_host_model_callback"
)

type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *EnvelopeError  `json:"error,omitempty"`
}

type EnvelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type LifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type Registration struct {
	SchemaVersion uint32       `json:"schema_version"`
	Metadata      Metadata     `json:"metadata"`
	Capabilities  Capabilities `json:"capabilities"`
}

type Metadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo,omitempty"`
	ConfigFields     []ConfigField `json:"ConfigFields"`
}

type ConfigField struct {
	Name        string   `json:"Name"`
	Type        string   `json:"Type"`
	EnumValues  []string `json:"EnumValues,omitempty"`
	Description string   `json:"Description"`
}

// Capabilities declares the host integration points this plugin implements.
//
// Note the absence of usage_plugin, which would be the obvious way to collect
// token counts. The host dispatches usage records asynchronously while still
// carrying the originating request's context, and native plugin clients refuse
// calls on a canceled context, so a native usage plugin receives nothing for
// HTTP-served requests. This plugin therefore reads tokens off the response
// itself, on the synchronous interception path, and commits them from the
// terminal lifecycle event — which the host does detach from cancellation.
type Capabilities struct {
	RequestInterceptor     bool `json:"request_interceptor"`
	RequestLifecyclePlugin bool `json:"request_lifecycle_plugin"`
	ResponseInterceptor    bool `json:"response_interceptor"`
	StreamChunkInterceptor bool `json:"response_stream_interceptor"`
	ManagementAPI          bool `json:"management_api"`
}

type RequestInterceptRequest struct {
	RequestID      string         `json:"RequestID"`
	SourceFormat   string         `json:"SourceFormat"`
	Model          string         `json:"Model"`
	RequestedModel string         `json:"RequestedModel"`
	Metadata       map[string]any `json:"Metadata"`
}

type RequestInterceptResponse struct {
	Terminate       bool        `json:"Terminate,omitempty"`
	StatusCode      int         `json:"StatusCode,omitempty"`
	ResponseHeaders http.Header `json:"ResponseHeaders,omitempty"`
	ResponseBody    []byte      `json:"ResponseBody,omitempty"`
}

type RequestCompletionOutcome string

const (
	RequestCompletionSucceeded RequestCompletionOutcome = "succeeded"
	RequestCompletionRejected  RequestCompletionOutcome = "rejected"
)

// RequestCompletion is the terminal event for an intercepted request. The host
// delivers exactly one per request and detaches it from request cancellation,
// which is what makes it a safe commit point for billing.
type RequestCompletion struct {
	RequestID string                   `json:"RequestID"`
	Outcome   RequestCompletionOutcome `json:"Outcome"`
}

type ResponseInterceptRequest struct {
	RequestID string `json:"RequestID"`
	Body      []byte `json:"Body"`
}

type StreamChunkInterceptRequest struct {
	RequestID  string `json:"RequestID"`
	Body       []byte `json:"Body"`
	ChunkIndex int    `json:"ChunkIndex"`
}

type ManagementRegistrationResponse struct {
	Routes    []ManagementRoute `json:"routes,omitempty"`
	Resources []ResourceRoute   `json:"resources,omitempty"`
}

// ManagementRoute is an authenticated route under /v0/management/.
// Paths are exact: the host rejects ':' and '*' segments.
type ManagementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}

// ResourceRoute is an unauthenticated browser-navigable GET route under
// /v0/resource/plugins/<pluginID>/. Menu makes it a panel sidebar entry.
type ResourceRoute struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}

type ManagementRequest struct {
	Method string     `json:"Method"`
	Path   string     `json:"Path"`
	Query  url.Values `json:"Query"`
	Body   []byte     `json:"Body"`
}

type ManagementResponse struct {
	StatusCode int         `json:"StatusCode,omitempty"`
	Headers    http.Header `json:"Headers,omitempty"`
	Body       []byte      `json:"Body,omitempty"`
}

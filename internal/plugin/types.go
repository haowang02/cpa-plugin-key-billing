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
	// ABIVersion and SchemaVersion identify the native ABI and the register
	// contract to the host, which refuses to load a plugin declaring either
	// beyond its own. Both mirror CLIProxyAPI's sdk/pluginabi.
	ABIVersion    uint32 = 1
	SchemaVersion uint32 = 2

	// StreamChunkHeaderInitIndex marks the synthetic chunk the host sends before
	// a stream starts, which carries headers rather than response data.
	StreamChunkHeaderInitIndex = -1
)

// Plugin identity. PluginID doubles as the dynamic library file name, the
// `plugins.configs` key, and the Management/resource route segment.
const (
	PluginID   = "cpa-key-billing"
	PluginName = "cpa-key-billing"
	Version    = "0.4.1"

	MenuLabel       = "API Key 计费"
	MenuDescription = "管理下游 API Key 的计费、订阅额度和用量"

	GitHubRepository = "https://github.com/haowang02/cpa-plugin-key-billing"
)

const (
	MethodPluginRegister    = "plugin.register"
	MethodPluginReconfigure = "plugin.reconfigure"

	MethodRequestInterceptBefore  = "request.intercept_before"
	MethodRequestInterceptAfter   = "request.intercept_after"
	MethodRequestComplete         = "request.complete"
	MethodResponseNormalizeBefore = "response.normalize_before"
	MethodResponseInterceptAfter  = "response.intercept_after"
	MethodResponseStreamChunk     = "response.intercept_stream_chunk"

	MethodUsageHandle = "usage.handle"

	MethodManagementRegister = "management.register"
	MethodManagementHandle   = "management.handle"
)

const (
	// MetadataCallerScope is sha256("cli-proxy-api:caller-scope:v1\x00"+apiKey)
	// in hex. It is the only downstream-key identifier available at interception
	// time, and it covers keys presented via query string as well as headers.
	MetadataCallerScope = "caller_scope"
	MetadataSource      = "source"
	// MetadataRequestPath is the inbound HTTP route the client called, such as
	// "/v1/messages". The host fills it from the matched route pattern, so a
	// wildcard route reports the pattern rather than the concrete path.
	MetadataRequestPath       = "request_path"
	MetadataSelectedAuthIndex = "selected_auth_index"
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
	ConfigYAML []byte `json:"config_yaml"`
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
	ConfigFields     []ConfigField `json:"ConfigFields"`
}

type ConfigField struct {
	Name        string `json:"Name"`
	Type        string `json:"Type"`
	Description string `json:"Description"`
}

type Capabilities struct {
	RequestInterceptor       bool `json:"request_interceptor"`
	RequestLifecyclePlugin   bool `json:"request_lifecycle_plugin"`
	ResponseBeforeTranslator bool `json:"response_before_translator"`
	ResponseInterceptor      bool `json:"response_interceptor"`
	StreamChunkInterceptor   bool `json:"response_stream_interceptor"`
	UsagePlugin              bool `json:"usage_plugin"`
	ManagementAPI            bool `json:"management_api"`
}

type RequestInterceptRequest struct {
	RequestID      string         `json:"RequestID"`
	SourceFormat   string         `json:"SourceFormat"`
	ToFormat       string         `json:"ToFormat"`
	Model          string         `json:"Model"`
	RequestedModel string         `json:"RequestedModel"`
	Stream         bool           `json:"Stream"`
	Body           []byte         `json:"Body"`
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
	RequestCompletionFailed    RequestCompletionOutcome = "failed"
	RequestCompletionRejected  RequestCompletionOutcome = "rejected"
	// RequestCompletionCanceled is the host's outcome for a canceled context,
	// which is what a client disconnecting mid-generation produces.
	RequestCompletionCanceled RequestCompletionOutcome = "canceled"
)

// RequestCompletion is the terminal event for an intercepted request. The host
// delivers exactly one per request and detaches it from request cancellation,
// which is what makes it a safe commit point for billing. It carries no tokens;
// those come from the response hooks.
type RequestCompletion struct {
	RequestID string                   `json:"RequestID"`
	Outcome   RequestCompletionOutcome `json:"Outcome"`
}

// Only credential identity is read from the host's usage event.
type UsageRecord struct {
	Provider  string `json:"Provider"`
	AuthIndex string `json:"AuthIndex"`
	AuthType  string `json:"AuthType"`
	// Source is the account behind the credential: an address for a signed-in
	// account, a plaintext API key for a provider from config.yaml.
	Source string `json:"Source"`
}

type ResponseTransformRequest struct {
	FromFormat      string `json:"FromFormat"`
	Model           string `json:"Model"`
	Stream          bool   `json:"Stream"`
	OriginalRequest []byte `json:"OriginalRequest"`
	Body            []byte `json:"Body"`
}

type ResponseInterceptRequest struct {
	RequestID  string `json:"RequestID"`
	Body       []byte `json:"Body"`
	ChunkIndex int    `json:"ChunkIndex"`
}

type ManagementRegistrationResponse struct {
	Routes    []ManagementRoute `json:"routes,omitempty"`
	Resources []ResourceRoute   `json:"resources,omitempty"`
}

type ManagementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description,omitempty"`
}

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

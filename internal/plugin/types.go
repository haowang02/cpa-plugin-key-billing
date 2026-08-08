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
	"time"

	"cpa-key-billing/internal/billing"
)

const (
	ABIVersion                  uint32 = 1
	SchemaVersion               uint32 = 3
	MinHostSchemaVersion        uint32 = 2
	CanonicalUsageSchemaVersion uint32 = 3
	StreamChunkHeaderInitIndex         = -1
)

// Plugin identity. PluginID doubles as the dynamic library file name, the
// `plugins.configs` key, and the Management/resource route segment.
const (
	PluginID   = "cpa-key-billing"
	PluginName = "cpa-key-billing"
	Version    = "0.1.6"

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

	MethodManagementRegister = "management.register"
	MethodManagementHandle   = "management.handle"
)

// Metadata keys the host places in Options.Metadata and forwards to interceptors.
const (
	// MetadataCallerScope is sha256("cli-proxy-api:caller-scope:v1\x00"+apiKey)
	// in hex. It is the only downstream-key identifier available at interception
	// time, and it covers keys presented via query string as well as headers.
	MetadataCallerScope = "caller_scope"
	MetadataSource      = "source"
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
	ConfigFields     []ConfigField `json:"ConfigFields"`
}

type ConfigField struct {
	Name        string `json:"Name"`
	Type        string `json:"Type"`
	Description string `json:"Description"`
}

// Capabilities declares the host integration points this plugin implements.
//
// Protocol 3 delivers canonical usage with the terminal lifecycle event.
// Protocol 2 uses the response hooks to read raw upstream usage instead.
type Capabilities struct {
	RequestInterceptor       bool `json:"request_interceptor"`
	RequestLifecyclePlugin   bool `json:"request_lifecycle_plugin"`
	ResponseBeforeTranslator bool `json:"response_before_translator"`
	ResponseInterceptor      bool `json:"response_interceptor"`
	StreamChunkInterceptor   bool `json:"response_stream_interceptor"`
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
	RequestCompletionRejected  RequestCompletionOutcome = "rejected"
)

// RequestCompletion is the terminal event for an intercepted request. The host
// delivers exactly one per request and detaches it from request cancellation,
// which is what makes it a safe commit point for billing.
type RequestCompletion struct {
	RequestID    string                   `json:"RequestID"`
	Outcome      RequestCompletionOutcome `json:"Outcome"`
	UsageRecords []RequestUsageRecord     `json:"UsageRecords"`
}

type RequestUsageRecord struct {
	Provider    string                `json:"Provider"`
	Model       string                `json:"Model"`
	Alias       string                `json:"Alias"`
	Generate    bool                  `json:"Generate"`
	Failed      bool                  `json:"Failed"`
	RequestedAt time.Time             `json:"RequestedAt"`
	Breakdown   RequestTokenBreakdown `json:"Breakdown"`
}

type RequestTokenBreakdown struct {
	SchemaVersion      int                            `json:"SchemaVersion"`
	Quality            billing.TokenAccountingQuality `json:"Quality"`
	TotalTokens        int64                          `json:"TotalTokens"`
	Input              RequestTokenInputBreakdown     `json:"Input"`
	Output             RequestTokenOutputBreakdown    `json:"Output"`
	UnclassifiedTokens int64                          `json:"UnclassifiedTokens"`
}

type RequestTokenInputBreakdown struct {
	TotalTokens      int64 `json:"TotalTokens"`
	UncachedTokens   int64 `json:"UncachedTokens"`
	CacheReadTokens  int64 `json:"CacheReadTokens"`
	CacheWriteTokens int64 `json:"CacheWriteTokens"`
}

type RequestTokenOutputBreakdown struct {
	TotalTokens        int64 `json:"TotalTokens"`
	NonReasoningTokens int64 `json:"NonReasoningTokens"`
	ReasoningTokens    int64 `json:"ReasoningTokens"`
}

type ResponseTransformRequest struct {
	FromFormat      string `json:"FromFormat"`
	Model           string `json:"Model"`
	Stream          bool   `json:"Stream"`
	OriginalRequest []byte `json:"OriginalRequest"`
	Body            []byte `json:"Body"`
}

type PayloadResponse struct {
	Body []byte `json:"Body"`
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

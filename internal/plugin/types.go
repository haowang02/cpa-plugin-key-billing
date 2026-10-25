// Package plugin implements the CLIProxyAPI plugin RPC contract.
package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

const (
	ABIVersion    uint32 = 1
	SchemaVersion uint32 = 6
)

// Plugin identity. PluginID doubles as the dynamic library file name, the
// `plugins.configs` key, and the Management/resource route segment.
const (
	PluginID   = "cpa-key-billing"
	PluginName = "cpa-key-billing"
	Version    = "1.4.10"

	MenuLabel       = "CPA Key Billing"
	MenuDescription = "Manage downstream API key routing, billing, concurrency limits, subscription quotas, and usage"

	GitHubRepository = "https://github.com/haowang02/cpa-plugin-key-billing"
)

const (
	MethodPluginRegister    = "plugin.register"
	MethodPluginReconfigure = "plugin.reconfigure"

	MethodRequestInterceptBefore = "request.intercept_before"
	MethodRequestInterceptAfter  = "request.intercept_after"
	MethodRequestComplete        = "request.complete"
	MethodSchedulerPick          = "scheduler.pick"
	MethodResponseInterceptAfter = "response.intercept_after"

	MethodUsageHandle = "usage.handle"

	MethodManagementRegister = "management.register"
	MethodManagementHandle   = "management.handle"
	MethodQuotaIdentifier    = "quota.identifier"
	MethodQuotaDescribe      = "quota.describe"
	MethodQuotaFetch         = "quota.fetch"
	MethodQuotaReset         = "quota.reset"
)

const (
	// MetadataCallerScope is sha256("cli-proxy-api:caller-scope:v1\x00"+apiKey)
	// in hex. It is the only downstream-key identifier available at interception
	// time, and it covers keys presented via query string as well as headers.
	MetadataCallerScope    = "caller_scope"
	MetadataGenerate       = "generate"
	MetadataSource         = "source"
	MetadataRequestPath    = "request_path"
	MetadataRequestedModel = "requested_model"
	MetadataSelectedAuth   = "selected_auth_id"
	MetadataSelectedIndex  = "selected_auth_index"
	// SourcePluginHostModelCallback is the MetadataSource value for nested
	// plugin-initiated executions, which bypass client model and quota checks.
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
	Logo             string        `json:"Logo"`
	ConfigFields     []ConfigField `json:"ConfigFields"`
}

type ConfigField struct {
	Name        string `json:"Name"`
	Type        string `json:"Type"`
	Default     any    `json:"Default"`
	Description string `json:"Description"`
}

type Capabilities struct {
	RequestInterceptor        bool `json:"request_interceptor"`
	ResponseInterceptor       bool `json:"response_interceptor"`
	RequestLifecyclePlugin    bool `json:"request_lifecycle_plugin"`
	UsagePlugin               bool `json:"usage_plugin"`
	ManagementAPI             bool `json:"management_api"`
	Scheduler                 bool `json:"scheduler"`
	SchedulerAcrossPriorities bool `json:"scheduler_across_priorities"`
	QuotaProvider             bool `json:"quota_provider"`
}

type SchedulerPickRequest struct {
	Provider string `json:"Provider"`
	Model    string `json:"Model"`
	Options  struct {
		Metadata map[string]any `json:"Metadata"`
	} `json:"Options"`
	Candidates []SchedulerAuthCandidate `json:"Candidates"`
}

type SchedulerAuthCandidate struct {
	ID         string            `json:"ID"`
	Provider   string            `json:"Provider"`
	Priority   int               `json:"Priority"`
	Status     string            `json:"Status"`
	Attributes map[string]string `json:"Attributes"`
}

type SchedulerPickResponse struct {
	AuthID  string `json:"AuthID,omitempty"`
	Handled bool   `json:"Handled"`
}

type RequestCompletion struct {
	RequestID  string `json:"RequestID"`
	Outcome    string `json:"Outcome"`
	StatusCode int    `json:"StatusCode"`
	Error      string `json:"Error"`
}

type RequestInterceptRequest struct {
	RequestID      string         `json:"RequestID"`
	TraceID        string         `json:"TraceID"`
	Headers        http.Header    `json:"Headers"`
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

// ResponseInterceptRequest holds the fields model catalog filtering reads.
type ResponseInterceptRequest struct {
	Model          string      `json:"Model"`
	RequestedModel string      `json:"RequestedModel"`
	RequestHeaders http.Header `json:"RequestHeaders"`
	Body           []byte      `json:"Body"`
}

// ResponseInterceptResponse leaves the response unchanged when Body is empty.
type ResponseInterceptResponse struct {
	Body []byte `json:"Body,omitempty"`
}

type UsageRecord struct {
	RequestID           string        `json:"RequestID"`
	TraceID             string        `json:"TraceID"`
	Provider            string        `json:"Provider"`
	BaseURL             string        `json:"BaseURL"`
	ExecutorType        string        `json:"ExecutorType"`
	Model               string        `json:"Model"`
	Alias               string        `json:"Alias"`
	APIKey              string        `json:"APIKey"`
	IsNativeKey         *bool         `json:"IsNativeKey"`
	AccessProvider      string        `json:"AccessProvider"`
	SessionID           string        `json:"SessionID"`
	ParentSessionID     string        `json:"ParentSessionID"`
	AuthID              string        `json:"AuthID"`
	AuthIndex           string        `json:"AuthIndex"`
	AuthType            string        `json:"AuthType"`
	Source              string        `json:"Source"`
	ReasoningEffort     string        `json:"ReasoningEffort"`
	ServiceTier         string        `json:"ServiceTier"`
	ResponseServiceTier string        `json:"ResponseServiceTier"`
	ResponseModel       string        `json:"ResponseModel"`
	ResponseHeaders     http.Header   `json:"ResponseHeaders"`
	Generate            bool          `json:"Generate"`
	Stream              *bool         `json:"Stream"`
	RequestedAt         time.Time     `json:"RequestedAt"`
	Latency             time.Duration `json:"Latency"`
	TTFT                time.Duration `json:"TTFT"`
	Failed              bool          `json:"Failed"`
	Failure             UsageFailure  `json:"Failure"`
	Detail              UsageDetail   `json:"Detail"`
}

type UsageFailure struct {
	StatusCode int    `json:"StatusCode"`
	Body       string `json:"Body"`
}

type UsageDetail struct {
	InputTokens         int64 `json:"InputTokens"`
	OutputTokens        int64 `json:"OutputTokens"`
	ReasoningTokens     int64 `json:"ReasoningTokens"`
	CachedTokens        int64 `json:"CachedTokens"`
	CacheReadTokens     int64 `json:"CacheReadTokens"`
	CacheCreationTokens int64 `json:"CacheCreationTokens"`
	TotalTokens         int64 `json:"TotalTokens"`
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
	Method         string      `json:"Method"`
	Path           string      `json:"Path"`
	Headers        http.Header `json:"Headers"`
	Query          url.Values  `json:"Query"`
	Body           []byte      `json:"Body"`
	HostCallbackID string      `json:"host_callback_id,omitempty"`
}

type ManagementResponse struct {
	StatusCode int         `json:"StatusCode,omitempty"`
	Headers    http.Header `json:"Headers,omitempty"`
	Body       []byte      `json:"Body,omitempty"`
}

type QuotaRequest struct {
	AuthIndex      string `json:"auth_index"`
	AuthID         string `json:"auth_id"`
	Provider       string `json:"provider"`
	HostCallbackID string `json:"host_callback_id"`
}

type QuotaResponse struct {
	Subscription *QuotaSubscription `json:"subscription,omitempty"`
	Summary      []QuotaMetric      `json:"summary,omitempty"`
	Groups       []QuotaGroup       `json:"groups,omitempty"`
}
type QuotaSubscription struct {
	Plan string `json:"plan,omitempty"`
}
type QuotaMetric struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Value    float64 `json:"value"`
	Format   string  `json:"format,omitempty"`
	Currency string  `json:"currency,omitempty"`
}
type QuotaGroup struct {
	DisplayName string        `json:"displayName,omitempty"`
	Buckets     []QuotaBucket `json:"buckets,omitempty"`
}
type QuotaBucket struct {
	Window            string  `json:"window,omitempty"`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime,omitempty"`
}

type QuotaDescription struct {
	SupportedProviders []string `json:"supported_providers"`
	DisplayName        string   `json:"display_name"`
	SupportsReset      bool     `json:"supports_reset"`
}
type QuotaResetResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

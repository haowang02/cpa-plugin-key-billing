package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"

	"cpa-key-billing/internal/billing"
)

type App struct {
	store      *billing.Store
	hostSchema atomic.Uint32
	protocol2  *protocol2UsageTracker
}

func NewApp() *App {
	return &App{
		store:     billing.NewStore(),
		protocol2: newProtocol2UsageTracker(),
	}
}

// HandleMethod dispatches one host RPC call. A panic anywhere below is
// converted into an error envelope: the host fuses a panicking plugin, and
// taking the whole proxy down over a billing bug is not an acceptable trade.
func (a *App) HandleMethod(method string, request []byte) (response []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			response = nil
			err = fmt.Errorf("插件处理 %s 时发生异常：%v", method, recovered)
		}
	}()
	response, err = a.handleMethod(method, request)
	if err == nil {
		switch method {
		case MethodPluginRegister, MethodPluginReconfigure, MethodRequestComplete, MethodUsageHandle, MethodManagementHandle:
			// These calls are off the request interception path. They drive
			// persistence because a c-shared plugin must not run a background
			// flusher; see billing.Store.
			a.store.FlushIfDue()
		}
	}
	return response, err
}

func (a *App) handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case MethodPluginRegister, MethodPluginReconfigure:
		if errConfigure := a.configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return OKEnvelope(registration(a.hostSchema.Load()))
	case MethodRequestInterceptBefore:
		return a.interceptBeforeAuth(request)
	case MethodRequestInterceptAfter:
		return a.interceptAfterAuth(request)
	case MethodRequestComplete:
		return a.handleRequestComplete(request)
	case MethodUsageHandle:
		return a.handleUsage(request)
	case MethodResponseNormalizeBefore:
		return a.normalizeResponseBefore(request)
	case MethodResponseInterceptAfter:
		return a.interceptResponseAfter(request)
	case MethodResponseStreamChunk:
		return a.interceptResponseStreamChunk(request)
	case MethodManagementRegister:
		return OKEnvelope(managementRegistration())
	case MethodManagementHandle:
		return a.handleManagement(request)
	default:
		return ErrorEnvelope("unknown_method", "不支持的插件方法："+method, http.StatusNotFound), nil
	}
}

func (a *App) Shutdown() {
	if a == nil || a.store == nil {
		return
	}
	a.store.Close()
}

func (a *App) configure(raw []byte) error {
	var req LifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return fmt.Errorf("解析插件生命周期请求：%w", errUnmarshal)
		}
	}
	if req.SchemaVersion < MinHostSchemaVersion {
		return fmt.Errorf("%s 需要宿主插件协议版本不低于 %d，当前版本为 %d",
			PluginID, MinHostSchemaVersion, req.SchemaVersion)
	}
	cfg, errDecode := billing.DecodeConfig(req.ConfigYAML)
	if errDecode != nil {
		return errDecode
	}
	if errConfigure := a.store.Configure(cfg); errConfigure != nil {
		return errConfigure
	}
	a.hostSchema.Store(req.SchemaVersion)
	return nil
}

func registration(hostSchema uint32) Registration {
	negotiatedSchema := negotiatedSchema(hostSchema)
	protocol2Usage := negotiatedSchema < CanonicalUsageSchemaVersion
	return Registration{
		SchemaVersion: negotiatedSchema,
		Metadata: Metadata{
			Name:             PluginName,
			Version:          Version,
			Author:           PluginName,
			GitHubRepository: GitHubRepository,
			ConfigFields: []ConfigField{
				{
					Name:        "enabled",
					Type:        "boolean",
					Description: "启用 API Key 计费和订阅额度控制。",
				},
				{
					Name:        "state_file",
					Type:        "string",
					Description: "计费状态 JSON 文件路径。",
				},
				{
					Name:        "log_entries",
					Type:        "integer",
					Description: "保留的计费日志条数；设为 0 可关闭日志。",
				},
			},
		},
		Capabilities: Capabilities{
			RequestInterceptor:       true,
			RequestLifecyclePlugin:   true,
			ResponseBeforeTranslator: protocol2Usage,
			ResponseInterceptor:      protocol2Usage,
			StreamChunkInterceptor:   protocol2Usage,
			UsagePlugin:              true,
			ManagementAPI:            true,
		},
	}
}

func negotiatedSchema(hostSchema uint32) uint32 {
	if hostSchema > SchemaVersion {
		return SchemaVersion
	}
	return hostSchema
}

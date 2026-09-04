package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/sqlite"
)

type App struct {
	store                 *billing.Store
	hostCaller            HostCaller
	routingMu             sync.Mutex
	credentials           map[string]credentialView
	credentialsByRawID    map[string]string
	credentialRefsByIndex map[string]string
	syncedCredentialRefs  map[string]struct{}
	scheduler             subsetScheduler
	pending               map[string]pendingRouteLog
	pendingSequence       uint64
}

func (a *App) SetHostCaller(caller HostCaller) {
	a.hostCaller = caller
}

func NewApp() *App {
	return &App{
		store:                 billing.NewStore(openRepository),
		credentials:           make(map[string]credentialView),
		credentialsByRawID:    make(map[string]string),
		credentialRefsByIndex: make(map[string]string),
		syncedCredentialRefs:  make(map[string]struct{}),
		pending:               make(map[string]pendingRouteLog),
	}
}

func openRepository(path string) (billing.Repository, error) {
	return sqlite.Open(path)
}

// HandleMethod dispatches one host RPC call. A panic anywhere below is
// converted into an error envelope: the host fuses a panicking plugin, and
// taking the whole proxy down over a billing bug is not an acceptable trade.
func (a *App) HandleMethod(method string, request []byte) (response []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			response = nil
			err = fmt.Errorf("插件处理 %s 时发生异常：%v", method, recovered)
			if a != nil && a.store != nil {
				a.store.AddPluginLog(billing.PluginLogError, "%v", err)
			}
		}
	}()
	return a.handleMethod(method, request)
}

func (a *App) handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case MethodPluginRegister, MethodPluginReconfigure:
		if errConfigure := a.configure(request); errConfigure != nil {
			a.store.AddPluginLog(billing.PluginLogError, "应用插件配置失败：%v", errConfigure)
			return nil, errConfigure
		}
		return OKEnvelope(registration())
	case MethodRequestInterceptBefore:
		return a.interceptBeforeAuth(request)
	case MethodRequestInterceptAfter:
		return a.interceptAfterAuth(request)
	case MethodRequestComplete:
		return a.completeRequest(request)
	case MethodSchedulerPick:
		return a.pickCredential(request)
	case MethodUsageHandle:
		return a.handleUsage(request)
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
	cfg, errDecode := billing.DecodeConfig(req.ConfigYAML)
	if errDecode != nil {
		return errDecode
	}
	if errConfigure := a.store.Configure(cfg); errConfigure != nil {
		return errConfigure
	}
	if _, errCatalog := billing.EnsureBuiltinCatalog(); errCatalog != nil {
		a.store.AddPluginLog(billing.PluginLogError, "加载 models.dev 参考价目录失败：%v", errCatalog)
	}
	return nil
}

func registration() Registration {
	return Registration{
		SchemaVersion: SchemaVersion,
		Metadata: Metadata{
			Name:             PluginName,
			Version:          Version,
			Author:           PluginName,
			GitHubRepository: GitHubRepository,
			ConfigFields: []ConfigField{
				{
					Name:        "enabled",
					Type:        "boolean",
					Description: "启用 API Key 路由、计费、并发限制和订阅额度控制。",
				},
				{
					Name:        "state_file",
					Type:        "string",
					Description: "计费数据库文件路径。",
				},
			},
		},
		Capabilities: Capabilities{
			RequestInterceptor:     true,
			RequestLifecyclePlugin: true,
			UsagePlugin:            true,
			ManagementAPI:          true,
			Scheduler:              true,
		},
	}
}

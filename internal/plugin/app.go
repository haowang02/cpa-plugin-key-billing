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
	admissionsMu          sync.Mutex
	admissions            map[string]*requestAdmission
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
	return newApp(billing.NewStore(openRepository, nil))
}

func newApp(store *billing.Store) *App {
	return &App{
		store:                 store,
		admissions:            make(map[string]*requestAdmission),
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
			err = fmt.Errorf("插件调用 %s 异常：%v", method, recovered)
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
	// Refresh records its result; a download failure does not disable custom prices.
	_, _ = a.store.EnsureReferencePrices()
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
					Name:        "debug",
					Type:        "boolean",
					Description: "记录 debug 日志，包括路由日志和参考价匹配日志",
				},
				{
					Name:        "codex_fast_mode_billing",
					Type:        "boolean",
					Description: "请求 Codex 上游时指定 priority 档位，按 2.5 倍计费",
				},
				{
					Name:        "codex_fast_mode_billing_excluded_models",
					Type:        "string",
					Description: "不叠加 Codex Fast 2.5 倍计费的模型 ID，逗号分隔；仅影响计费，不改变请求档位",
				},
				{
					Name:        "state_file",
					Type:        "string",
					Description: "计费数据库文件路径",
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

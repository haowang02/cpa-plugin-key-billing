package plugin

import (
	"container/list"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"sync"
	"sync/atomic"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/sqlite"
)

type App struct {
	store                 *billing.Store
	hostCaller            HostCaller
	requestOrigins        map[requestOriginKey]*list.Element
	requestOriginOrder    list.List
	requestOriginsMu      sync.Mutex
	routingMu             sync.Mutex
	credentials           map[string]credentialView
	credentialIdentities  map[string]billing.CredentialProviderSelector
	credentialRefsByIndex map[string]string
	scheduler             subsetScheduler
	pending               map[string]pendingRouteLog
	pendingSequence       uint64
	authModelsSupported   atomic.Bool
}

func (a *App) SetHostCaller(caller HostCaller) {
	a.hostCaller = caller
}

func NewApp() *App {
	return newApp(nil)
}

func newApp(store *billing.Store) *App {
	app := &App{
		store:                 store,
		requestOrigins:        make(map[requestOriginKey]*list.Element),
		credentials:           make(map[string]credentialView),
		credentialIdentities:  make(map[string]billing.CredentialProviderSelector),
		credentialRefsByIndex: make(map[string]string),
		pending:               make(map[string]pendingRouteLog),
	}
	if app.store == nil {
		app.store = billing.NewStore(openRepository, app.downloadReferencePrices)
	}
	return app
}

func openRepository(path string) (billing.Repository, error) {
	return sqlite.Open(path, quotaHeaderReport)
}

// HandleMethod keeps panics from crossing the plugin's C ABI boundary.
func (a *App) HandleMethod(method string, request []byte) (response []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			response = nil
			err = fmt.Errorf("Plugin call %s panicked: %v", method, recovered)
			a.store.AddPluginLog(billing.PluginLogError, "%v", err)
		}
	}()
	switch method {
	case MethodPluginRegister, MethodPluginReconfigure:
		if errConfigure := a.configure(request); errConfigure != nil {
			a.store.AddPluginLog(billing.PluginLogError, "Failed to apply plugin configuration: %v", errConfigure)
			return nil, errConfigure
		}
		return OKEnvelope(a.registration())
	case MethodRequestInterceptBefore:
		return a.interceptBeforeAuth(request)
	case MethodRequestInterceptAfter:
		return a.interceptAfterAuth(request)
	case MethodRequestComplete:
		return a.completeRequest(request)
	case MethodResponseInterceptAfter:
		return a.interceptResponse(request)
	case MethodSchedulerPick:
		return a.pickCredential(request)
	case MethodUsageHandle:
		return a.handleUsage(request)
	case MethodQuotaIdentifier:
		return OKEnvelope(map[string]string{"identifier": PluginID})
	case MethodQuotaDescribe:
		return OKEnvelope(QuotaDescription{SupportedProviders: authQuotaProviders, DisplayName: MenuLabel, SupportsReset: true})
	case MethodQuotaFetch, MethodQuotaReset:
		return a.handleQuota(method, request)
	case MethodManagementRegister:
		return OKEnvelope(managementRegistration())
	case MethodManagementHandle:
		return a.handleManagement(request)
	default:
		return ErrorEnvelope("unknown_method", "Unsupported plugin method: "+method, http.StatusNotFound), nil
	}
}

func (a *App) Shutdown() {
	a.store.Close()
}

func (a *App) configure(raw []byte) error {
	var req LifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return fmt.Errorf("Parse plugin lifecycle request: %w", errUnmarshal)
		}
	}
	cfg, errDecode := billing.DecodeConfig(req.ConfigYAML)
	if errDecode != nil {
		return errDecode
	}
	if errConfigure := func() error {
		a.routingMu.Lock()
		defer a.routingMu.Unlock()
		previous := a.store.ConfigCredentials()
		if err := a.store.Configure(cfg); err != nil {
			return err
		}
		a.scheduler.configure(cfg.Routing, cfg.Enabled)
		if loaded := a.store.ConfigCredentials(); !maps.EqualFunc(previous, loaded, func(left, right billing.ConfigCredential) bool {
			return left.ConfigCredentialDetails == right.ConfigCredentialDetails
		}) {
			a.replaceSyncedCredentials(previous, loaded)
		}
		return nil
	}(); errConfigure != nil {
		return errConfigure
	}
	a.authModelsSupported.Store(a.probeHostAuthModels())
	// Refresh records its result; a download failure does not disable custom prices.
	_, _ = a.store.EnsureReferencePrices()
	return nil
}

func (a *App) registration() Registration {
	defaults := billing.DefaultConfig()
	return Registration{
		SchemaVersion: SchemaVersion,
		Metadata: Metadata{
			Name:             PluginName,
			Version:          Version,
			Author:           PluginName,
			GitHubRepository: GitHubRepository,
			Logo:             pluginLogo,
			ConfigFields: []ConfigField{
				{
					Name:        "routing",
					Default:     defaults.Routing,
					Type:        "object",
					Description: "Credential routing: strategy (round-robin, weighted-round-robin, fill-first), session-affinity, and session-affinity-ttl. Applies to credential-restricted routes.",
				},
				{
					Name:        "debug",
					Default:     defaults.Debug,
					Type:        "boolean",
					Description: "Record debug logs, including routing and reference price matching",
				},
				{
					Name:        "codex_fast_mode_billing",
					Default:     defaults.CodexFastModeBilling,
					Type:        "boolean",
					Description: "Bill Codex priority requests at 2.5 times the standard cost",
				},
				{
					Name:        "allow_unpriced_models",
					Default:     defaults.AllowUnpricedModels,
					Type:        "boolean",
					Description: "Admit requests for models without a price and record their usage at zero cost",
				},
				{
					Name:        "cny_per_usd",
					Default:     defaults.CNYPerUSD,
					Type:        "number",
					Description: "Exchange rate in CNY per USD, used to convert amounts between currencies",
				},
				{
					Name:        "mask_api_key_view_emails",
					Default:     defaults.MaskAPIKeyViewEmails,
					Type:        "boolean",
					Description: "Mask email addresses in API key account responses",
				},
				{
					Name:        "allow_api_key_quota_reset",
					Default:     defaults.AllowAPIKeyQuotaReset,
					Type:        "boolean",
					Description: "Allow API key users to reset Codex and Claude auth file quotas using upstream resets",
				},
				{
					Name:        "state_file",
					Default:     defaults.StateFile,
					Type:        "string",
					Description: "Billing database file path",
				},
			},
		},
		Capabilities: Capabilities{
			RequestInterceptor:        true,
			ResponseInterceptor:       a.filtersModelCatalogs(),
			RequestLifecyclePlugin:    true,
			UsagePlugin:               true,
			ManagementAPI:             true,
			Scheduler:                 true,
			SchedulerAcrossPriorities: true,
			QuotaProvider:             true,
		},
	}
}

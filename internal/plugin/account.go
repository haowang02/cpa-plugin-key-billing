package plugin

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

type accountIdentity struct {
	Preview string `json:"preview,omitempty"`
	Label   string `json:"label,omitempty"`
}

type accountSubscription struct {
	Name         string    `json:"name,omitempty"`
	Unlimited    bool      `json:"unlimited"`
	Blocked      bool      `json:"blocked"`
	LimitUSD     float64   `json:"limit_usd"`
	SpentUSD     float64   `json:"spent_usd"`
	RemainingUSD float64   `json:"remaining_usd"`
	UsedPercent  float64   `json:"used_percent"`
	CycleEndAt   time.Time `json:"cycle_end_at,omitzero"`
}

type accountConcurrency struct {
	Limit   int `json:"limit"`
	Current int `json:"current"`
}

type accountAccessResponse struct {
	Role         string                   `json:"role"`
	Models       []string                 `json:"models"`
	Credentials  []accountRouteCredential `json:"credentials"`
	RoutingValid bool                     `json:"routing_valid"`
	Warnings     []string                 `json:"warnings"`
}

type accountRouteCredential struct {
	Source       string `json:"source,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Name         string `json:"name,omitempty"`
	Status       string `json:"status"`
	ProviderWide bool   `json:"provider_wide,omitempty"`
}

type accountStatusResponse struct {
	Role         string              `json:"role"`
	Tracked      bool                `json:"tracked"`
	Identity     accountIdentity     `json:"identity"`
	Subscription accountSubscription `json:"subscription"`
	Concurrency  accountConcurrency  `json:"concurrency"`
}

func (a *App) accountStatus(access viewAccess) ManagementResponse {
	if !access.Tracked {
		return apiKeyJSON(http.StatusOK, accountStatusResponse{Role: "api_key", Tracked: false})
	}
	view := access.Key
	remaining := view.LimitUSD - view.SpentUSD
	if remaining < 0 {
		remaining = 0
	}
	return apiKeyJSON(http.StatusOK, accountStatusResponse{
		Role:     "api_key",
		Tracked:  true,
		Identity: accountIdentity{Preview: view.Preview, Label: view.Label},
		Subscription: accountSubscription{
			Name: view.PlanName, Unlimited: view.Unlimited, Blocked: view.Blocked,
			LimitUSD: view.LimitUSD, SpentUSD: view.SpentUSD, RemainingUSD: remaining,
			UsedPercent: view.UsedPercent, CycleEndAt: view.CycleEndAt,
		},
		Concurrency: accountConcurrency{Limit: view.ConcurrencyLimit, Current: view.CurrentConcurrency},
	})
}

func (a *App) accountAccess(access viewAccess) ManagementResponse {
	if !access.Tracked {
		return apiKeyJSON(http.StatusOK, accountAccessResponse{Role: "api_key", Models: []string{}, Credentials: []accountRouteCredential{}, RoutingValid: true, Warnings: []string{}})
	}
	warnings := []string{}
	if err := a.refreshCredentialInventory(); err != nil {
		warnings = append(warnings, "上游凭证信息暂时无法加载")
	}
	decision := a.store.ResolveRouting(access.Scope, "", "")
	if decision.ConfigurationError != "" {
		warnings = append(warnings, "路由规则已不存在，请联系管理员")
	}
	inventory := a.credentialInventory()
	byRef := map[string]credentialView{}
	for _, item := range inventory {
		byRef[item.Ref] = item
	}
	credentials := make([]accountRouteCredential, 0, len(decision.CredentialIDs)+len(decision.CredentialProviders))
	seen := map[string]struct{}{}
	add := func(key string, item accountRouteCredential) {
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		credentials = append(credentials, item)
	}
	for _, ref := range decision.CredentialIDs {
		if credential, ok := byRef[ref]; ok {
			add(credential.Ref, accountCredential(credential))
		} else {
			add("missing", accountRouteCredential{Name: "指定上游凭证当前不可用", Status: "missing"})
		}
	}
	for _, selector := range decision.CredentialProviders {
		matched := false
		for _, credential := range inventory {
			if credential.Source == selector.Source && strings.EqualFold(credential.Provider, selector.Provider) {
				add(credential.Ref, accountCredential(credential))
				matched = true
			}
		}
		if matched {
			continue
		}
		name := sourceLabel(selector.Source) + " · " + selector.Provider
		add("provider\x00"+selector.Source+"\x00"+selector.Provider, accountRouteCredential{
			Source: selector.Source, Provider: selector.Provider, Status: "missing", ProviderWide: true,
		})
		warnings = append(warnings, "当前没有符合「"+name+"」的上游凭证")
	}
	sort.Slice(credentials, func(i, j int) bool {
		left := credentials[i].Source + "\x00" + credentials[i].Provider + "\x00" + credentials[i].Name
		right := credentials[j].Source + "\x00" + credentials[j].Provider + "\x00" + credentials[j].Name
		return left < right
	})
	return apiKeyJSON(http.StatusOK, accountAccessResponse{
		Role: "api_key", Models: decision.ModelScope, Credentials: credentials,
		RoutingValid: decision.ConfigurationError == "", Warnings: warnings,
	})
}

func accountCredential(item credentialView) accountRouteCredential {
	status := item.Status
	if status == "" {
		status = "active"
	}
	return accountRouteCredential{Source: item.Source, Provider: item.Provider, Name: item.DisplayName, Status: status}
}
func sourceLabel(source string) string {
	if source == billing.CredentialSourceAuthFiles {
		return "认证文件"
	}
	if source == billing.CredentialSourceAIProviders {
		return "AI 供应商"
	}
	return source
}
func accountScope(headers http.Header) (string, bool) {
	values := headers.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 8192 {
		return "", false
	}
	scope := billing.CallerScope(parts[1])
	return scope, scope != ""
}

func apiKeyUnauthorized() ManagementResponse {
	response := apiKeyJSONError(http.StatusUnauthorized, "unauthorized", "API Key 无效")
	response.Headers.Set("WWW-Authenticate", `Bearer realm="cpa-key-billing-account"`)
	return response
}

func apiKeyJSON(status int, payload any) ManagementResponse {
	response := JSONResponse(status, payload)
	secureAPIKeyResponse(&response)
	return response
}

func apiKeyJSONError(status int, code, message string) ManagementResponse {
	response := JSONError(status, code, message)
	secureAPIKeyResponse(&response)
	return response
}

func secureAPIKeyResponse(response *ManagementResponse) {
	response.Headers.Set("Cache-Control", "private, no-store")
	response.Headers.Set("Pragma", "no-cache")
	response.Headers.Set("Vary", "Authorization")
	response.Headers.Set("Referrer-Policy", "no-referrer")
	response.Headers.Set("X-Content-Type-Options", "nosniff")
}

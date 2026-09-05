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
	Tracked      bool                     `json:"tracked"`
	Identity     accountIdentity          `json:"identity"`
	Subscription accountSubscription      `json:"subscription"`
	Concurrency  accountConcurrency       `json:"concurrency"`
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

func (a *App) accountAccess(access viewAccess) ManagementResponse {
	response := accountAccessResponse{
		Tracked: access.Tracked, Models: []string{}, Credentials: []accountRouteCredential{},
		RoutingValid: true, Warnings: []string{},
	}
	if !access.Tracked {
		return apiKeyJSON(http.StatusOK, response)
	}
	view := access.Key
	remaining := view.LimitUSD - view.SpentUSD
	if remaining < 0 {
		remaining = 0
	}
	response.Identity = accountIdentity{Preview: view.Preview, Label: view.Label}
	response.Subscription = accountSubscription{
		Name: view.PlanName, Unlimited: view.Unlimited, Blocked: view.Blocked,
		LimitUSD: view.LimitUSD, SpentUSD: view.SpentUSD, RemainingUSD: remaining,
		UsedPercent: view.UsedPercent, CycleEndAt: view.CycleEndAt,
	}
	response.Concurrency = accountConcurrency{Limit: view.ConcurrencyLimit, Current: view.CurrentConcurrency}
	decision := a.store.ResolveRouting(access.Scope, "", "")
	response.Models = decision.ModelScope
	response.RoutingValid = decision.ConfigurationError == ""
	if decision.ConfigurationError != "" {
		response.Warnings = append(response.Warnings, "路由规则已不存在，请联系管理员")
	}
	if !decision.RestrictsCredentials() {
		return apiKeyJSON(http.StatusOK, response)
	}
	if err := a.refreshCredentialInventory(); err != nil {
		response.Warnings = append(response.Warnings, "上游凭证加载失败")
	}
	inventory := a.credentialInventory()
	byRef := map[string]credentialView{}
	for _, item := range inventory {
		byRef[item.Ref] = item
	}
	credentials := make([]accountRouteCredential, 0, len(decision.CredentialIDs)+len(decision.CredentialProviders))
	seenRefs := map[string]struct{}{}
	addCredential := func(item credentialView) {
		if _, exists := seenRefs[item.Ref]; exists {
			return
		}
		seenRefs[item.Ref] = struct{}{}
		credentials = append(credentials, accountCredential(item))
	}
	missingCredential := false
	for _, ref := range decision.CredentialIDs {
		if credential, ok := byRef[ref]; ok {
			addCredential(credential)
		} else if !missingCredential {
			credentials = append(credentials, accountRouteCredential{Name: "指定上游凭证不可用", Status: "missing"})
			missingCredential = true
		}
	}
	for _, selector := range decision.CredentialProviders {
		matched := false
		for _, credential := range inventory {
			if credential.Source == selector.Source && credential.Provider == selector.Provider {
				addCredential(credential)
				matched = true
			}
		}
		if matched {
			continue
		}
		name := sourceLabel(selector.Source) + " · " + selector.Provider
		credentials = append(credentials, accountRouteCredential{
			Source: selector.Source, Provider: selector.Provider, Status: "missing", ProviderWide: true,
		})
		response.Warnings = append(response.Warnings, "没有匹配「"+name+"」的上游凭证")
	}
	sort.Slice(credentials, func(i, j int) bool {
		left, right := credentials[i], credentials[j]
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		return left.Name < right.Name
	})
	response.Credentials = credentials
	return apiKeyJSON(http.StatusOK, response)
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

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

type accountModelAccess struct {
	Role      string   `json:"role"`
	AllModels bool     `json:"all_models"`
	Models    []string `json:"models"`
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
		return apiKeyJSON(http.StatusOK, accountModelAccess{Role: "api_key", Models: []string{}})
	}
	models := accountModels(access.Key, a.store.ModelGroups())
	return apiKeyJSON(http.StatusOK, accountModelAccess{Role: "api_key", AllModels: access.Key.AllModels, Models: models})
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

func accountModels(view billing.KeyView, groups []billing.ModelGroup) []string {
	if view.AllModels {
		return []string{}
	}
	models := append([]string(nil), view.Models...)
	selectedGroups := make(map[string]struct{}, len(view.ModelGroupIDs))
	for _, id := range view.ModelGroupIDs {
		selectedGroups[id] = struct{}{}
	}
	for _, group := range groups {
		if _, selected := selectedGroups[group.ID]; selected {
			models = append(models, group.Models...)
		}
	}
	sort.Strings(models)
	result := models[:0]
	for _, value := range models {
		value = strings.TrimSpace(value)
		if value == "" || (len(result) > 0 && result[len(result)-1] == value) {
			continue
		}
		result = append(result, value)
	}
	return result
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

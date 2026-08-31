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
	PeriodKind   string    `json:"period_kind,omitempty"`
	CycleEndAt   time.Time `json:"cycle_end_at,omitzero"`
}

type accountConcurrency struct {
	Limit   int `json:"limit"`
	Current int `json:"current"`
}

type accountModelAccess struct {
	AllModels bool     `json:"all_models"`
	Models    []string `json:"models"`
}

type accountOverviewResponse struct {
	Tracked      bool                  `json:"tracked"`
	Identity     accountIdentity       `json:"identity"`
	Subscription accountSubscription   `json:"subscription"`
	Concurrency  accountConcurrency    `json:"concurrency"`
	ModelAccess  accountModelAccess    `json:"model_access"`
	ByModel      []billing.ModelTotals `json:"by_model"`
}

type accountLogEntry struct {
	At                time.Time                      `json:"at"`
	BillingModel      string                         `json:"billing_model,omitempty"`
	ExecutorType      string                         `json:"executor_type,omitempty"`
	Source            string                         `json:"source,omitempty"`
	ReasoningEffort   string                         `json:"reasoning_effort,omitempty"`
	ServiceTier       string                         `json:"service_tier,omitempty"`
	Failed            bool                           `json:"failed"`
	LatencyMS         int64                          `json:"latency_ms,omitempty"`
	TTFTMS            int64                          `json:"ttft_ms,omitempty"`
	AccountingQuality billing.TokenAccountingQuality `json:"accounting_quality,omitempty"`
	TotalUSD          float64                        `json:"total_usd"`
	UncachedInput     int64                          `json:"uncached_input_tokens"`
	CacheRead         int64                          `json:"cache_read_tokens"`
	CacheWrite        int64                          `json:"cache_write_tokens"`
	Output            int64                          `json:"output_tokens"`
	Reasoning         int64                          `json:"reasoning_tokens,omitempty"`
}

type accountLogView struct {
	Entries  []accountLogEntry       `json:"entries"`
	Total    int                     `json:"total"`
	Statuses billing.LogStatusCounts `json:"status_counts"`
}

func (a *App) accountOverview(req ManagementRequest) ManagementResponse {
	scope, ok := accountScope(req.Headers)
	if !ok {
		return accountUnauthorized()
	}
	view, tracked := a.store.KeyViewForScope(scope)
	if !tracked {
		return accountJSON(http.StatusOK, accountOverviewResponse{
			Tracked:     false,
			ModelAccess: accountModelAccess{Models: []string{}},
			ByModel:     []billing.ModelTotals{},
		})
	}
	remaining := view.LimitUSD - view.SpentUSD
	if remaining < 0 {
		remaining = 0
	}
	byModel := view.ByModel
	if byModel == nil {
		byModel = []billing.ModelTotals{}
	}
	var periodKind string
	for _, plan := range a.store.Plans() {
		if plan.ID == view.PlanID {
			periodKind = string(plan.Period.Kind)
			break
		}
	}
	models := accountModels(view, a.store.ModelGroups())
	return accountJSON(http.StatusOK, accountOverviewResponse{
		Tracked:  true,
		Identity: accountIdentity{Preview: view.Preview, Label: view.Label},
		Subscription: accountSubscription{
			Name: view.PlanName, Unlimited: view.Unlimited, Blocked: view.Blocked,
			LimitUSD: view.LimitUSD, SpentUSD: view.SpentUSD, RemainingUSD: remaining,
			UsedPercent: view.UsedPercent, PeriodKind: periodKind,
			CycleEndAt: view.CycleEndAt,
		},
		Concurrency: accountConcurrency{Limit: view.ConcurrencyLimit, Current: view.CurrentConcurrency},
		ModelAccess: accountModelAccess{AllModels: len(models) == 0, Models: models},
		ByModel:     byModel,
	})
}

func (a *App) accountPrices(req ManagementRequest) ManagementResponse {
	scope, ok := accountScope(req.Headers)
	if !ok {
		return accountUnauthorized()
	}
	view, tracked := a.store.KeyViewForScope(scope)
	if !tracked {
		return accountJSON(http.StatusOK, []billing.PriceRule{})
	}
	models := accountModels(view, a.store.ModelGroups())
	return accountJSON(http.StatusOK, a.store.PricesForModels(models))
}

func (a *App) accountLogs(req ManagementRequest) ManagementResponse {
	scope, ok := accountScope(req.Headers)
	if !ok {
		return accountUnauthorized()
	}
	if _, tracked := a.store.KeyViewForScope(scope); !tracked {
		return accountJSON(http.StatusOK, accountLogView{Entries: []accountLogEntry{}})
	}
	query := billing.LogQuery{
		Scope: scope, VisibleFieldsOnly: true, Search: req.Query.Get("q"),
		Status: strings.TrimSpace(req.Query.Get("status")), Limit: defaultLogPageSize,
	}
	if !billing.ValidLogStatus(query.Status) {
		return accountJSONError(http.StatusBadRequest, "invalid", "status 无效")
	}
	if errOffset := countParam(req.Query, "offset", &query.Offset); errOffset != nil {
		return accountErrorResponse(errOffset)
	}
	if errLimit := countParam(req.Query, "limit", &query.Limit); errLimit != nil {
		return accountErrorResponse(errLimit)
	}
	if query.Limit < 1 || query.Limit > maxLogPageSize {
		return accountJSONError(http.StatusBadRequest, "invalid", "limit 必须是 1 到 1000 之间的整数")
	}
	view, errLogs := a.store.Logs(query)
	if errLogs != nil {
		return accountErrorResponse(errLogs)
	}
	result := accountLogView{Entries: make([]accountLogEntry, 0, len(view.Entries)), Total: view.Total, Statuses: view.Statuses}
	for _, row := range view.Entries {
		result.Entries = append(result.Entries, accountLogEntry{
			At: row.At, BillingModel: row.BillingModel, ExecutorType: row.ExecutorType,
			Source: accountSource(row.Provider), ReasoningEffort: row.ReasoningEffort,
			ServiceTier: row.ServiceTier, Failed: row.Failed, LatencyMS: row.LatencyMS, TTFTMS: row.TTFTMS,
			AccountingQuality: row.AccountingQuality, TotalUSD: row.Cost.TotalUSD,
			UncachedInput: row.Cost.UncachedInputTokens, CacheRead: row.Cost.CacheReadTokens,
			CacheWrite: row.Cost.CacheWriteTokens, Output: row.Cost.BilledOutputTokens, Reasoning: row.ReasoningTokens,
		})
	}
	return accountJSON(http.StatusOK, result)
}

func accountSource(provider string) string {
	name := billing.Credential{Provider: provider}.Name()
	if strings.Contains(name, "@") {
		return ""
	}
	return name
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

func accountUnauthorized() ManagementResponse {
	response := accountJSONError(http.StatusUnauthorized, "unauthorized", "API Key 无效")
	response.Headers.Set("WWW-Authenticate", `Bearer realm="cpa-key-billing-account"`)
	return response
}

func accountErrorResponse(err error) ManagementResponse {
	response := errorResponse(err)
	secureAccountResponse(&response)
	return response
}

func accountJSON(status int, payload any) ManagementResponse {
	response := JSONResponse(status, payload)
	secureAccountResponse(&response)
	return response
}

func accountJSONError(status int, code, message string) ManagementResponse {
	response := JSONError(status, code, message)
	secureAccountResponse(&response)
	return response
}

func secureAccountResponse(response *ManagementResponse) {
	response.Headers.Set("Cache-Control", "private, no-store")
	response.Headers.Set("Pragma", "no-cache")
	response.Headers.Set("Vary", "Authorization")
	response.Headers.Set("Referrer-Policy", "no-referrer")
	response.Headers.Set("X-Content-Type-Options", "nosniff")
}

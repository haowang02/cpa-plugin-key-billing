package plugin

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/messages"
)

// authFileQuota is an auth file's quota: what its provider answered when it
// was last queried, at FetchedAt, as the reports of its requests have updated
// it since.
type authFileQuota struct {
	FetchedAt                           time.Time           `json:"fetched_at,omitzero"`
	Plan                                string              `json:"plan,omitempty"`
	CreditBalance                       string              `json:"credit_balance,omitempty"`
	CreditCurrency                      string              `json:"credit_currency,omitempty"`
	CreditsUnlimited                    bool                `json:"credits_unlimited,omitempty"`
	RateLimitResetCreditsAvailableCount *int                `json:"rate_limit_reset_credits_available_count,omitempty"`
	RateLimitResetCredits               []resetCreditExpiry `json:"rate_limit_reset_credits,omitempty"`
	RateLimitResetCreditsUnavailable    bool                `json:"rate_limit_reset_credits_unavailable,omitempty"`
	Quota                               []quotaRow          `json:"quota"`
}

type resetCreditExpiry struct {
	ExpiresAt string `json:"expires_at"`
}

type quotaRow struct {
	Label            string           `json:"label,omitempty"`
	GroupLabel       string           `json:"group_label,omitempty"`
	LabelMessage     messages.Message `json:"label_message,omitzero"`
	GroupMessage     messages.Message `json:"group_message,omitzero"`
	LabelPrefix      string           `json:"label_prefix,omitempty"`
	RemainingPercent *float64         `json:"remaining_percent,omitempty"`
	Used             *float64         `json:"used,omitempty"`
	Limit            *float64         `json:"limit,omitempty"`
	Currency         string           `json:"currency,omitempty"`
	ResetAt          string           `json:"reset_at,omitempty"`
	// WindowSeconds is the window's length; zero when the provider does not say.
	WindowSeconds int64     `json:"window_seconds,omitempty"`
	ObservedAt    time.Time `json:"observed_at,omitzero"`
	// LimitKey names the limit of a window that requests report; with the
	// window's length it identifies the window from one report to the next.
	LimitKey string      `json:"limit_key,omitempty"`
	Meter    *quotaMeter `json:"meter,omitempty"`
	// Usage is what CLIProxyAPI requests consumed in the window's current
	// period, which UsageQuery selects.
	Usage      *billing.UsageTotal `json:"usage,omitempty"`
	UsageQuery *billing.UsageQuery `json:"usage_query,omitempty"`
}

// quotaSince is when the oldest query or report still worth reading was made;
// an older one describes no window that ran in the history range.
func quotaSince(now time.Time) time.Time { return now.Add(-quotaHistoryRange - quotaHistoryLookback) }

// saveQuotaQuery keeps what a query of the auth file's quota answered.
func (a *App) saveQuotaQuery(file hostAuthFile, quota authFileQuota) error {
	encoded, err := json.Marshal(quota)
	if err != nil {
		return fmt.Errorf("Encode quota query: %w", err)
	}
	query := billing.QuotaQuery{Provider: authCategory(file.Type), At: quota.FetchedAt, Quota: encoded}
	return a.store.SaveQuotaQuery(file.AuthIndex, query, quotaSince(quota.FetchedAt))
}

// currentQuotas returns the quota of each credential that has one, with the
// usage of each window's current period. A scope limits usage to one API key.
func (a *App) currentQuotas(scope string, credentials []quotaCredential) (map[quotaCredential]*authFileQuota, error) {
	indexes := make([]string, len(credentials))
	for i, credential := range credentials {
		indexes[i] = credential.authIndex
	}
	since := quotaSince(a.store.Now())
	queries, err := a.store.QuotaQueries(indexes, since)
	if err != nil {
		return nil, err
	}
	reports, err := a.store.LatestQuotaReports(indexes, since)
	if err != nil {
		return nil, err
	}
	quotas := make(map[quotaCredential]*authFileQuota)
	for _, credential := range credentials {
		var query *authFileQuota
		if stored, ok := queries[credential.authIndex]; ok && stored.Provider == credential.provider {
			query = new(authFileQuota)
			if err := json.Unmarshal(stored.Quota, query); err != nil {
				return nil, fmt.Errorf("Read quota query: %w", err)
			}
		}
		if quota := currentQuota(credential.provider, query, reports[credential.authIndex]); quota != nil {
			quotas[credential] = quota
		}
	}
	return quotas, a.attachQuotaUsage(scope, quotas)
}

// currentQuota combines a credential's last query with the reports of its
// requests, oldest first; nil means neither says anything. A query lists
// every window, so it replaces the windows reported before it, while a
// response reports only the windows of the limit it used and so updates just
// those. A window drops out once its limit is reported after the window's
// reset time without it, as the 5-hour window is after an upgrade to a plan
// that has none. Plan and credits follow the latest source that reports them;
// only a query reports manual resets.
func currentQuota(provider string, query *authFileQuota, observations []billing.QuotaObservation) *authFileQuota {
	var result *authFileQuota
	pending := query
	applyQuery := func() {
		current := *pending
		current.Quota = slices.Clone(pending.Quota)
		for i := range current.Quota {
			current.Quota[i].ObservedAt = pending.FetchedAt
		}
		if result != nil {
			current.Plan = cmp.Or(current.Plan, result.Plan)
			if current.CreditBalance == "" {
				current.CreditBalance, current.CreditCurrency = result.CreditBalance, result.CreditCurrency
			}
		}
		result, pending = &current, nil
	}
	reportedAt := make(map[string]time.Time)
	for _, observation := range observations {
		if observation.Provider != provider {
			continue
		}
		if pending != nil && observation.At.After(pending.FetchedAt) {
			applyQuery()
		}
		if result == nil {
			result = &authFileQuota{Quota: []quotaRow{}}
		}
		report := observation.QuotaReport
		if report.Plan != "" {
			result.Plan = codexPlanLabel(report.Plan)
		}
		if report.CreditBalance != "" {
			result.CreditBalance, result.CreditCurrency = report.CreditBalance, ""
		}
		if report.CreditsUnlimited != nil {
			result.CreditsUnlimited = *report.CreditsUnlimited
		}
		for _, window := range report.Windows {
			row := reportedRow(provider, window, observation.At)
			reportedAt[row.LimitKey] = observation.At
			if index := slices.IndexFunc(result.Quota, row.sameWindow); index >= 0 {
				result.Quota[index] = row
			} else {
				result.Quota = append(result.Quota, row)
			}
		}
	}
	if pending != nil {
		applyQuery()
	}
	if result == nil {
		return nil
	}
	result.Quota = slices.DeleteFunc(result.Quota, func(row quotaRow) bool {
		resetAt, err := time.Parse(time.RFC3339, row.ResetAt)
		reported := reportedAt[row.LimitKey]
		return err == nil && reported.After(row.ObservedAt) && !reported.Before(resetAt)
	})
	// A query lists its windows in the provider's order.
	if query == nil {
		slices.SortStableFunc(result.Quota, compareQuotaWindows)
	}
	return result
}

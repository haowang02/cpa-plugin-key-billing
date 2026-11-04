package billing

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

type QuotaMetric string

const (
	QuotaAmount   QuotaMetric = "amount"
	QuotaTokens   QuotaMetric = "tokens"
	QuotaRequests QuotaMetric = "requests"
)

// Spending keeps money in the currency it was charged in. A plan converts it
// into its own currency when it compares it with a limit. Sums saturate rather
// than overflow.
type Spending struct {
	USD float64 `json:"spent_usd"`
	CNY float64 `json:"spent_cny"`
}

func spent(amount float64, currency Currency) Spending {
	if currency == CNY {
		return Spending{CNY: amount}
	}
	return Spending{USD: amount}
}

func (s Spending) add(other Spending) Spending {
	return Spending{USD: math.Min(s.USD+other.USD, math.MaxFloat64), CNY: math.Min(s.CNY+other.CNY, math.MaxFloat64)}
}

func (s Spending) in(currency Currency, rate ExchangeRate) float64 {
	return math.Min(rate.Convert(s.USD, USD, currency)+rate.Convert(s.CNY, CNY, currency), math.MaxFloat64)
}

func (s Spending) valid() bool {
	return !invalidAmount(s.USD) && !invalidAmount(s.CNY)
}

type quotaUsage struct {
	Spending
	Tokens   int64
	Requests int64
}

// JSON numbers retain integer counters without float conversion. An amount
// balance is in its plan's Currency.
type QuotaBalance struct {
	Metric      QuotaMetric `json:"metric"`
	Currency    Currency    `json:"currency,omitempty"`
	Limit       json.Number `json:"limit"`
	Used        json.Number `json:"used"`
	Remaining   json.Number `json:"remaining"`
	UsedPercent float64     `json:"used_percent"`
	Blocked     bool        `json:"blocked"`
}

type QuotaWindowView struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	PeriodSeconds int64          `json:"period_seconds"`
	CycleAnchorAt time.Time      `json:"cycle_anchor_at,omitzero"`
	Started       bool           `json:"started"`
	Blocked       bool           `json:"blocked"`
	StartAt       time.Time      `json:"start_at,omitzero"`
	EndAt         time.Time      `json:"end_at,omitzero"`
	Dimensions    []QuotaBalance `json:"dimensions"`
}

type QuotaView struct {
	SharedQuota bool              `json:"shared_quota"`
	Unlimited   bool              `json:"unlimited"`
	Blocked     bool              `json:"blocked"`
	RetryAt     time.Time         `json:"retry_at,omitzero"`
	Windows     []QuotaWindowView `json:"windows"`
}

func (w QuotaWindow) view(cycle QuotaCycle, currency Currency, rate ExchangeRate) QuotaWindowView {
	view := QuotaWindowView{
		ID: w.ID, Name: w.Name, PeriodSeconds: w.PeriodSeconds, CycleAnchorAt: w.CycleAnchorAt,
		Started: !cycle.StartAt.IsZero(), StartAt: cycle.StartAt, EndAt: cycle.EndAt,
		Dimensions: make([]QuotaBalance, 0, 3),
	}
	view.Dimensions = appendQuotaBalance(view.Dimensions, QuotaAmount, currency, w.Amount, cycle.Spending.in(currency, rate))
	view.Dimensions = appendQuotaBalance(view.Dimensions, QuotaTokens, "", w.TokenLimit, cycle.UsedTokens)
	view.Dimensions = appendQuotaBalance(view.Dimensions, QuotaRequests, "", w.RequestLimit, cycle.UsedRequests)
	for _, balance := range view.Dimensions {
		view.Blocked = view.Blocked || balance.Blocked
	}
	return view
}

func quotaView(cycles QuotaCycles, plan *Plan, rate ExchangeRate, now time.Time) QuotaView {
	if plan == nil {
		return QuotaView{Unlimited: true, Windows: []QuotaWindowView{}}
	}
	view := QuotaView{Windows: make([]QuotaWindowView, 0, len(plan.Windows)), SharedQuota: plan.SharedQuota}
	if plan.SharedQuota {
		cycles = plan.Cycles
	}
	for _, window := range plan.Windows {
		cycle := cycles[window.ID]
		if cycle.StartAt.IsZero() && !window.CycleAnchorAt.IsZero() {
			cycle = window.newCycle(plan.ID, now)
		}
		item := window.view(cycle, plan.Currency, rate)
		if item.Blocked {
			view.Blocked = true
			if item.EndAt.After(view.RetryAt) {
				view.RetryAt = item.EndAt
			}
		}
		view.Windows = append(view.Windows, item)
	}
	return view
}

func appendQuotaBalance[T int64 | float64](balances []QuotaBalance, metric QuotaMetric, currency Currency, limit, used T) []QuotaBalance {
	if limit <= 0 {
		return balances
	}
	return append(balances, QuotaBalance{
		Metric: metric, Currency: currency, Limit: json.Number(fmt.Sprint(limit)), Used: json.Number(fmt.Sprint(used)),
		Remaining:   json.Number(fmt.Sprint(max(0, limit-used))),
		UsedPercent: math.Min(float64(used)/float64(limit), 1) * 100,
		Blocked:     used >= limit,
	})
}

func (b QuotaBalance) Description() string {
	if b.Metric == QuotaAmount {
		used, _ := b.Used.Float64()
		limit, _ := b.Limit.Float64()
		return b.Currency.Format(used) + " / " + b.Currency.Format(limit)
	}
	return fmt.Sprintf("%s %s / %s", b.Metric, b.Used, b.Limit)
}

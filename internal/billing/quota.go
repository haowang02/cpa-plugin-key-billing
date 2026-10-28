package billing

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
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

// UsageSince excludes requests admitted before binding or an administrative reset.
type QuotaCycle struct {
	PlanID     string    `json:"plan_id,omitempty"`
	StartAt    time.Time `json:"start_at,omitzero"`
	EndAt      time.Time `json:"end_at,omitzero"`
	UsageSince time.Time `json:"usage_since,omitzero"`
	Spending
	UsedTokens   int64 `json:"used_tokens"`
	UsedRequests int64 `json:"used_requests"`
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
	Unlimited bool              `json:"unlimited"`
	Blocked   bool              `json:"blocked"`
	RetryAt   time.Time         `json:"retry_at,omitzero"`
	Windows   []QuotaWindowView `json:"windows"`
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

func quotaView(key *KeyState, plan Plan, rate ExchangeRate, now time.Time) QuotaView {
	view := QuotaView{Windows: make([]QuotaWindowView, 0, len(plan.Windows)), Unlimited: plan.ID == ""}
	for _, window := range plan.Windows {
		cycle := key.Cycles[window.ID]
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

func (w QuotaWindow) newCycle(planID string, now time.Time) QuotaCycle {
	cycle := QuotaCycle{PlanID: planID, StartAt: now}
	if !w.CycleAnchorAt.IsZero() {
		// Reduce seconds before converting to Duration, including times before the anchor.
		offset := (now.Unix() - w.CycleAnchorAt.Unix()) % w.PeriodSeconds
		if offset < 0 {
			offset += w.PeriodSeconds
		}
		cycle.StartAt = time.Unix(now.Unix()-offset, 0).UTC()
		cycle.UsageSince = now
	}
	cycle.EndAt = cycle.StartAt.Add(time.Duration(w.PeriodSeconds) * time.Second)
	return cycle
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

// Expiration never starts another window; only admission can do that.
func settleExpiredCycles(key *KeyState, now time.Time) bool {
	changed := false
	for id, cycle := range key.Cycles {
		if !now.Before(cycle.EndAt) {
			delete(key.Cycles, id)
			changed = true
		}
	}
	return changed
}

func activateCycles(key *KeyState, plan Plan, now time.Time) bool {
	if key.Cycles == nil {
		key.Cycles = make(map[string]QuotaCycle)
	}
	changed := false
	for _, window := range plan.Windows {
		if _, exists := key.Cycles[window.ID]; !exists {
			key.Cycles[window.ID] = window.newCycle(plan.ID, now)
			changed = true
		}
	}
	return changed
}

func (key *KeyState) ValidateCycles(plan Plan) error {
	if key.PlanID != "" && plan.ID != key.PlanID {
		return invalidf("The subscription plan bound to this API key does not exist")
	}
	for id, cycle := range key.Cycles {
		index := slices.IndexFunc(plan.Windows, func(window QuotaWindow) bool { return window.ID == id })
		if index < 0 || cycle.PlanID != key.PlanID || cycle.StartAt.IsZero() || cycle.EndAt.IsZero() ||
			!cycle.EndAt.Equal(cycle.StartAt.Add(time.Duration(plan.Windows[index].PeriodSeconds)*time.Second)) ||
			!cycle.Spending.valid() || cycle.UsedRequests < 0 || cycle.UsedTokens < 0 {
			return invalidf("Invalid quota cycle data for this API key")
		}
		window := plan.Windows[index]
		if !window.CycleAnchorAt.IsZero() && !window.newCycle(plan.ID, cycle.StartAt).StartAt.Equal(cycle.StartAt) ||
			!cycle.UsageSince.IsZero() && (cycle.UsageSince.Before(cycle.StartAt) || !cycle.UsageSince.Before(cycle.EndAt)) {
			return invalidf("Invalid quota cycle data for this API key")
		}
	}
	return nil
}

// Usage never starts a window or charges a replacement window with older usage.
func (key *KeyState) chargeCycles(at time.Time, usage quotaUsage) {
	if key.PlanID == "" || at.IsZero() {
		return
	}
	for id, cycle := range key.Cycles {
		if cycle.PlanID != key.PlanID || at.Before(cycle.StartAt) || at.Before(cycle.UsageSince) || !at.Before(cycle.EndAt) {
			continue
		}
		// Keep all dimensions, including currently disabled limits, so changing
		// limits retains this cycle's usage.
		cycle.Spending = cycle.Spending.add(usage.Spending)
		cycle.UsedTokens = addQuotaCount(cycle.UsedTokens, usage.Tokens)
		cycle.UsedRequests = addQuotaCount(cycle.UsedRequests, usage.Requests)
		key.Cycles[id] = cycle
	}
}

func addQuotaCount(current, delta int64) int64 {
	if delta > math.MaxInt64-current {
		return math.MaxInt64
	}
	return current + delta
}

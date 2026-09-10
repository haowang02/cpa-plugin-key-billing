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
	QuotaAmount   QuotaMetric = "amount_usd"
	QuotaTokens   QuotaMetric = "tokens"
	QuotaRequests QuotaMetric = "requests"
)

type quotaUsage struct {
	AmountUSD float64
	Tokens    int64
	Requests  int64
}

// UsageSince excludes requests admitted before binding or an administrative reset.
type QuotaCycle struct {
	PlanID       string    `json:"plan_id,omitempty"`
	StartAt      time.Time `json:"start_at,omitzero"`
	EndAt        time.Time `json:"end_at,omitzero"`
	UsageSince   time.Time `json:"usage_since,omitzero"`
	SpentUSD     float64   `json:"spent_usd"`
	UsedTokens   int64     `json:"used_tokens"`
	UsedRequests int64     `json:"used_requests"`
}

// JSON numbers retain integer counters without float conversion.
type QuotaBalance struct {
	Metric      QuotaMetric `json:"metric"`
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

func (w QuotaWindow) view(cycle QuotaCycle) QuotaWindowView {
	view := QuotaWindowView{
		ID: w.ID, Name: w.Name, PeriodSeconds: w.PeriodSeconds, CycleAnchorAt: w.CycleAnchorAt,
		Started: !cycle.StartAt.IsZero(), StartAt: cycle.StartAt, EndAt: cycle.EndAt,
		Dimensions: make([]QuotaBalance, 0, 3),
	}
	view.Dimensions = appendQuotaBalance(view.Dimensions, QuotaAmount, w.AmountUSD, cycle.SpentUSD)
	view.Dimensions = appendQuotaBalance(view.Dimensions, QuotaTokens, w.TokenLimit, cycle.UsedTokens)
	view.Dimensions = appendQuotaBalance(view.Dimensions, QuotaRequests, w.RequestLimit, cycle.UsedRequests)
	for _, balance := range view.Dimensions {
		view.Blocked = view.Blocked || balance.Blocked
	}
	return view
}

func quotaView(key *KeyState, plan Plan, now time.Time) QuotaView {
	view := QuotaView{Windows: make([]QuotaWindowView, 0, len(plan.Windows)), Unlimited: plan.ID == ""}
	for _, window := range plan.Windows {
		cycle := key.Cycles[window.ID]
		if cycle.StartAt.IsZero() && !window.CycleAnchorAt.IsZero() {
			cycle = window.newCycle(plan.ID, now)
		}
		item := window.view(cycle)
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

func appendQuotaBalance[T int64 | float64](balances []QuotaBalance, metric QuotaMetric, limit, used T) []QuotaBalance {
	if limit <= 0 {
		return balances
	}
	return append(balances, QuotaBalance{
		Metric: metric, Limit: json.Number(fmt.Sprint(limit)), Used: json.Number(fmt.Sprint(used)),
		Remaining:   json.Number(fmt.Sprint(max(0, limit-used))),
		UsedPercent: math.Min(float64(used)/float64(limit), 1) * 100,
		Blocked:     used >= limit,
	})
}

func (b QuotaBalance) Description() string {
	if b.Metric == QuotaAmount {
		used, _ := b.Used.Float64()
		limit, _ := b.Limit.Float64()
		return fmt.Sprintf("$%.4f / $%.4f", used, limit)
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
		return invalidf("API Key 绑定的订阅计划不存在")
	}
	for id, cycle := range key.Cycles {
		index := slices.IndexFunc(plan.Windows, func(window QuotaWindow) bool { return window.ID == id })
		if index < 0 || cycle.PlanID != key.PlanID || cycle.StartAt.IsZero() || cycle.EndAt.IsZero() ||
			!cycle.EndAt.Equal(cycle.StartAt.Add(time.Duration(plan.Windows[index].PeriodSeconds)*time.Second)) ||
			cycle.SpentUSD < 0 || math.IsNaN(cycle.SpentUSD) || math.IsInf(cycle.SpentUSD, 0) ||
			cycle.UsedRequests < 0 || cycle.UsedTokens < 0 {
			return invalidf("API Key 的额度周期数据无效")
		}
		window := plan.Windows[index]
		if !window.CycleAnchorAt.IsZero() && !window.newCycle(plan.ID, cycle.StartAt).StartAt.Equal(cycle.StartAt) ||
			!cycle.UsageSince.IsZero() && (cycle.UsageSince.Before(cycle.StartAt) || !cycle.UsageSince.Before(cycle.EndAt)) {
			return invalidf("API Key 的额度周期数据无效")
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
		// limits retains this cycle's usage. Saturation only prevents overflow.
		cycle.SpentUSD = math.Min(cycle.SpentUSD+usage.AmountUSD, math.MaxFloat64)
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

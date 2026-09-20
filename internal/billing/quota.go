package billing

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
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
	PlanID         string         `json:"plan_id,omitempty"`
	StartAt        time.Time      `json:"start_at,omitzero"`
	EndAt          time.Time      `json:"end_at,omitzero"`
	UsageSince     time.Time      `json:"usage_since,omitzero"`
	SpentUSD       float64        `json:"spent_usd"`
	UsedTokens     int64          `json:"used_tokens"`
	UsedRequests   int64          `json:"used_requests"`
	TemporaryQuota TemporaryQuota `json:"temporary_quota,omitzero"`
}

// JSON numbers retain integer counters without float conversion.
type QuotaBalance struct {
	Metric         QuotaMetric `json:"metric"`
	BaseLimit      json.Number `json:"base_limit,omitempty"`
	TemporaryLimit json.Number `json:"temporary_limit,omitempty"`
	Limit          json.Number `json:"limit"`
	Used           json.Number `json:"used"`
	Remaining      json.Number `json:"remaining"`
	UsedPercent    float64     `json:"used_percent"`
	Blocked        bool        `json:"blocked"`
}

type QuotaWindowView struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	PeriodSeconds  int64          `json:"period_seconds"`
	CycleAnchorAt  time.Time      `json:"cycle_anchor_at,omitzero"`
	Started        bool           `json:"started"`
	Blocked        bool           `json:"blocked"`
	StartAt        time.Time      `json:"start_at,omitzero"`
	EndAt          time.Time      `json:"end_at,omitzero"`
	Dimensions     []QuotaBalance `json:"dimensions"`
	CreditRevision string         `json:"credit_revision,omitempty"`
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
		Dimensions: make([]QuotaBalance, 0, len(quotaDimensions)),
	}
	for _, dim := range quotaDimensions {
		view.Dimensions = dim.appendBalance(view.Dimensions, w, cycle)
	}
	for _, balance := range view.Dimensions {
		view.Blocked = view.Blocked || balance.Blocked
	}
	return view
}

func quotaView(key *KeyState, plan Plan, now time.Time) QuotaView {
	view := QuotaView{Windows: make([]QuotaWindowView, 0, len(plan.Windows)), Unlimited: plan.ID == ""}
	for _, window := range plan.Windows {
		cycle, persisted := key.Cycles[window.ID]
		if cycle.StartAt.IsZero() && !window.CycleAnchorAt.IsZero() {
			cycle = window.newCycle(plan.ID, now)
		}
		item := window.view(cycle)
		item.CreditRevision = quotaCreditRevision(plan.ID, window, cycle, persisted)
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

func appendQuotaBalance[T int64 | float64](balances []QuotaBalance, metric QuotaMetric, base, extra, used T) []QuotaBalance {
	if base <= 0 {
		return balances
	}
	limit := base + extra
	balance := QuotaBalance{
		Metric: metric, Limit: json.Number(fmt.Sprint(limit)), Used: json.Number(fmt.Sprint(used)),
		Remaining:   json.Number(fmt.Sprint(max(0, limit-used))),
		UsedPercent: math.Min(float64(used)/float64(limit), 1) * 100,
		Blocked:     used >= limit,
	}
	if extra > 0 {
		balance.BaseLimit = json.Number(fmt.Sprint(base))
		balance.TemporaryLimit = json.Number(fmt.Sprint(extra))
	}
	return append(balances, balance)
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
		return invalidf("The subscription plan bound to this API key does not exist")
	}
	for id, cycle := range key.Cycles {
		index := slices.IndexFunc(plan.Windows, func(window QuotaWindow) bool { return window.ID == id })
		if index < 0 || cycle.PlanID != key.PlanID || cycle.StartAt.IsZero() || cycle.EndAt.IsZero() ||
			!cycle.EndAt.Equal(cycle.StartAt.Add(time.Duration(plan.Windows[index].PeriodSeconds)*time.Second)) {
			return invalidf("Invalid quota cycle data for this API key")
		}
		for _, dim := range quotaDimensions {
			if !dim.validateCycle(cycle) {
				return invalidf("Invalid quota cycle data for this API key")
			}
		}
		window := plan.Windows[index]
		if err := cycle.TemporaryQuota.validate(window); err != nil {
			return err
		}
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
		// limits retains this cycle's usage. Saturation only prevents overflow.
		for _, dim := range quotaDimensions {
			dim.charge(&cycle, usage)
		}
		key.Cycles[id] = cycle
	}
}

func addQuotaCount(current, delta int64) int64 {
	if delta > math.MaxInt64-current {
		return math.MaxInt64
	}
	return current + delta
}

type quotaDimension interface {
	metric() QuotaMetric
	appendBalance(balances []QuotaBalance, w QuotaWindow, cycle QuotaCycle) []QuotaBalance
	charge(cycle *QuotaCycle, usage quotaUsage)
	validateCycle(cycle QuotaCycle) bool
	validateWindow(name string, w QuotaWindow) error
	hasLimit(w QuotaWindow) bool
	validateTemporary(w QuotaWindow, q TemporaryQuota) error
	resetTemporaryIfDisabled(w QuotaWindow, q *TemporaryQuota)
	writeWindowLimit(b *strings.Builder, w QuotaWindow)
	writeTemporaryLimit(b *strings.Builder, q TemporaryQuota)
}

func validateIntWindowLimit(name string, limit int64) error {
	if limit < 0 || limit > maxQuotaCount {
		return invalidf("Window %q: token and request limits must be integers from 0 to %d", name, maxQuotaCount)
	}
	return nil
}

func validateTemporaryInt(base, extra int64) error {
	if extra < 0 || extra > maxQuotaCount {
		return invalidf("Temporary credits must be finite non-negative values, and token and request credits must be safe integers")
	}
	if base == 0 && extra != 0 {
		return invalidf("Unlimited quota dimensions do not need temporary credits")
	}
	if base > maxQuotaCount-extra {
		return invalidf("The base quota plus temporary credits exceeds the supported range")
	}
	return nil
}

type amountDimension struct{}

func (amountDimension) metric() QuotaMetric {
	return QuotaAmount
}

func (amountDimension) appendBalance(balances []QuotaBalance, w QuotaWindow, cycle QuotaCycle) []QuotaBalance {
	return appendQuotaBalance(balances, QuotaAmount, w.AmountUSD, cycle.TemporaryQuota.AmountUSD, cycle.SpentUSD)
}

func (amountDimension) charge(cycle *QuotaCycle, usage quotaUsage) {
	cycle.SpentUSD = math.Min(cycle.SpentUSD+usage.AmountUSD, math.MaxFloat64)
}

func (amountDimension) validateCycle(cycle QuotaCycle) bool {
	return cycle.SpentUSD >= 0 && !math.IsNaN(cycle.SpentUSD) && !math.IsInf(cycle.SpentUSD, 0)
}

func (amountDimension) validateWindow(name string, w QuotaWindow) error {
	if w.AmountUSD < 0 || math.IsNaN(w.AmountUSD) || math.IsInf(w.AmountUSD, 0) {
		return invalidf("Window %q: amount quota must be a finite non-negative number", name)
	}
	return nil
}

func (amountDimension) hasLimit(w QuotaWindow) bool {
	return w.AmountUSD > 0
}

func (amountDimension) validateTemporary(w QuotaWindow, q TemporaryQuota) error {
	if q.AmountUSD < 0 || math.IsNaN(q.AmountUSD) || math.IsInf(q.AmountUSD, 0) {
		return invalidf("Temporary credits must be finite non-negative values, and token and request credits must be safe integers")
	}
	if w.AmountUSD == 0 && q.AmountUSD != 0 {
		return invalidf("Unlimited quota dimensions do not need temporary credits")
	}
	if math.IsInf(w.AmountUSD+q.AmountUSD, 0) {
		return invalidf("The base quota plus temporary credits exceeds the supported range")
	}
	return nil
}

func (amountDimension) resetTemporaryIfDisabled(w QuotaWindow, q *TemporaryQuota) {
	if w.AmountUSD == 0 {
		q.AmountUSD = 0
	}
}

func (amountDimension) writeWindowLimit(b *strings.Builder, w QuotaWindow) {
	fmt.Fprintf(b, "|%.17g", w.AmountUSD)
}

func (amountDimension) writeTemporaryLimit(b *strings.Builder, q TemporaryQuota) {
	fmt.Fprintf(b, "|%.17g", q.AmountUSD)
}

type tokensDimension struct{}

func (tokensDimension) metric() QuotaMetric {
	return QuotaTokens
}

func (tokensDimension) appendBalance(balances []QuotaBalance, w QuotaWindow, cycle QuotaCycle) []QuotaBalance {
	return appendQuotaBalance(balances, QuotaTokens, w.TokenLimit, cycle.TemporaryQuota.TokenLimit, cycle.UsedTokens)
}

func (tokensDimension) charge(cycle *QuotaCycle, usage quotaUsage) {
	cycle.UsedTokens = addQuotaCount(cycle.UsedTokens, usage.Tokens)
}

func (tokensDimension) validateCycle(cycle QuotaCycle) bool {
	return cycle.UsedTokens >= 0
}

func (tokensDimension) validateWindow(name string, w QuotaWindow) error {
	return validateIntWindowLimit(name, w.TokenLimit)
}

func (tokensDimension) hasLimit(w QuotaWindow) bool {
	return w.TokenLimit > 0
}

func (tokensDimension) validateTemporary(w QuotaWindow, q TemporaryQuota) error {
	return validateTemporaryInt(w.TokenLimit, q.TokenLimit)
}

func (tokensDimension) resetTemporaryIfDisabled(w QuotaWindow, q *TemporaryQuota) {
	if w.TokenLimit == 0 {
		q.TokenLimit = 0
	}
}

func (tokensDimension) writeWindowLimit(b *strings.Builder, w QuotaWindow) {
	fmt.Fprintf(b, "|%d", w.TokenLimit)
}

func (tokensDimension) writeTemporaryLimit(b *strings.Builder, q TemporaryQuota) {
	fmt.Fprintf(b, "|%d", q.TokenLimit)
}

type requestsDimension struct{}

func (requestsDimension) metric() QuotaMetric {
	return QuotaRequests
}

func (requestsDimension) appendBalance(balances []QuotaBalance, w QuotaWindow, cycle QuotaCycle) []QuotaBalance {
	return appendQuotaBalance(balances, QuotaRequests, w.RequestLimit, cycle.TemporaryQuota.RequestLimit, cycle.UsedRequests)
}

func (requestsDimension) charge(cycle *QuotaCycle, usage quotaUsage) {
	cycle.UsedRequests = addQuotaCount(cycle.UsedRequests, usage.Requests)
}

func (requestsDimension) validateCycle(cycle QuotaCycle) bool {
	return cycle.UsedRequests >= 0
}

func (requestsDimension) validateWindow(name string, w QuotaWindow) error {
	return validateIntWindowLimit(name, w.RequestLimit)
}

func (requestsDimension) hasLimit(w QuotaWindow) bool {
	return w.RequestLimit > 0
}

func (requestsDimension) validateTemporary(w QuotaWindow, q TemporaryQuota) error {
	return validateTemporaryInt(w.RequestLimit, q.RequestLimit)
}

func (requestsDimension) resetTemporaryIfDisabled(w QuotaWindow, q *TemporaryQuota) {
	if w.RequestLimit == 0 {
		q.RequestLimit = 0
	}
}

func (requestsDimension) writeWindowLimit(b *strings.Builder, w QuotaWindow) {
	fmt.Fprintf(b, "|%d", w.RequestLimit)
}

func (requestsDimension) writeTemporaryLimit(b *strings.Builder, q TemporaryQuota) {
	fmt.Fprintf(b, "|%d", q.RequestLimit)
}

var quotaDimensions = []quotaDimension{
	amountDimension{},
	tokensDimension{},
	requestsDimension{},
}

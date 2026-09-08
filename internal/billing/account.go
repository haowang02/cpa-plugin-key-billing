package billing

import (
	"strings"
	"time"
)

type UsageEvent struct {
	Scope           string
	KeyPreview      string
	AuthIndex       string
	Provider        string
	ExecutorType    string
	AuthType        string
	Account         string
	ReasoningEffort string
	ServiceTier     string
	UpstreamModel   string
	RouteModel      string
	RequestedAt     time.Time
	Latency         time.Duration
	TTFT            time.Duration
	Breakdown       TokenBreakdown
	At              time.Time
}

func (s *Store) RecordUsage(event UsageEvent) {
	s.recordUsage(event, nil)
}

func (s *Store) RecordUsageError(event UsageEvent, failure RequestError) {
	s.recordUsage(event, &failure)
}

func (s *Store) recordUsage(event UsageEvent, failure *RequestError) {
	scope := strings.TrimSpace(event.Scope)
	at := event.At
	if at.IsZero() {
		at = s.Now()
	}
	event.At = at
	price, billingModel, priceErr := s.ResolveModelPrice(event.UpstreamModel, event.RouteModel, false)
	if priceErr != nil {
		s.AddPluginLog(PluginLogError, "模型价格读取失败，保留用量事件并按零计费")
	}
	if priceErr != nil || price.Source == PriceSourceNone {
		// Usage has already happened. Keep all reported tokens and failure
		// details even if its price was deleted or reference prices are unavailable.
		price = Price{Source: PriceSourceNone}
	}

	cost := ComputeCost(price, event.Breakdown)
	missingCycleTime := false
	updateResult(s, func(state *State) (struct{}, Changes) {
		// ServiceTier is the client-requested tier, not the upstream response tier.
		if s.cfg.CodexFastModeBilling && price.Source != PriceSourceNone && event.Breakdown.Billable() &&
			!s.cfg.excludesCodexFastBilling(billingModel) &&
			strings.EqualFold(strings.TrimSpace(event.Provider), "codex") &&
			strings.EqualFold(strings.TrimSpace(event.AuthType), "oauth") &&
			strings.EqualFold(strings.TrimSpace(event.ServiceTier), "priority") {
			cost.Multiplier = CodexFastModeMultiplier
			cost.UncachedInputUSD *= CodexFastModeMultiplier
			cost.CacheReadUSD *= CodexFastModeMultiplier
			cost.CacheWriteUSD *= CodexFastModeMultiplier
			cost.OutputUSD *= CodexFastModeMultiplier
			cost.TotalUSD = cost.UncachedInputUSD + cost.CacheReadUSD + cost.CacheWriteUSD + cost.OutputUSD
			cost.AppliedInputPer1M *= CodexFastModeMultiplier
			cost.AppliedOutputPer1M *= CodexFastModeMultiplier
			cost.AppliedCacheReadPer1M *= CodexFastModeMultiplier
			cost.AppliedCacheWritePer1M *= CodexFastModeMultiplier
		}
		upstreamModel := strings.TrimSpace(event.UpstreamModel)
		if upstreamModel == "" {
			upstreamModel = strings.TrimSpace(event.RouteModel)
		}
		failed := failure != nil
		entryAt := event.RequestedAt
		if entryAt.IsZero() {
			entryAt = at
		}
		entry := RequestEvent{
			At:                entryAt,
			Scope:             scope,
			AuthIndex:         event.AuthIndex,
			Provider:          strings.TrimSpace(event.Provider),
			ExecutorType:      event.ExecutorType,
			ReasoningEffort:   event.ReasoningEffort,
			ServiceTier:       event.ServiceTier,
			UpstreamModel:     upstreamModel,
			BillingModel:      billingModel,
			Failed:            failed,
			LatencyMS:         event.Latency.Milliseconds(),
			TTFTMS:            event.TTFT.Milliseconds(),
			AccountingQuality: event.Breakdown.Quality,
			PriceSource:       price.Source,
			Cost:              cost,
			ReasoningTokens:   event.Breakdown.Output.ReasoningTokens,
		}
		var changedKeys []string
		if key := state.ensureKey(scope, event.KeyPreview); key != nil {
			if !failed || usageBreakdownPresent(event.Breakdown) {
				missingCycleTime = event.RequestedAt.IsZero() && len(key.Cycles) > 0 && cost.TotalUSD > 0
				chargeCycles(key, event, cost.TotalUSD)
			}
			// A completion may arrive after its period ended. Close it now, but do
			// not start the next period until another request is admitted.
			if _, hasPlan := state.FindPlan(key.PlanID); hasPlan {
				settleExpiredCycles(key, at)
			}
			changedKeys = []string{scope}
		}
		changes := Changes{
			Keys:               changedKeys,
			Credentials:        learnCredential(state, scope, event.AuthIndex, event.Provider, event.AuthType, event.Account),
			RequestEventCutoff: at.Add(-RequestEventRetention),
		}
		if failure == nil {
			changes.NormalRequestEvents = []RequestEvent{entry}
		} else {
			changes.RequestErrorEvents = []RequestErrorEvent{{Event: entry, Error: *failure}}
		}
		return struct{}{}, changes
	})
	if missingCycleTime {
		s.AddPluginLog(PluginLogError, "用量记录缺少请求时间，保留费用事件并跳过额度扣除")
	}
	if price.Source == PriceSourceReference {
		s.AddPluginLog(PluginLogDebug,
			"参考价计费：billing_model=%q，费用=$%.8f，单价（每百万 Token）：输入=$%g，输出=$%g，缓存读=$%g，缓存写=$%g",
			billingModel, cost.TotalUSD, cost.AppliedInputPer1M, cost.AppliedOutputPer1M,
			cost.AppliedCacheReadPer1M, cost.AppliedCacheWritePer1M)
	}
}

// Usage never starts a window or charges a replacement window with older usage.
func chargeCycles(key *KeyState, event UsageEvent, costUSD float64) {
	if key.PlanID == "" || event.RequestedAt.IsZero() {
		return
	}
	for id, cycle := range key.Cycles {
		if cycle.PlanID != key.PlanID || event.RequestedAt.Before(cycle.StartAt) || !event.RequestedAt.Before(cycle.EndAt) {
			continue
		}
		cycle.SpentUSD += costUSD
		key.Cycles[id] = cycle
	}
}

func usageBreakdownPresent(value TokenBreakdown) bool {
	return value.TotalTokens != 0 || value.Input.TotalTokens != 0 || value.Output.TotalTokens != 0 ||
		value.UnclassifiedTokens != 0 || value.Quality != ""
}

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
	provider := strings.TrimSpace(event.Provider)
	authType := strings.ToLower(strings.TrimSpace(event.AuthType))
	account := ""
	switch authType {
	case "apikey":
		account = PreviewKey(event.Account)
	case "oauth":
		// The host may fall back to the downstream API key when no account is available.
		if CallerScope(event.Account) != normalizeScope(scope) {
			account = strings.TrimSpace(event.Account)
		}
	}
	at := event.At
	if at.IsZero() {
		at = s.Now()
	}
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
			strings.EqualFold(provider, "codex") && authType == "oauth" &&
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
			Provider:          provider,
			Account:           account,
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
		planStarted := false
		if key := state.ensureKey(scope, event.KeyPreview); key != nil {
			usage := quotaUsage{AmountUSD: cost.TotalUSD}
			if !failed {
				usage.Requests = 1
			}
			if event.Breakdown.Valid() && event.Breakdown.Quality != TokenAccountingInconsistent {
				usage.Tokens = event.Breakdown.TotalTokens
			}
			plan, hasPlan := state.FindPlan(key.PlanID)
			if hasPlan && plan.SharedCycles() {
				missingCycleTime = event.RequestedAt.IsZero() && usage != (quotaUsage{})
				if !event.RequestedAt.IsZero() && !event.RequestedAt.Before(key.BillingSince) && !event.RequestedAt.After(at) &&
					(!plan.StartedAt.IsZero() || plan.startsWithUsage(usage)) {
					// Price lookup may finish out of order. Serialize billing time
					// with the plan clock so concurrent first records all count.
					billedAt := at
					if now := s.Now(); now.After(billedAt) {
						billedAt = now
					}
					if plan.StartedAt.After(billedAt) {
						billedAt = plan.StartedAt
					}
					if plan.StartedAt.IsZero() {
						plan.StartedAt = billedAt
						for i := range state.Plans {
							if state.Plans[i].ID == plan.ID {
								state.Plans[i].StartedAt = billedAt
								break
							}
						}
						planStarted = true
					}
					syncPlanCycles(key, plan, billedAt)
					key.chargeCycles(billedAt, usage)
				}
			} else {
				missingCycleTime = event.RequestedAt.IsZero() && len(key.Cycles) > 0 && usage != (quotaUsage{})
				key.chargeCycles(event.RequestedAt, usage)
				if hasPlan {
					settleExpiredCycles(key, at)
				}
			}
			changedKeys = []string{scope}
		}
		changes := Changes{
			Keys:               changedKeys,
			Plans:              planStarted,
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
		s.AddPluginLog(PluginLogError, "用量记录缺少请求时间，保留用量事件并跳过额度扣除")
	}
	if price.Source == PriceSourceReference {
		s.AddPluginLog(PluginLogDebug,
			"参考价计费：billing_model=%q，费用=$%.8f，单价（每百万 Token）：输入=$%g，输出=$%g，缓存读=$%g，缓存写=$%g",
			billingModel, cost.TotalUSD, cost.AppliedInputPer1M, cost.AppliedOutputPer1M,
			cost.AppliedCacheReadPer1M, cost.AppliedCacheWritePer1M)
	}
}

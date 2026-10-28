package billing

import (
	"strings"
	"time"
)

type UsageEvent struct {
	RequestOrigin
	Execution           *ExecutionMetadata
	Stream              *bool
	Scope               string
	KeyPreview          string
	AuthIndex           string
	Provider            string
	ExecutorType        string
	AuthType            string
	Quota               *QuotaReport
	Account             string
	ReasoningEffort     string
	ServiceTier         string
	ResponseServiceTier string
	UpstreamModel       string
	ResponseModel       string
	RouteModel          string
	RequestedAt         time.Time
	Latency             time.Duration
	TTFT                time.Duration
	Breakdown           TokenBreakdown
	At                  time.Time
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
	at := event.At
	if at.IsZero() {
		at = s.Now()
	}
	price, billingModel, priceErr := s.ResolveModelPrice(event.UpstreamModel, event.RouteModel, false)
	if priceErr != nil {
		s.AddPluginLog(PluginLogError, "Failed to read model pricing; preserving the usage event with zero cost")
	}
	if priceErr != nil || price.Source == PriceSourceNone {
		// Usage has already happened. Keep all reported tokens and failure
		// details even if its price was deleted or reference prices are unavailable.
		price = Price{Source: PriceSourceNone, Currency: USD}
	}

	cost := ComputeCost(price, event.Breakdown)
	missingCycleTime := false
	updateResult(s, func(state *State) (struct{}, Changes) {
		// ServiceTier is the client-requested tier, not the upstream response tier.
		if s.cfg.CodexFastModeBilling && price.Source != PriceSourceNone && event.Breakdown.Billable() &&
			strings.EqualFold(provider, "codex") && authType == "oauth" &&
			strings.EqualFold(strings.TrimSpace(event.ServiceTier), "priority") {
			cost.multiply(CodexFastModeMultiplier)
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
			RequestOrigin:       event.RequestOrigin,
			Execution:           event.Execution,
			Stream:              event.Stream,
			At:                  entryAt,
			Scope:               scope,
			AuthIndex:           event.AuthIndex,
			Provider:            provider,
			Account:             event.Account,
			ExecutorType:        event.ExecutorType,
			ReasoningEffort:     event.ReasoningEffort,
			ServiceTier:         event.ServiceTier,
			ResponseServiceTier: strings.TrimSpace(event.ResponseServiceTier),
			UpstreamModel:       upstreamModel,
			ResponseModel:       strings.TrimSpace(event.ResponseModel),
			BillingModel:        billingModel,
			Failed:              failed,
			LatencyMS:           event.Latency.Milliseconds(),
			TTFTMS:              event.TTFT.Milliseconds(),
			AccountingQuality:   event.Breakdown.Quality,
			PriceSource:         price.Source,
			Cost:                cost,
			ReasoningTokens:     event.Breakdown.Output.ReasoningTokens,
			Quota:               event.Quota,
		}
		var changedKeys []string
		if key := state.ensureKey(scope, event.KeyPreview); key != nil {
			usage := quotaUsage{Spending: spent(cost.Total, cost.Currency)}
			if !failed {
				usage.Requests = 1
			}
			if event.Breakdown.Valid() && event.Breakdown.Quality != TokenAccountingInconsistent {
				usage.Tokens = event.Breakdown.TotalTokens
			}
			missingCycleTime = event.RequestedAt.IsZero() && len(key.Cycles) > 0 && usage != (quotaUsage{})
			key.chargeCycles(event.RequestedAt, usage)
			settleExpiredCycles(key, at)
			changedKeys = []string{scope}
		}
		changes := Changes{
			Keys:               changedKeys,
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
		s.AddPluginLog(PluginLogError, "Usage record has no request time; preserving the event without deducting quota")
	}
	if price.Source == PriceSourceReference {
		s.AddPluginLog(PluginLogDebug,
			"Reference pricing: billing_model=%q, cost=$%.8f, rates per million tokens: input=$%g, output=$%g, cache_read=$%g, cache_write=$%g",
			billingModel, cost.Total, cost.AppliedInputPer1M, cost.AppliedOutputPer1M,
			cost.AppliedCacheReadPer1M, cost.AppliedCacheWritePer1M)
	}
}

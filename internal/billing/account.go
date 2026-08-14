package billing

import (
	"strings"
	"time"
)

// A retried request keeps only the final provider usage record.
type UsageRecord struct {
	BillingModel  string
	UpstreamModel string
	Generate      bool
	// Responded reports whether the upstream produced any response at all.
	Responded   bool
	RequestedAt time.Time
	Breakdown   TokenBreakdown
}

// RequestOutcome is how a downstream request ended. The empty outcome is a
// request that completed normally, which is the overwhelming majority and so
// costs nothing to store.
type RequestOutcome string

const (
	OutcomeFailed RequestOutcome = "failed"
	// Provider usage reported before cancellation is still charged.
	OutcomeCanceled RequestOutcome = "canceled"
)

// UsageEvent commits one terminal downstream request. A nil Record means the
// request produced no usage the plugin could read.
type UsageEvent struct {
	Scope     string
	RequestID string
	Endpoint  string
	AuthIndex string
	Outcome   RequestOutcome
	Record    *UsageRecord
	At        time.Time
	// The cycle fields identify the subscription window the request was admitted
	// in, so a late completion is never charged to a newer period or binding.
	CyclePlanID  string
	CycleStartAt time.Time
}

func (s *Store) RecordUsage(event UsageEvent) {
	scope := strings.TrimSpace(event.Scope)
	if scope == "" {
		return
	}
	at := event.At
	if at.IsZero() {
		at = s.Now()
	}
	if event.Record == nil {
		return
	}
	record := *event.Record
	if !record.Generate {
		return
	}
	// An upstream that answered with an error produced nothing to bill and
	// nothing to attribute; that belongs in CPA's log, not this one. A failure
	// that arrives after output has flowed is a different thing: those tokens
	// exist, so the request stays visible here like a canceled one does.
	if event.Outcome == OutcomeFailed && !record.Responded {
		return
	}

	updateResult(s, func(state *State) (struct{}, Changes) {
		key := state.ensureKey(scope)

		upstreamModel := modelWithoutSuffix(record.UpstreamModel)
		if upstreamModel == "" {
			upstreamModel = modelWithoutSuffix(record.BillingModel)
		}
		billingModel := strings.TrimSpace(record.BillingModel)
		if billingModel == "" {
			billingModel = upstreamModel
		}
		price := state.ResolvePrice(upstreamModel, billingModel)
		cost := ComputeCost(price, record.Breakdown)
		totals := Totals{
			CostUSD:             cost.TotalUSD,
			Requests:            1,
			UncachedInputTokens: cost.UncachedInputTokens,
			OutputTokens:        cost.BilledOutputTokens,
			ReasoningTokens:     record.Breakdown.Output.ReasoningTokens,
			CacheReadTokens:     cost.CacheReadTokens,
			CacheCreationTokens: cost.CacheWriteTokens,
		}
		key.Lifetime.Add(totals)
		key.addModelTotals(billingModel, totals)

		entryAt := record.RequestedAt
		if entryAt.IsZero() {
			entryAt = at
		}
		entry := LogEntry{
			At:                entryAt,
			Scope:             scope,
			RequestID:         event.RequestID,
			Endpoint:          event.Endpoint,
			AuthIndex:         event.AuthIndex,
			UpstreamModel:     upstreamModel,
			BillingModel:      billingModel,
			Outcome:           event.Outcome,
			AccountingQuality: record.Breakdown.Quality,
			PriceSource:       price.Source,
			Cost:              cost,
			ReasoningTokens:   record.Breakdown.Output.ReasoningTokens,
		}
		chargeCycle(key, event, cost.TotalUSD)
		// A completion may arrive after its period ended. Close it now, but do
		// not start the next period until another request is admitted.
		if plan, hasPlan := state.FindPlan(key.PlanID); hasPlan {
			settleExpiredCycle(key, plan, at)
		}
		return struct{}{}, Changes{
			Keys:      []string{scope},
			Log:       []LogEntry{entry},
			LogCutoff: at.Add(-LogRetention),
		}
	})
}

// chargeCycle charges the cost to the subscription window the request was
// admitted in, and only to that window. A completion that arrives after its
// window rolled, or after an operator rebound the key, finds a window it does
// not belong to and is left out of it — the spend still lands in the lifetime
// totals and the billing log. An unbound key has no window at all.
func chargeCycle(key *KeyState, event UsageEvent, costUSD float64) {
	if event.CyclePlanID == "" || event.CycleStartAt.IsZero() {
		return
	}
	if key.Cycle.PlanID != event.CyclePlanID || !key.Cycle.StartAt.Equal(event.CycleStartAt) {
		return
	}
	key.Cycle.SpentUSD += costUSD
}

func (s *State) ensureKey(scope string) *KeyState {
	if s.Keys == nil {
		s.Keys = make(map[string]*KeyState)
	}
	key := s.Keys[scope]
	if key == nil {
		key = &KeyState{ByModel: make(map[string]*Totals)}
		s.Keys[scope] = key
	}
	if key.ByModel == nil {
		key.ByModel = make(map[string]*Totals)
	}
	return key
}

func (k *KeyState) addModelTotals(model string, totals Totals) {
	if k.ByModel == nil {
		k.ByModel = make(map[string]*Totals)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	entry := k.ByModel[model]
	if entry == nil {
		entry = &Totals{}
		k.ByModel[model] = entry
	}
	entry.Add(totals)
}

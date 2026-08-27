package billing

import (
	"strings"
	"time"
)

type UsageEvent struct {
	Scope         string
	AuthIndex     string
	Provider      string
	AuthType      string
	Account       string
	UpstreamModel string
	RouteModel    string
	RequestedAt   time.Time
	Latency       time.Duration
	TTFT          time.Duration
	Failed        bool
	Breakdown     TokenBreakdown
	At            time.Time
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
	event.At = at
	updateResult(s, func(state *State) (struct{}, Changes) {
		key := state.ensureKey(scope)

		upstreamModel := modelWithoutSuffix(event.UpstreamModel)
		if upstreamModel == "" {
			upstreamModel = modelWithoutSuffix(event.RouteModel)
		}
		billingModel := state.ResolveBillingModel(event.UpstreamModel, event.RouteModel)
		price := state.ResolvePrice(upstreamModel, billingModel)
		cost := ComputeCost(price, event.Breakdown)
		totals := Totals{
			CostUSD:             cost.TotalUSD,
			Requests:            1,
			UncachedInputTokens: cost.UncachedInputTokens,
			OutputTokens:        cost.BilledOutputTokens,
			ReasoningTokens:     event.Breakdown.Output.ReasoningTokens,
			CacheReadTokens:     cost.CacheReadTokens,
			CacheCreationTokens: cost.CacheWriteTokens,
		}
		key.Lifetime.Add(totals)
		key.addModelTotals(billingModel, totals)

		entryAt := event.RequestedAt
		if entryAt.IsZero() {
			entryAt = at
		}
		entry := LogEntry{
			At:                entryAt,
			Scope:             scope,
			AuthIndex:         event.AuthIndex,
			UpstreamModel:     upstreamModel,
			BillingModel:      billingModel,
			Failed:            event.Failed,
			LatencyMS:         durationMillis(event.Latency),
			TTFTMS:            durationMillis(event.TTFT),
			AccountingQuality: event.Breakdown.Quality,
			PriceSource:       price.Source,
			Cost:              cost,
			ReasoningTokens:   event.Breakdown.Output.ReasoningTokens,
		}
		chargeCycle(key, event, cost.TotalUSD)
		// A completion may arrive after its period ended. Close it now, but do
		// not start the next period until another request is admitted.
		if plan, hasPlan := state.FindPlan(key.PlanID); hasPlan {
			settleExpiredCycle(key, plan, at)
		}
		return struct{}{}, Changes{
			Keys:        []string{scope},
			Credentials: learnCredential(state, scope, event.AuthIndex, event.Provider, event.AuthType, event.Account),
			Log:         []LogEntry{entry},
			LogCutoff:   at.Add(-LogRetention),
		}
	})
}

func durationMillis(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	if milliseconds := duration.Milliseconds(); milliseconds > 0 {
		return milliseconds
	}
	return 1
}

// chargeCycle charges only a window opened when the request was admitted.
// Usage completion never starts a window: after a reset or rebind, an older
// in-flight request must not create and spend a new subscription period.
func chargeCycle(key *KeyState, event UsageEvent, costUSD float64) {
	if key.PlanID == "" || key.Cycle.StartAt.IsZero() || key.Cycle.PlanID != key.PlanID {
		return
	}
	requestedAt := event.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = event.At
	}
	if requestedAt.Before(key.Cycle.StartAt) {
		return
	}
	if !key.Cycle.EndAt.IsZero() && !requestedAt.Before(key.Cycle.EndAt) {
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

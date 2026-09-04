package billing

import (
	"strings"
	"time"
)

type UsageEvent struct {
	Scope           string
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
	updateResult(s, func(state *State) (struct{}, Changes) {
		upstreamModel := strings.TrimSpace(event.UpstreamModel)
		if upstreamModel == "" {
			upstreamModel = strings.TrimSpace(event.RouteModel)
		}
		billingModel := state.ResolveBillingModel(event.UpstreamModel, event.RouteModel)
		price := state.ResolvePrice(upstreamModel, billingModel)
		cost := ComputeCost(price, event.Breakdown)
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
		if scope != "" {
			key := state.ensureKey(scope)
			if !failed || usageBreakdownPresent(event.Breakdown) {
				chargeCycle(key, event, cost.TotalUSD)
			}
			// A completion may arrive after its period ended. Close it now, but do
			// not start the next period until another request is admitted.
			if plan, hasPlan := state.FindPlan(key.PlanID); hasPlan {
				settleExpiredCycle(key, plan, at)
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
		key = &KeyState{}
		s.Keys[scope] = key
	}
	return key
}

func usageBreakdownPresent(value TokenBreakdown) bool {
	return value.TotalTokens != 0 || value.Input.TotalTokens != 0 || value.Output.TotalTokens != 0 ||
		value.UnclassifiedTokens != 0 || value.Quality != ""
}

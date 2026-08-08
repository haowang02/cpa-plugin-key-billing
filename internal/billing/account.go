package billing

import (
	"strings"
	"time"
)

// UsageRecord is one canonical provider usage event produced before response
// translation. Multiple records may belong to one downstream request.
type UsageRecord struct {
	Provider     string
	ExecutorType string
	Model        string
	Alias        string
	Generate     bool
	Failed       bool
	RequestedAt  time.Time
	Breakdown    TokenBreakdown
}

// UsageEvent commits every canonical provider record associated with one
// terminal downstream request.
type UsageEvent struct {
	Scope          string
	Preview        string
	RequestID      string
	ClientProtocol string
	Failed         bool
	Records        []UsageRecord
	At             time.Time
	// AttributionKnown and the cycle fields snapshot subscription state when
	// the downstream request was admitted. They prevent a late completion from
	// being charged to a newer period or binding.
	AttributionKnown bool
	CyclePlanID      string
	CycleStartAt     time.Time
	CycleEndAt       time.Time
	CycleLimitUSD    float64
}

// RecordUsage prices and commits canonical usage records atomically. Provider
// attempts are intentionally not deduplicated: retries and additional model
// calls are separate upstream costs, matching CLIProxyAPI's usage event model.
func (s *Store) RecordUsage(event UsageEvent) {
	scope := strings.TrimSpace(event.Scope)
	if scope == "" {
		return
	}
	at := event.At
	if at.IsZero() {
		at = s.Now()
	}
	if len(event.Records) == 0 {
		s.usageNoTokens.Add(1)
		return
	}
	hasGenerated := false
	for _, record := range event.Records {
		if record.Generate {
			hasGenerated = true
			break
		}
	}
	if !hasGenerated {
		return
	}

	s.Update(func(state *State) {
		key := state.ensureKey(scope, at)
		if key.Preview == "" && event.Preview != "" {
			key.Preview = event.Preview
		}
		key.LastSeen = at

		plan, hasPlan := state.FindPlan(key.PlanID)
		if !event.AttributionKnown {
			if hasPlan {
				activateCycle(key, plan, at)
			} else if key.PlanID != "" {
				key.PlanID = ""
				key.Cycle = Cycle{}
			}
		}

		usageIndex := 0
		for _, record := range event.Records {
			if !record.Generate {
				continue
			}
			index := usageIndex
			usageIndex++
			s.usageRecorded.Add(1)
			if !record.Breakdown.Billable() {
				s.usageUnclassified.Add(1)
			}
			model := strings.TrimSpace(record.Model)
			alias := strings.TrimSpace(record.Alias)
			if model == "" {
				model = alias
			}
			price := state.ResolvePrice(model, alias)
			if price.Source == PriceSourceNone {
				s.usageUnpriced.Add(1)
			}
			if record.Breakdown.TotalTokens == 0 {
				s.usageNoTokens.Add(1)
			}
			cost := ComputeCost(price, record.Breakdown)
			failed := event.Failed || record.Failed
			totals := Totals{
				CostUSD:             cost.TotalUSD,
				Requests:            1,
				UncachedInputTokens: cost.UncachedInputTokens,
				OutputTokens:        cost.BilledOutputTokens,
				ReasoningTokens:     record.Breakdown.Output.ReasoningTokens,
				CacheReadTokens:     cost.CacheReadTokens,
				CacheCreationTokens: cost.CacheWriteTokens,
			}
			if failed {
				totals.FailedRequests = 1
			}
			key.Lifetime.Add(totals)
			key.addModelTotals(model, totals)

			entryAt := record.RequestedAt
			if entryAt.IsZero() {
				entryAt = at
			}
			entry := LogEntry{
				At:                entryAt,
				Scope:             scope,
				Preview:           key.Preview,
				RequestID:         event.RequestID,
				UsageIndex:        index,
				ClientProtocol:    event.ClientProtocol,
				Provider:          record.Provider,
				ExecutorType:      record.ExecutorType,
				Model:             model,
				Failed:            failed,
				AccountingQuality: record.Breakdown.Quality,
				PriceSource:       price.Source,
				Cost:              cost,
				ReasoningTokens:   record.Breakdown.Output.ReasoningTokens,
			}
			if alias != "" && alias != model {
				entry.Alias = alias
			}

			if planID, spentUSD, limitUSD, attributed := attributeCycleUsage(key, plan, hasPlan, event, cost.TotalUSD); attributed {
				entry.PlanID = planID
				entry.CycleSpentUSD = spentUSD
				entry.CycleLimitUSD = limitUSD
			}
			appendLog(state, entry, s.cfg.LogEntries)
		}
		// A completion attributed to the current period may arrive after its end.
		// Archive it now, but do not start the next period until another request
		// is admitted.
		if event.AttributionKnown && hasPlan {
			settleExpiredCycle(key, plan, at)
		}
	})
}

func attributeCycleUsage(key *KeyState, currentPlan Plan, hasCurrentPlan bool, event UsageEvent, costUSD float64) (string, float64, float64, bool) {
	if !event.AttributionKnown {
		if !hasCurrentPlan {
			return "", 0, 0, false
		}
		key.Cycle.SpentUSD += costUSD
		key.Cycle.Requests++
		return currentPlan.ID, key.Cycle.SpentUSD, currentPlan.AmountUSD, true
	}
	if event.CyclePlanID == "" || event.CycleStartAt.IsZero() {
		return "", 0, 0, false
	}
	if key.Cycle.PlanID == event.CyclePlanID && key.Cycle.StartAt.Equal(event.CycleStartAt) {
		key.Cycle.SpentUSD += costUSD
		key.Cycle.Requests++
		return event.CyclePlanID, key.Cycle.SpentUSD, event.CycleLimitUSD, true
	}
	for index := len(key.RecentCycles) - 1; index >= 0; index-- {
		cycle := &key.RecentCycles[index]
		if cycle.PlanID != event.CyclePlanID || !cycle.StartAt.Equal(event.CycleStartAt) {
			continue
		}
		cycle.SpentUSD += costUSD
		cycle.Requests++
		return cycle.PlanID, cycle.SpentUSD, cycle.LimitUSD, true
	}
	cycle := CycleSummary{
		StartAt:  event.CycleStartAt,
		EndAt:    event.CycleEndAt,
		SpentUSD: costUSD,
		Requests: 1,
		LimitUSD: event.CycleLimitUSD,
		PlanID:   event.CyclePlanID,
	}
	key.RecentCycles = append(key.RecentCycles, cycle)
	if len(key.RecentCycles) > MaxRecentCycles {
		key.RecentCycles = key.RecentCycles[len(key.RecentCycles)-MaxRecentCycles:]
	}
	return cycle.PlanID, cycle.SpentUSD, cycle.LimitUSD, true
}

func (s *State) ensureKey(scope string, at time.Time) *KeyState {
	if s.Keys == nil {
		s.Keys = make(map[string]*KeyState)
	}
	key := s.Keys[scope]
	if key == nil {
		key = &KeyState{Scope: scope, FirstSeen: at, ByModel: make(map[string]*Totals)}
		s.Keys[scope] = key
	}
	if key.ByModel == nil {
		key.ByModel = make(map[string]*Totals)
	}
	if key.FirstSeen.IsZero() {
		key.FirstSeen = at
	}
	return key
}

// addModelTotals accumulates per-model statistics, folding overflow into a
// shared bucket so a client cycling through model names cannot grow the state.
func (k *KeyState) addModelTotals(model string, totals Totals) {
	if k.ByModel == nil {
		k.ByModel = make(map[string]*Totals)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = OtherModelsBucket
	}
	if _, exists := k.ByModel[model]; !exists && len(k.ByModel) >= MaxModelsPerKey {
		model = OtherModelsBucket
	}
	entry := k.ByModel[model]
	if entry == nil {
		entry = &Totals{}
		k.ByModel[model] = entry
	}
	entry.Add(totals)
}

package billing

import (
	"strings"
	"time"
)

// UsageEvent is one completed request, ready to be priced and attributed.
// Scope is the caller scope of the downstream API key.
type UsageEvent struct {
	Scope   string
	Preview string
	Model   string
	Alias   string
	// Semantics is how the token buckets nest, detected from the payload the
	// counters were read out of.
	Semantics Semantics
	Failed    bool
	Tokens    TokenUsage
	At        time.Time
}

// RecordUsage prices one usage record and accumulates it against its key.
//
// Every attributable record lands in the lifetime and per-model statistics,
// whether or not the key has a subscription: unbound keys are unlimited but
// still reported. Only bound keys also accumulate against a cycle.
func (s *Store) RecordUsage(event UsageEvent) {
	if s == nil {
		return
	}
	scope := strings.TrimSpace(event.Scope)
	if scope == "" {
		return
	}
	at := event.At
	if at.IsZero() {
		at = s.Now()
	}
	model := strings.TrimSpace(event.Model)
	alias := strings.TrimSpace(event.Alias)
	if model == "" {
		model = alias
	}

	s.usageRecorded.Add(1)
	s.Update(func(state *State) {
		price := state.ResolvePrice(model, alias)
		if price.Source == PriceSourceNone {
			s.usageUnpriced.Add(1)
		}
		if event.Tokens == (TokenUsage{}) {
			s.usageNoTokens.Add(1)
		}
		cost := ComputeCost(price, event.Semantics, event.Tokens)

		key := state.ensureKey(scope, at)
		if key.Preview == "" && event.Preview != "" {
			key.Preview = event.Preview
		}
		key.LastSeen = at

		totals := Totals{
			CostUSD:             cost.TotalUSD,
			Requests:            1,
			UncachedInputTokens: cost.UncachedInputTokens,
			OutputTokens:        cost.BilledOutputTokens,
			ReasoningTokens:     nonNegative(event.Tokens.ReasoningTokens),
			CacheReadTokens:     cost.CacheReadTokens,
			CacheCreationTokens: cost.CacheWriteTokens,
		}
		if event.Failed {
			totals.FailedRequests = 1
		}
		key.Lifetime.Add(totals)
		key.addModelTotals(model, totals)

		entry := LogEntry{
			At:              at,
			Scope:           scope,
			Preview:         key.Preview,
			Model:           model,
			Failed:          event.Failed,
			PriceSource:     price.Source,
			Cost:            cost,
			ReasoningTokens: totals.ReasoningTokens,
		}
		if alias != "" && alias != model {
			entry.Alias = alias
		}

		if plan, ok := state.FindPlan(key.PlanID); ok {
			rollCycle(key, plan, at, s.locationLocked())
			key.Cycle.SpentUSD += cost.TotalUSD
			key.Cycle.Requests++
			entry.PlanID = plan.ID
			entry.CycleSpentUSD = key.Cycle.SpentUSD
			entry.CycleLimitUSD = plan.AmountUSD
		} else if key.PlanID != "" {
			// The bound plan was deleted. Treat the key as unlimited rather
			// than blocking it, and drop the stale window.
			key.PlanID = ""
			key.Cycle = Cycle{}
		}

		// Logged last so the entry carries the window as it stands after this
		// request, which is the number an operator compares against the budget.
		appendLog(state, entry, s.cfg.LogEntries)
	})
}

// ensureKey returns the state for a scope, creating it on first sight.
//
// Auto-creation is what makes statistics complete for keys that were never
// bound to a plan and for deployments where the administrator has not opened
// the plugin page yet.
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
// shared bucket so a client cycling through model names cannot grow the state
// file without bound.
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

// locationLocked returns the default time zone. It reads s.loc without taking
// s.mu because every caller already holds it through Update/UpdateResult.
func (s *Store) locationLocked() *time.Location {
	if s == nil || s.loc == nil {
		return time.UTC
	}
	return s.loc
}

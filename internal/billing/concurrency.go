package billing

import "strings"

const MaxConcurrencyLimit = 10000

// SlotDecision reports the result of reserving one generation concurrency
// slot. Active is the number of slots already occupied when a request is
// refused, or the number occupied after a successful reservation.
type SlotDecision struct {
	Allowed  bool
	Acquired bool
	Limit    int
	Active   int
}

// AcquireSlot atomically checks and reserves one API-key-scoped generation
// slot. Every attributable generation is tracked even while its limit is zero,
// so lowering an unlimited key to a finite limit immediately accounts for
// requests that were already running.
func (s *Store) AcquireSlot(scope, requestID string) SlotDecision {
	decision := SlotDecision{Allowed: true}
	scope = strings.TrimSpace(scope)
	requestID = strings.TrimSpace(requestID)
	if scope == "" {
		return decision
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if key := s.state.Keys[scope]; key != nil {
		decision.Limit = key.ConcurrencyLimit
	}
	decision.Active = s.activeByScope[scope]
	if requestID == "" {
		// A finite limit cannot be enforced safely without an exact completion
		// correlation. Current compatible hosts always provide RequestID.
		decision.Allowed = decision.Limit <= 0
		return decision
	}
	if existingScope, exists := s.activeRequests[requestID]; exists {
		decision.Allowed = existingScope == scope
		// The original admission owns this reservation. Reporting a duplicate
		// as newly acquired would let a later admission check roll it back.
		decision.Acquired = false
		return decision
	}
	if decision.Limit > 0 && decision.Active >= decision.Limit {
		decision.Allowed = false
		return decision
	}

	s.activeRequests[requestID] = scope
	s.activeByScope[scope] = decision.Active + 1
	decision.Active++
	decision.Acquired = true
	return decision
}

// ReleaseSlot idempotently releases the slot reserved for requestID. Unknown
// and duplicate completion events are harmless.
func (s *Store) ReleaseSlot(requestID string) bool {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scope, exists := s.activeRequests[requestID]
	if !exists {
		return false
	}
	delete(s.activeRequests, requestID)
	if active := s.activeByScope[scope]; active > 1 {
		s.activeByScope[scope] = active - 1
	} else {
		delete(s.activeByScope, scope)
	}
	return true
}

func (s *Store) SetConcurrencyLimit(scope string, limit int) error {
	scope = normalizeScope(scope)
	if scope == "" {
		return invalidf("API Key 标识不能为空")
	}
	if limit < 0 || limit > MaxConcurrencyLimit {
		return invalidf("并发限制必须为 0 到 %d 的整数", MaxConcurrencyLimit)
	}

	var errApply error
	updateResult(s, func(state *State) (struct{}, Changes) {
		key := state.liveKey(scope)
		if key == nil {
			errApply = notFoundf("API Key %q 不存在", scope)
			return struct{}{}, Changes{}
		}
		if key.ConcurrencyLimit == limit {
			return struct{}{}, Changes{}
		}
		key.ConcurrencyLimit = limit
		return struct{}{}, Changes{Keys: []string{scope}}
	})
	return errApply
}

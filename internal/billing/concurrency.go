package billing

import "strings"

const MaxConcurrencyLimit = 10000

type concurrencySlot struct {
	Scope         string
	CredentialRef string
}

// Active counts occupied slots: before a refusal, or after a successful reservation.
type SlotDecision struct {
	Allowed bool
	Limit   int
	Active  int
}

// Track unlimited keys too, so lowering a limit includes requests already running.
func (s *Store) AcquireSlot(scope, requestID string) SlotDecision {
	decision := SlotDecision{Allowed: true}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return decision
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if key := s.state.Keys[scope]; key != nil {
		decision.Limit = key.ConcurrencyLimit
	}
	decision.Active = s.activeByScope[scope]
	if decision.Limit > 0 && decision.Active >= decision.Limit {
		decision.Allowed = false
		return decision
	}

	s.activeRequests[requestID] = concurrencySlot{Scope: scope}
	s.activeByScope[scope] = decision.Active + 1
	decision.Active++
	return decision
}

// Retries keep the request ID and replace its credential.
func (s *Store) AssignSlotCredential(requestID, credentialRef string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	slot, exists := s.activeRequests[requestID]
	if !exists {
		return
	}
	slot.CredentialRef = credentialRef
	s.activeRequests[requestID] = slot
}

func (s *Store) CredentialConcurrency(scope string) map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := make(map[string]int)
	for _, slot := range s.activeRequests {
		if slot.CredentialRef != "" && (scope == "" || slot.Scope == scope) {
			counts[slot.CredentialRef]++
		}
	}
	return counts
}

// Completion also covers requests that never acquired a slot.
func (s *Store) ReleaseSlot(requestID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	slot, exists := s.activeRequests[requestID]
	if !exists {
		return false
	}
	delete(s.activeRequests, requestID)
	if active := s.activeByScope[slot.Scope]; active > 1 {
		s.activeByScope[slot.Scope] = active - 1
	} else {
		delete(s.activeByScope, slot.Scope)
	}
	return true
}

func (s *Store) SetConcurrencyLimit(scope string, limit int) error {
	scope = normalizeScope(scope)
	if scope == "" {
		return invalidf("API key identifier is required")
	}
	if limit < 0 || limit > MaxConcurrencyLimit {
		return invalidf("Concurrency limit must be an integer from 0 to %d", MaxConcurrencyLimit)
	}

	_, err := editConfiguration(s, func(state *State) (struct{}, Changes, error) {
		key := state.liveKey(scope)
		if key == nil {
			return struct{}{}, Changes{}, notFoundf("API key %q does not exist", scope)
		}
		if key.ConcurrencyLimit == limit {
			return struct{}{}, Changes{}, nil
		}
		key.ConcurrencyLimit = limit
		return struct{}{}, Changes{Keys: []string{scope}}, nil
	})
	return err
}

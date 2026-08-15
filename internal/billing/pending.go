package billing

import (
	"strings"
	"sync"
	"time"
)

// The host promises exactly one terminal event per intercepted request, so
// in-flight entries normally clear themselves. These two bound what a host bug
// or a lost callback can accumulate: the TTL is long enough that a slow stream
// is still billable when it ends, and the count is the hard memory cap.
const (
	MaxPendingRequests = 8192
	PendingTTL         = 24 * time.Hour
)

type PendingRequest struct {
	Scope     string
	Endpoint  string
	StartedAt time.Time
	// AuthIndex identifies the upstream credential currently serving the
	// request. The host selects one per attempt, so a retried request ends up
	// attributed to the credential that produced its response.
	AuthIndex string
	// Cycle fields identify the subscription window at admission. Completion can
	// arrive after that window ended or after an operator changed the binding.
	CyclePlanID  string
	CycleStartAt time.Time
}

type pendingTable struct {
	mu      sync.Mutex
	entries map[string]PendingRequest
}

// begin registers an admitted request. A duplicate ID overwrites, which is the
// safe choice: the newer interception is the live one.
func (p *pendingTable) begin(requestID string, entry PendingRequest, now time.Time) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	entry.StartedAt = now
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepLocked(now)
	if _, exists := p.entries[requestID]; !exists && len(p.entries) >= MaxPendingRequests {
		// Table is full even after sweeping. Dropping the new entry loses one
		// request's billing, which is strictly better than unbounded growth.
		return
	}
	p.entries[requestID] = entry
}

func (p *pendingTable) setAuthIndex(requestID, authIndex string) {
	requestID = strings.TrimSpace(requestID)
	authIndex = strings.TrimSpace(authIndex)
	if requestID == "" || authIndex == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, exists := p.entries[requestID]
	if !exists {
		return
	}
	entry.AuthIndex = authIndex
	p.entries[requestID] = entry
}

func (p *pendingTable) finish(requestID string) (PendingRequest, bool) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return PendingRequest{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, exists := p.entries[requestID]
	if !exists {
		return PendingRequest{}, false
	}
	delete(p.entries, requestID)
	return entry, true
}

func (p *pendingTable) sweepLocked(now time.Time) {
	for requestID, entry := range p.entries {
		if now.Sub(entry.StartedAt) > PendingTTL {
			delete(p.entries, requestID)
		}
	}
}

func (s *Store) BeginRequest(requestID string, entry PendingRequest) {
	s.pending.begin(requestID, entry, s.Now())
}

// SetRequestCredential records which upstream credential is serving an
// in-flight request. The host calls the after-auth interceptor once per
// attempt, so the last call before completion names the credential that
// answered. A request that was never admitted is ignored: there is nothing to
// bill and nothing to attribute.
func (s *Store) SetRequestCredential(requestID, authIndex string) {
	s.pending.setAuthIndex(requestID, authIndex)
}

// It is the single commit point for every outcome — success, failure and
// cancellation alike. A nil record means the request was never tracked, so
// there is nothing to log about it beyond the counter. reason is what the
// client was told went wrong, when the plugin saw it.
func (s *Store) FinishRequest(requestID string, record *UsageRecord, outcome RequestOutcome, reason string) {
	entry, exists := s.pending.finish(requestID)
	if !exists {
		return
	}
	if entry.Scope == "" {
		return
	}
	// The plugin log names a failure before billing decides whether there is
	// anything to charge for it, because the failures with nothing to charge
	// are the ones nothing else records.
	if outcome == OutcomeFailed {
		s.reportFailedRequest(requestID, entry, record, reason)
	}
	s.RecordUsage(UsageEvent{
		Scope:        entry.Scope,
		RequestID:    requestID,
		Endpoint:     entry.Endpoint,
		AuthIndex:    entry.AuthIndex,
		Outcome:      outcome,
		Record:       record,
		At:           s.Now(),
		CyclePlanID:  entry.CyclePlanID,
		CycleStartAt: entry.CycleStartAt,
	})
}

func (s *Store) DiscardRequest(requestID string) {
	s.pending.finish(requestID)
}

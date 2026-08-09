package billing

import (
	"strings"
	"sync"
	"time"
)

const (
	// MaxPendingRequests bounds the in-flight table. The host promises exactly
	// one terminal event per intercepted request, so entries normally clear
	// themselves; this cap plus PendingTTL is the backstop that keeps a host
	// bug or a lost callback from growing memory without limit.
	MaxPendingRequests = 8192
	// PendingTTL keeps long streams billable while still reclaiming callbacks
	// the host never delivers. MaxPendingRequests provides the hard memory cap.
	PendingTTL = 24 * time.Hour
)

// PendingRequest accumulates one in-flight request between interception and its
// terminal event. Nothing here is persisted.
type PendingRequest struct {
	Scope     string
	Endpoint  string
	StartedAt time.Time
	// AuthIndex identifies the upstream credential currently serving the
	// request. The host selects one per attempt, so a retried request ends up
	// attributed to the credential that produced its response.
	AuthIndex string
	// Cycle fields capture the subscription window at admission. Completion can
	// arrive after that window ended or after an operator changed the binding.
	CyclePlanID   string
	CycleStartAt  time.Time
	CycleEndAt    time.Time
	CycleLimitUSD float64
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

// setAuthIndex attributes an admitted request to the credential the host just
// selected for it. An unknown request ID is ignored: it was never admitted, so
// there is nothing to bill and nothing to attribute.
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

func (p *pendingTable) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
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
// answered.
func (s *Store) SetRequestCredential(requestID, authIndex string) {
	s.pending.setAuthIndex(requestID, authIndex)
}

// FinishRequest bills an in-flight request and clears it.
//
// It is the single commit point for every outcome — success, failure, and
// cancellation alike. Each canonical upstream usage record is retained, so
// retries are billed exactly as separate usage events.
func (s *Store) FinishRequest(requestID string, records []UsageRecord, failed bool) {
	entry, exists := s.pending.finish(requestID)
	if !exists {
		return
	}
	if entry.Scope == "" {
		return
	}
	s.RecordUsage(UsageEvent{
		Scope:            entry.Scope,
		RequestID:        requestID,
		Endpoint:         entry.Endpoint,
		AuthIndex:        entry.AuthIndex,
		Failed:           failed,
		Records:          records,
		At:               s.Now(),
		AttributionKnown: true,
		CyclePlanID:      entry.CyclePlanID,
		CycleStartAt:     entry.CycleStartAt,
		CycleEndAt:       entry.CycleEndAt,
		CycleLimitUSD:    entry.CycleLimitUSD,
	})
}

func (s *Store) DiscardRequest(requestID string) {
	s.pending.finish(requestID)
}

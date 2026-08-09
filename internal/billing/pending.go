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
	Scope          string
	ClientProtocol string
	StartedAt      time.Time
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
		ClientProtocol:   entry.ClientProtocol,
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

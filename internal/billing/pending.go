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
	// PendingTTL is how long an unfinished request may occupy the table.
	// Long-running streams can legitimately last many minutes.
	PendingTTL = 30 * time.Minute
)

// PendingRequest accumulates one in-flight request between interception and its
// terminal event. Nothing here is persisted.
type PendingRequest struct {
	// Scope is the caller scope of the downstream key to bill.
	Scope string
	// Preview is a masked rendering of the key, when known.
	Preview string
	// Model is the upstream model; Alias is what the client asked for.
	Model string
	Alias string
	// Semantics is how the observed token buckets nest, detected from the
	// response payloads. It stays empty until one is observed: the request says
	// nothing reliable about it, because CPA may translate protocols in
	// between.
	Semantics Semantics
	// Tokens is the running merge of every usage block observed so far.
	Tokens TokenUsage
	// StartedAt is when the request was admitted, which is what ages an entry
	// out when its terminal event never arrives.
	StartedAt time.Time
}

// pendingTable tracks in-flight requests by host request ID.
type pendingTable struct {
	mu      sync.Mutex
	entries map[string]*PendingRequest
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
	p.entries[requestID] = &entry
}

// observe merges a usage block into an in-flight request. Unknown request IDs
// are ignored: they belong to requests this plugin did not admit.
func (p *pendingTable) observe(requestID string, semantics Semantics, usage TokenUsage) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.entries[requestID]
	if entry == nil {
		return
	}
	// What the payload carries overrides the guess made at admission time: the
	// counters are being read out of this body, not out of the request.
	entry.Semantics = mergeSemantics(entry.Semantics, semantics)
	entry.Tokens.MergeMax(usage)
}

// noteUpstreamModel records the model chosen after credential selection.
func (p *pendingTable) noteUpstreamModel(requestID, model string) {
	requestID = strings.TrimSpace(requestID)
	model = strings.TrimSpace(model)
	if requestID == "" || model == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if entry := p.entries[requestID]; entry != nil {
		entry.Model = model
	}
}

// finish removes and returns an in-flight request.
func (p *pendingTable) finish(requestID string) (PendingRequest, bool) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return PendingRequest{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, exists := p.entries[requestID]
	if !exists || entry == nil {
		return PendingRequest{}, false
	}
	delete(p.entries, requestID)
	return *entry, true
}

func (p *pendingTable) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// sweepLocked drops entries whose terminal event never arrived.
func (p *pendingTable) sweepLocked(now time.Time) {
	for requestID, entry := range p.entries {
		if entry == nil || now.Sub(entry.StartedAt) > PendingTTL {
			delete(p.entries, requestID)
		}
	}
}

// BeginRequest records an admitted request so its response can be billed to the
// right key when the terminal event arrives.
func (s *Store) BeginRequest(requestID string, entry PendingRequest) {
	if s == nil || s.pending == nil {
		return
	}
	s.pending.begin(requestID, entry, s.Now())
}

// NoteUpstreamModel records the upstream model an in-flight request was routed
// to, so a price rule can match either that name or the client-facing alias.
func (s *Store) NoteUpstreamModel(requestID, model string) {
	if s == nil || s.pending == nil {
		return
	}
	s.pending.noteUpstreamModel(requestID, model)
}

// ObserveTokens merges usage parsed from a response body or stream chunk,
// together with the layout that body turned out to use.
func (s *Store) ObserveTokens(requestID string, semantics Semantics, usage TokenUsage) {
	if s == nil || s.pending == nil {
		return
	}
	s.pending.observe(requestID, semantics, usage)
}

// FinishRequest bills an in-flight request and clears it.
//
// It is the single commit point for every outcome — success, failure, and
// cancellation alike — which is what makes billing exactly-once per client
// request even when the host retries across several upstream credentials.
func (s *Store) FinishRequest(requestID string, failed bool) {
	if s == nil || s.pending == nil {
		return
	}
	entry, exists := s.pending.finish(requestID)
	if !exists {
		return
	}
	s.usageReceived.Add(1)
	if entry.Scope == "" {
		s.usageUnattributed.Add(1)
		return
	}
	s.RecordUsage(UsageEvent{
		Scope:     entry.Scope,
		Preview:   entry.Preview,
		Model:     entry.Model,
		Alias:     entry.Alias,
		Semantics: entry.Semantics,
		Failed:    failed,
		Tokens:    entry.Tokens,
		At:        s.Now(),
	})
}

// DiscardRequest drops an in-flight request without billing it.
func (s *Store) DiscardRequest(requestID string) {
	if s == nil || s.pending == nil {
		return
	}
	s.pending.finish(requestID)
}

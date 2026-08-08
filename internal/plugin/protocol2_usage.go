package plugin

import (
	"crypto/sha256"
	"strings"
	"sync"
	"time"

	"cpa-key-billing/internal/billing"
)

// Protocol 2 splits correlation and authoritative usage across response hooks.
// Raw usage arrives before translation without a request ID; translated responses
// arrive with the request ID but may contain estimated tokens. Only the former
// is accumulated, while the latter is used solely to bind stable response IDs.
type protocol2UsageTracker struct {
	mu             sync.Mutex
	requests       map[string]*protocol2Request
	routes         map[protocol2Route]map[string]struct{}
	responseOwners map[string]string
	orphans        map[string]*protocol2Observation
}

type protocol2Request struct {
	clientProtocol string
	model          string
	alias          string
	provider       string
	generate       bool
	startedAt      time.Time
	routes         map[protocol2Route]struct{}
	usage          upstreamUsage
}

type protocol2Route struct {
	provider    string
	model       string
	stream      bool
	requestHash [sha256.Size]byte
}

type protocol2Observation struct {
	provider  string
	model     string
	usage     upstreamUsage
	updatedAt time.Time
}

func newProtocol2UsageTracker() *protocol2UsageTracker {
	tracker := &protocol2UsageTracker{}
	tracker.clear()
	return tracker
}

func (t *protocol2UsageTracker) begin(requestID, clientProtocol, model, alias string, generate bool, now time.Time) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked(now)
	t.removeRequestLocked(requestID)
	if len(t.requests) >= billing.MaxPendingRequests {
		return
	}
	if strings.TrimSpace(alias) == "" {
		alias = model
	}
	t.requests[requestID] = &protocol2Request{
		clientProtocol: strings.TrimSpace(clientProtocol),
		model:          strings.TrimSpace(model),
		alias:          strings.TrimSpace(alias),
		generate:       generate,
		startedAt:      now,
		routes:         make(map[protocol2Route]struct{}),
	}
}

func (t *protocol2UsageTracker) addRoute(requestID, provider, model, alias string, stream bool, originalRequest []byte) {
	requestID = strings.TrimSpace(requestID)
	t.mu.Lock()
	defer t.mu.Unlock()
	request := t.requests[requestID]
	if request == nil {
		return
	}
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if model != "" {
		request.model = baseUsageModel(model)
	}
	if strings.TrimSpace(alias) != "" {
		request.alias = strings.TrimSpace(alias)
	}
	if provider != "" {
		request.provider = provider
	}
	key := protocol2RouteKey(provider, model, stream, originalRequest)
	request.routes[key] = struct{}{}
	owners := t.routes[key]
	if owners == nil {
		owners = make(map[string]struct{})
		t.routes[key] = owners
	}
	owners[requestID] = struct{}{}
}

func (t *protocol2UsageTracker) observeUpstream(req ResponseTransformRequest, now time.Time) {
	if len(req.Body) == 0 {
		return
	}
	frames := parseUpstreamResponse(req.FromFormat, req.Body)
	if len(frames) == 0 {
		return
	}
	route := protocol2RouteKey(req.FromFormat, req.Model, req.Stream, req.OriginalRequest)

	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked(now)
	for _, frame := range frames {
		owner := ""
		if frame.responseID != "" {
			owner = t.responseOwners[frame.responseID]
			if t.requests[owner] == nil {
				owner = ""
			}
		}
		if owner == "" {
			owners := t.routes[route]
			if len(owners) == 1 {
				for requestID := range owners {
					owner = requestID
				}
			}
		}
		if owner != "" {
			request := t.requests[owner]
			request.provider = strings.TrimSpace(req.FromFormat)
			if model := strings.TrimSpace(req.Model); model != "" {
				request.model = baseUsageModel(model)
			}
			if frame.responseID != "" {
				t.responseOwners[frame.responseID] = owner
			}
			if frame.usage.hasUsage() {
				request.usage.merge(frame.usage)
			}
			continue
		}
		if frame.responseID == "" {
			continue
		}
		observation := t.orphans[frame.responseID]
		if observation == nil {
			if len(t.orphans) >= billing.MaxPendingRequests {
				continue
			}
			observation = &protocol2Observation{}
			t.orphans[frame.responseID] = observation
		}
		observation.provider = strings.TrimSpace(req.FromFormat)
		observation.model = baseUsageModel(req.Model)
		observation.updatedAt = now
		observation.usage.merge(frame.usage)
	}
}

func (t *protocol2UsageTracker) bindResponse(requestID string, body []byte) {
	if len(body) == 0 {
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	objects := responseObjects(body)
	t.mu.Lock()
	defer t.mu.Unlock()
	request := t.requests[requestID]
	if request == nil {
		return
	}
	for _, object := range objects {
		id := responseID(object)
		if id == "" {
			continue
		}
		t.responseOwners[id] = requestID
		if observation := t.orphans[id]; observation != nil {
			request.usage.merge(observation.usage)
			if observation.provider != "" {
				request.provider = observation.provider
			}
			if observation.model != "" {
				request.model = observation.model
			}
			delete(t.orphans, id)
		}
	}
	if strings.EqualFold(strings.TrimSpace(request.provider), strings.TrimSpace(request.clientProtocol)) {
		for _, object := range objects {
			request.usage.merge(parseUsageObject(request.provider, object))
		}
	}
}

func (t *protocol2UsageTracker) finish(requestID string) []billing.UsageRecord {
	requestID = strings.TrimSpace(requestID)
	t.mu.Lock()
	request := t.requests[requestID]
	if request == nil {
		t.mu.Unlock()
		return nil
	}
	if !request.usage.hasUsage() {
		t.removeRequestLocked(requestID)
		t.mu.Unlock()
		return nil
	}
	breakdown := request.usage.breakdown()
	record := billing.UsageRecord{
		Provider:    request.provider,
		Model:       request.model,
		Alias:       request.alias,
		Generate:    request.generate,
		RequestedAt: request.startedAt,
		Breakdown:   breakdown,
	}
	t.removeRequestLocked(requestID)
	t.mu.Unlock()
	return []billing.UsageRecord{record}
}

func (t *protocol2UsageTracker) discard(requestID string) {
	t.mu.Lock()
	t.removeRequestLocked(strings.TrimSpace(requestID))
	t.mu.Unlock()
}

func (t *protocol2UsageTracker) clear() {
	t.mu.Lock()
	t.requests = make(map[string]*protocol2Request)
	t.routes = make(map[protocol2Route]map[string]struct{})
	t.responseOwners = make(map[string]string)
	t.orphans = make(map[string]*protocol2Observation)
	t.mu.Unlock()
}

func (t *protocol2UsageTracker) removeRequestLocked(requestID string) {
	request := t.requests[requestID]
	if request == nil {
		return
	}
	for route := range request.routes {
		owners := t.routes[route]
		delete(owners, requestID)
		if len(owners) == 0 {
			delete(t.routes, route)
		}
	}
	for responseID, owner := range t.responseOwners {
		if owner == requestID {
			delete(t.responseOwners, responseID)
		}
	}
	delete(t.requests, requestID)
}

func (t *protocol2UsageTracker) sweepLocked(now time.Time) {
	for requestID, request := range t.requests {
		if now.Sub(request.startedAt) > billing.PendingTTL {
			t.removeRequestLocked(requestID)
		}
	}
	for responseID, observation := range t.orphans {
		if now.Sub(observation.updatedAt) > billing.PendingTTL {
			delete(t.orphans, responseID)
		}
	}
}

func protocol2RouteKey(provider, model string, stream bool, request []byte) protocol2Route {
	return protocol2Route{
		provider:    strings.ToLower(strings.TrimSpace(provider)),
		model:       strings.ToLower(strings.TrimSpace(model)),
		stream:      stream,
		requestHash: sha256.Sum256(request),
	}
}

func baseUsageModel(model string) string {
	model = strings.TrimSpace(model)
	open := strings.LastIndex(model, "(")
	if open >= 0 && strings.HasSuffix(model, ")") {
		return strings.TrimSpace(model[:open])
	}
	return model
}

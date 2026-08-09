package plugin

import (
	"crypto/sha256"
	"strings"
	"sync"
	"time"

	"cpa-key-billing/internal/billing"
)

// Protocol 2 splits correlation and authoritative usage across response hooks.
// Raw usage arrives before translation without a request ID; translated
// responses provide the request ID used to bind stable response IDs.
type protocol2UsageTracker struct {
	mu             sync.Mutex
	requests       map[string]*protocol2Request
	routes         map[protocol2Route]map[string]struct{}
	responseOwners map[string]string
	orphans        map[string]*protocol2Observation
}

type protocol2Request struct {
	clientProtocol string
	upstreamFormat string
	upstreamModel  string
	routeModel     string
	provider       string
	generate       bool
	startedAt      time.Time
	routes         map[protocol2Route]struct{}
	usage          upstreamUsage
}

type protocol2Route struct {
	format        string
	upstreamModel string
	stream        bool
	requestHash   [sha256.Size]byte
}

type protocol2Observation struct {
	upstreamModel string
	usage         upstreamUsage
	updatedAt     time.Time
}

func newProtocol2UsageTracker() *protocol2UsageTracker {
	return &protocol2UsageTracker{
		requests:       make(map[string]*protocol2Request),
		routes:         make(map[protocol2Route]map[string]struct{}),
		responseOwners: make(map[string]string),
		orphans:        make(map[string]*protocol2Observation),
	}
}

func (t *protocol2UsageTracker) begin(requestID, clientProtocol, upstreamModel, routeModel string, generate bool, now time.Time) {
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
	if strings.TrimSpace(routeModel) == "" {
		routeModel = upstreamModel
	}
	t.requests[requestID] = &protocol2Request{
		clientProtocol: strings.TrimSpace(clientProtocol),
		upstreamModel:  strings.TrimSpace(upstreamModel),
		routeModel:     strings.TrimSpace(routeModel),
		generate:       generate,
		startedAt:      now,
		routes:         make(map[protocol2Route]struct{}),
	}
}

func (t *protocol2UsageTracker) addRoute(requestID, format, provider, upstreamModel, routeModel string, stream bool, originalRequest []byte) {
	requestID = strings.TrimSpace(requestID)
	t.mu.Lock()
	defer t.mu.Unlock()
	request := t.requests[requestID]
	if request == nil {
		return
	}
	format = strings.TrimSpace(format)
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel != "" {
		request.upstreamModel = upstreamModel
	}
	if strings.TrimSpace(routeModel) != "" {
		request.routeModel = strings.TrimSpace(routeModel)
	}
	request.upstreamFormat = format
	request.provider = strings.TrimSpace(provider)
	key := protocol2RouteKey(format, upstreamModel, stream, originalRequest)
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
			if model := strings.TrimSpace(req.Model); model != "" {
				request.upstreamModel = model
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
		observation.upstreamModel = strings.TrimSpace(req.Model)
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
			if observation.upstreamModel != "" {
				request.upstreamModel = observation.upstreamModel
			}
			delete(t.orphans, id)
		}
	}
	if strings.EqualFold(strings.TrimSpace(request.upstreamFormat), strings.TrimSpace(request.clientProtocol)) {
		for _, object := range objects {
			request.usage.merge(parseUsageObject(request.upstreamFormat, object))
		}
	}
}

func (t *protocol2UsageTracker) finish(requestID string, resolveModel func(string, string) string) []billing.UsageRecord {
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
		Provider:      request.provider,
		BillingModel:  resolveModel(request.upstreamModel, request.routeModel),
		UpstreamModel: request.upstreamModel,
		Generate:      request.generate,
		RequestedAt:   request.startedAt,
		Breakdown:     breakdown,
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

func protocol2RouteKey(format, upstreamModel string, stream bool, request []byte) protocol2Route {
	return protocol2Route{
		format:        strings.ToLower(strings.TrimSpace(format)),
		upstreamModel: strings.ToLower(strings.TrimSpace(upstreamModel)),
		stream:        stream,
		requestHash:   sha256.Sum256(request),
	}
}

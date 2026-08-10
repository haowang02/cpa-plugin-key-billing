package plugin

import (
	"crypto/sha256"
	"strings"
	"sync"
	"time"

	"cpa-key-billing/internal/billing"
)

// usageTracker reassembles one request's authoritative token usage from the
// response hooks, which split correlation and usage between them: raw upstream
// usage arrives before translation without a request ID, while translated
// responses carry the request ID used to bind stable response IDs.
type usageTracker struct {
	mu             sync.Mutex
	requests       map[string]*trackedRequest
	routes         map[routeKey]map[string]struct{}
	responseOwners map[string]string
	orphans        map[string]*pendingObservation
}

type trackedRequest struct {
	clientFormat   string
	upstreamFormat string
	upstreamModel  string
	routeModel     string
	generate       bool
	startedAt      time.Time
	routes         map[routeKey]struct{}
	usage          upstreamUsage
}

type routeKey struct {
	format        string
	upstreamModel string
	stream        bool
	requestHash   [sha256.Size]byte
}

type pendingObservation struct {
	upstreamModel string
	usage         upstreamUsage
	updatedAt     time.Time
}

func newUsageTracker() *usageTracker {
	return &usageTracker{
		requests:       make(map[string]*trackedRequest),
		routes:         make(map[routeKey]map[string]struct{}),
		responseOwners: make(map[string]string),
		orphans:        make(map[string]*pendingObservation),
	}
}

func (t *usageTracker) begin(requestID, clientFormat, upstreamModel, routeModel string, generate bool, now time.Time) {
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
	t.requests[requestID] = &trackedRequest{
		clientFormat:  strings.TrimSpace(clientFormat),
		upstreamModel: strings.TrimSpace(upstreamModel),
		routeModel:    strings.TrimSpace(routeModel),
		generate:      generate,
		startedAt:     now,
		routes:        make(map[routeKey]struct{}),
	}
}

func (t *usageTracker) addRoute(requestID, format, upstreamModel, routeModel string, stream bool, originalRequest []byte) {
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
	key := newRouteKey(format, upstreamModel, stream, originalRequest)
	request.routes[key] = struct{}{}
	owners := t.routes[key]
	if owners == nil {
		owners = make(map[string]struct{})
		t.routes[key] = owners
	}
	owners[requestID] = struct{}{}
}

func (t *usageTracker) observeUpstream(req ResponseTransformRequest, now time.Time) {
	if len(req.Body) == 0 {
		return
	}
	frames := parseUpstreamResponse(req.FromFormat, req.Body)
	if len(frames) == 0 {
		return
	}
	route := newRouteKey(req.FromFormat, req.Model, req.Stream, req.OriginalRequest)

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
			observation = &pendingObservation{}
			t.orphans[frame.responseID] = observation
		}
		observation.upstreamModel = strings.TrimSpace(req.Model)
		observation.updatedAt = now
		observation.usage.merge(frame.usage)
	}
}

func (t *usageTracker) bindResponse(requestID string, body []byte) {
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
	if strings.EqualFold(strings.TrimSpace(request.upstreamFormat), strings.TrimSpace(request.clientFormat)) {
		for _, object := range objects {
			request.usage.merge(parseUsageObject(request.upstreamFormat, object))
		}
	}
}

// finish returns what the request accumulated and forgets it. A nil result is a
// request this tracker never saw.
//
// A tracked request always yields a record, even one the provider never
// reported usage for: a canceled or failed request has none, and it still has
// to reach the billing log so it is visible rather than merely missing. Its
// Breakdown is then the zero value, which prices at nothing.
func (t *usageTracker) finish(requestID string, resolveModel func(string, string) string) *billing.UsageRecord {
	requestID = strings.TrimSpace(requestID)
	t.mu.Lock()
	defer t.mu.Unlock()
	request := t.requests[requestID]
	if request == nil {
		return nil
	}
	defer t.removeRequestLocked(requestID)
	record := &billing.UsageRecord{
		BillingModel:  resolveModel(request.upstreamModel, request.routeModel),
		UpstreamModel: request.upstreamModel,
		Generate:      request.generate,
		RequestedAt:   request.startedAt,
	}
	if request.usage.hasUsage() {
		record.Breakdown = request.usage.breakdown()
	}
	return record
}

func (t *usageTracker) discard(requestID string) {
	t.mu.Lock()
	t.removeRequestLocked(strings.TrimSpace(requestID))
	t.mu.Unlock()
}

func (t *usageTracker) removeRequestLocked(requestID string) {
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

func (t *usageTracker) sweepLocked(now time.Time) {
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

func newRouteKey(format, upstreamModel string, stream bool, request []byte) routeKey {
	return routeKey{
		format:        strings.ToLower(strings.TrimSpace(format)),
		upstreamModel: strings.ToLower(strings.TrimSpace(upstreamModel)),
		stream:        stream,
		requestHash:   sha256.Sum256(request),
	}
}

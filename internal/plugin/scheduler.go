package plugin

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cpa-key-billing/internal/billing"
)

const maxPoolsPerKey = 256
const maxCredentialsPerPool = 1024
const maxSessionBindings = 65536

type subsetScheduleState struct {
	LastUsed   time.Time
	LastPicked string
	Current    map[string]int64
	Weights    map[string]int64
}

type sessionBindingKey struct{ Scope, Pool, Session string }
type sessionBinding struct {
	Key       sessionBindingKey
	AuthID    string
	ExpiresAt time.Time
}

type subsetScheduler struct {
	mu              sync.Mutex
	strategy        string
	sessionAffinity bool
	sessionTTL      time.Duration
	keys            map[string]map[string]*subsetScheduleState
	sessions        map[sessionBindingKey]*list.Element
	sessionOrder    list.List
}

func (s *subsetScheduler) configure(cfg billing.RoutingConfig, enabled bool) {
	ttl, _ := time.ParseDuration(cfg.SessionAffinityTTL) // Configuration is validated before application.
	s.mu.Lock()
	defer s.mu.Unlock()
	if enabled && s.strategy == cfg.Strategy && s.sessionAffinity == cfg.SessionAffinity && s.sessionTTL == ttl {
		return
	}
	s.strategy, s.sessionAffinity, s.sessionTTL = cfg.Strategy, cfg.SessionAffinity, ttl
	s.keys, s.sessions = nil, nil
	s.sessionOrder.Init()
}

func candidateWeight(candidate SchedulerAuthCandidate) int64 {
	raw := strings.TrimSpace(candidate.Attributes["weight"])
	if raw == "" {
		return 1
	}
	weight, _ := strconv.ParseInt(raw, 10, 64)
	return max(0, weight)
}

func routingPoolKey(provider, model string, routing billing.RouteRule) string {
	// Route permissions isolate both rotation progress and session bindings. A
	// model-only policy change does not alter credential selection for this model.
	policy := routing
	policy.Models, policy.DeniedModels = nil, nil
	raw, _ := json.Marshal(policy)
	sum := sha256.Sum256(raw)
	return strings.ToLower(strings.TrimSpace(provider)) + "\x00" + billing.ModelWithoutThinkingSuffix(model) + "\x00" + hex.EncodeToString(sum[:])
}

func (s *subsetScheduler) pick(scope, pool, session string, candidates []SchedulerAuthCandidate, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	eligible := make([]SchedulerAuthCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		// Zero weights exclude credentials only in weighted mode, as in CPA.
		if s.strategy != "weighted-round-robin" || candidateWeight(candidate) > 0 {
			eligible = append(eligible, candidate)
		}
	}
	if len(eligible) == 0 {
		return ""
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	// Expire idle bindings synchronously, with no background cleanup work.
	for tail := s.sessionOrder.Back(); tail != nil; tail = s.sessionOrder.Back() {
		if now.Before(tail.Value.(*sessionBinding).ExpiresAt) {
			break
		}
		s.removeSession(tail)
	}
	key := sessionBindingKey{scope, pool, session}
	useSession := s.sessionAffinity && session != "" && len(session) <= 512
	if useSession {
		if element := s.sessions[key]; element != nil {
			binding := element.Value.(*sessionBinding)
			for _, candidate := range eligible {
				if now.Before(binding.ExpiresAt) && candidate.ID == binding.AuthID {
					binding.ExpiresAt = now.Add(s.sessionTTL)
					s.sessionOrder.MoveToFront(element)
					if state := s.keys[scope][pool]; state != nil {
						state.LastUsed = now
					}
					return binding.AuthID
				}
			}
			// Host eligibility, retries, or route permissions removed this credential.
			// Replace the binding only with a candidate allowed for this request.
			s.removeSession(element)
		}
	}
	// Existing usable bindings outrank priority. Priority applies to cold
	// selections and failover, before the configured fallback strategy runs.
	highestPriority := eligible[0].Priority
	for _, candidate := range eligible {
		if candidate.Priority > highestPriority {
			highestPriority = candidate.Priority
		}
	}
	highest := eligible[:0]
	for _, candidate := range eligible {
		if candidate.Priority == highestPriority {
			highest = append(highest, candidate)
		}
	}
	if s.keys == nil {
		s.keys = make(map[string]map[string]*subsetScheduleState)
	}
	pools := s.keys[scope]
	if pools == nil {
		pools = make(map[string]*subsetScheduleState)
		s.keys[scope] = pools
	}
	state := pools[pool]
	if state == nil {
		if len(pools) >= maxPoolsPerKey {
			var oldest string
			for key, candidate := range pools {
				if oldest == "" || candidate.LastUsed.Before(pools[oldest].LastUsed) {
					oldest = key
				}
			}
			delete(pools, oldest)
		}
		state = &subsetScheduleState{}
		pools[pool] = state
	}
	state.LastUsed = now
	selected := state.pick(s.strategy, highest)
	if useSession {
		if s.sessions == nil {
			s.sessions = make(map[sessionBindingKey]*list.Element)
		}
		if len(s.sessions) >= maxSessionBindings {
			s.removeSession(s.sessionOrder.Back())
		}
		s.sessions[key] = s.sessionOrder.PushFront(&sessionBinding{key, selected, now.Add(s.sessionTTL)})
	}
	return selected
}

func (state *subsetScheduleState) pick(strategy string, candidates []SchedulerAuthCandidate) string {
	if strategy == "fill-first" {
		return candidates[0].ID
	}
	if strategy != "weighted-round-robin" {
		// Resume after the last credential identity even if the candidate set
		// shrinks on retry or during cooldown; a slice index would skip accounts.
		index := sort.Search(len(candidates), func(i int) bool { return candidates[i].ID > state.LastPicked })
		if index == len(candidates) {
			index = 0
		}
		state.LastPicked = candidates[index].ID
		return state.LastPicked
	}
	if state.Current == nil {
		state.Current = make(map[string]int64)
	}
	if state.Weights == nil {
		state.Weights = make(map[string]int64)
	}
	weights := make(map[string]int64, len(candidates))
	changed := false
	for _, candidate := range candidates {
		weight := candidateWeight(candidate)
		weights[candidate.ID] = weight
		if previous, ok := state.Weights[candidate.ID]; ok && previous != weight {
			changed = true
		}
		state.Weights[candidate.ID] = weight
	}
	if changed {
		clear(state.Current)
	}
	// Missing candidates are often temporary. Retain their credits, bounding
	// both maps so weight changes cannot leave an unbounded history behind.
	if len(state.Current) > maxCredentialsPerPool || len(state.Weights) > maxCredentialsPerPool {
		for id := range state.Current {
			if _, ok := weights[id]; !ok {
				delete(state.Current, id)
			}
		}
		for id := range state.Weights {
			if _, ok := weights[id]; !ok {
				delete(state.Weights, id)
			}
		}
	}
	selected := ""
	highest, total := int64(0), int64(0)
	for _, candidate := range candidates {
		weight := weights[candidate.ID]
		total = schedulingScore(total, weight)
		state.Current[candidate.ID] = schedulingScore(state.Current[candidate.ID], weight)
		score := state.Current[candidate.ID]
		if selected == "" || score > highest {
			selected, highest = candidate.ID, score
		}
	}
	state.Current[selected] = schedulingScore(state.Current[selected], -total)
	return selected
}

func schedulingScore(value, delta int64) int64 {
	if delta > 0 && value > math.MaxInt64-delta {
		return math.MaxInt64
	}
	if delta < 0 && value < math.MinInt64-delta {
		return math.MinInt64
	}
	return value + delta
}

func (s *subsetScheduler) removeSession(element *list.Element) {
	delete(s.sessions, element.Value.(*sessionBinding).Key)
	s.sessionOrder.Remove(element)
}

func (s *subsetScheduler) prune(scopes map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for scope := range s.keys {
		if _, ok := scopes[scope]; !ok {
			delete(s.keys, scope)
		}
	}
	for key, element := range s.sessions {
		if _, ok := scopes[key.Scope]; !ok {
			s.removeSession(element)
		}
	}
}

func (a *App) pickCredential(raw []byte) ([]byte, error) {
	var req SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("Parse upstream credential scheduling parameters: %w", err)
	}
	if !a.store.Enabled() {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	if metadataString(req.Options.Metadata, MetadataSource) == SourcePluginHostModelCallback {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	scope := metadataString(req.Options.Metadata, MetadataCallerScope)
	if scope == "" {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	routing := a.store.Routing(scope)
	if !routing.RestrictsCredentials() {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	// CPA enriches scheduler metadata before credential selection. Do not infer a
	// session from request IDs or from later, asynchronous usage records.
	session := ""
	for _, name := range []string{"canonical_session_id", "execution_session_id", "derived_session_id"} {
		if value, ok := req.Options.Metadata[name].(string); ok && strings.TrimSpace(value) != "" {
			// Keep different identity sources separate even when their raw values match.
			session = name + "\x00" + strings.TrimSpace(value)
			break
		}
	}
	allowed := func(directory credentialDirectory) []SchedulerAuthCandidate {
		return slices.DeleteFunc(slices.Clone(req.Candidates), func(candidate SchedulerAuthCandidate) bool {
			return !directory.allows(candidate.ID, routing)
		})
	}
	ids := make([]string, len(req.Candidates))
	for i, candidate := range req.Candidates {
		ids[i] = candidate.ID
	}
	directory, err := a.routingDirectory(func(directory credentialDirectory) bool { return len(allowed(directory)) > 0 }, ids...)
	if err != nil {
		return nil, fmt.Errorf("Read upstream credentials: %w", err)
	}
	pool := routingPoolKey(req.Provider, req.Model, routing)
	if id := a.scheduler.pick(scope, pool, session, allowed(directory), a.store.Now()); id != "" {
		return OKEnvelope(SchedulerPickResponse{AuthID: id, Handled: true})
	}
	shortfall := routeUnavailable
	if !directory.serves(routing, req.Model) {
		shortfall = routeNoCredential
	}
	return ErrorEnvelope("no_routed_credential", shortfall.message(req.Model), shortfall.status), nil
}

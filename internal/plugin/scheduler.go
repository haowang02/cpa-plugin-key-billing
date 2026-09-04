package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"cpa-key-billing/internal/billing"
)

const maxPoolsPerKey = 256
const maxCredentialsPerPool = 1024
const noRoutedCredentialMessage = "当前没有符合路由规则且可用的上游凭证"

type subsetScheduleState struct {
	Current map[string]int64
	Weights map[string]int64
}
type subsetScheduler struct {
	mu   sync.Mutex
	keys map[string]map[string]*subsetScheduleState
}

func candidateWeight(candidate SchedulerAuthCandidate) int64 {
	raw := strings.TrimSpace(candidate.Attributes["weight"])
	if raw == "" {
		return 1
	}
	weight, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || weight <= 0 {
		return 0
	}
	return weight
}

func routingPoolKey(model string, decision billing.RoutingDecision) string {
	policy := struct {
		IDs       []string                             `json:"ids"`
		Providers []billing.CredentialProviderSelector `json:"providers"`
	}{decision.CredentialIDs, decision.CredentialProviders}
	raw, _ := json.Marshal(policy)
	sum := sha256.Sum256(raw)
	return strings.ToLower(strings.TrimSpace(model)) + "\x00" + hex.EncodeToString(sum[:])
}

func (s *subsetScheduler) pick(scope, pool string, candidates []SchedulerAuthCandidate) string {
	positive := make([]SchedulerAuthCandidate, 0, len(candidates))
	weights := make(map[string]int64, len(candidates))
	for _, candidate := range candidates {
		weight := candidateWeight(candidate)
		if weight > 0 {
			positive = append(positive, candidate)
			weights[candidate.ID] = weight
		}
	}
	if len(positive) == 0 {
		return ""
	}
	if len(positive) == 1 {
		return positive[0].ID
	}
	sort.Slice(positive, func(i, j int) bool { return positive[i].ID < positive[j].ID })
	s.mu.Lock()
	defer s.mu.Unlock()
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
			pools = make(map[string]*subsetScheduleState)
			s.keys[scope] = pools
		}
		state = &subsetScheduleState{Current: make(map[string]int64), Weights: make(map[string]int64)}
		pools[pool] = state
	}
	weightsChanged := false
	for _, candidate := range positive {
		weight := weights[candidate.ID]
		if old, ok := state.Weights[candidate.ID]; ok && old != weight {
			weightsChanged = true
		}
		state.Weights[candidate.ID] = weight
	}
	if weightsChanged {
		state.Current = make(map[string]int64)
	}
	if len(state.Current) > maxCredentialsPerPool {
		current := make(map[string]int64, len(positive))
		keptWeights := make(map[string]int64, len(positive))
		for _, c := range positive {
			current[c.ID] = state.Current[c.ID]
			keptWeights[c.ID] = state.Weights[c.ID]
		}
		state.Current = current
		state.Weights = keptWeights
	}
	var selected string
	var highest int64
	total := int64(0)
	for _, candidate := range positive {
		weight := state.Weights[candidate.ID]
		total += weight
		state.Current[candidate.ID] += weight
		score := state.Current[candidate.ID]
		if selected == "" || score > highest || (score == highest && candidate.ID < selected) {
			selected = candidate.ID
			highest = score
		}
	}
	state.Current[selected] -= total
	return selected
}

func (s *subsetScheduler) prune(scopes map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for scope := range s.keys {
		if _, ok := scopes[scope]; !ok {
			delete(s.keys, scope)
		}
	}
}

func candidateAllowed(candidate SchedulerAuthCandidate, decision billing.RoutingDecision) bool {
	return routingAllowsCredential(candidate.ID, credentialSourceFromCandidate(candidate), candidate.Provider, decision)
}

func (a *App) pickCredential(raw []byte) ([]byte, error) {
	var req SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("解析上游凭证调度参数：%w", err)
	}
	if a == nil || a.store == nil || !a.store.Enabled() {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	if metadataString(req.Options.Metadata, MetadataSource) == SourcePluginHostModelCallback {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	a.observeCandidates(req.Candidates)
	scope := metadataString(req.Options.Metadata, MetadataCallerScope)
	if scope == "" {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	requestedModel := metadataString(req.Options.Metadata, MetadataRequestedModel)
	if requestedModel == "" {
		requestedModel = req.Model
	}
	decision := a.store.ResolveRouting(scope, req.Model, requestedModel)
	if decision.ConfigurationError != "" {
		return ErrorEnvelope("routing_configuration_error", decision.ConfigurationError, http.StatusServiceUnavailable), nil
	}
	if !decision.RestrictsCredentials() {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	allowed := make([]SchedulerAuthCandidate, 0, len(req.Candidates))
	for _, candidate := range req.Candidates {
		if candidateAllowed(candidate, decision) {
			allowed = append(allowed, candidate)
		}
	}
	if len(allowed) == 0 {
		return ErrorEnvelope("no_routed_credential", noRoutedCredentialMessage, http.StatusServiceUnavailable), nil
	}
	if len(allowed) == len(req.Candidates) {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	id := a.scheduler.pick(scope, routingPoolKey(decision.Model, decision), allowed)
	if id == "" {
		return ErrorEnvelope("no_routed_credential", noRoutedCredentialMessage, http.StatusServiceUnavailable), nil
	}
	return OKEnvelope(SchedulerPickResponse{AuthID: id, Handled: true})
}

package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
)

const (
	RouteBindingRoute              = "route"
	RouteBindingModel              = "model"
	RouteBindingCredential         = "credential"
	RouteBindingCredentialProvider = "credential_provider"

	CredentialSourceAuthFiles   = "auth-files"
	CredentialSourceAIProviders = "ai-providers"

	maxRouteNameBytes  = 128
	maxRouteEntries    = 1024
	maxRouteValueBytes = 512
)

type CredentialProviderSelector struct {
	Source   string `json:"source"`
	Provider string `json:"provider"`
}

type RouteRule struct {
	Models              []string                     `json:"models"`
	CredentialIDs       []string                     `json:"credential_ids"`
	CredentialProviders []CredentialProviderSelector `json:"credential_providers"`
}

type Route struct {
	ID   string    `json:"id"`
	Name string    `json:"name"`
	Rule RouteRule `json:"rule"`
}

type RouteBinding struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type RoutePatch struct {
	ID   string     `json:"id"`
	Name *string    `json:"name,omitempty"`
	Rule *RouteRule `json:"rule,omitempty"`
}

type RoutingDecision struct {
	Model                string
	ModelAllowed         bool
	ModelRestricted      bool
	ModelScope           []string
	CredentialRestricted bool
	CredentialIDs        []string
	CredentialProviders  []CredentialProviderSelector
	ConfigurationError   string
}

type RouteDeleteResult struct {
	Deleted               string `json:"deleted"`
	AffectedKeys          int    `json:"affected_keys"`
	FullyUnrestrictedKeys int    `json:"fully_unrestricted_keys"`
}

type RouteView struct {
	Route
	BoundKeyCount         int `json:"bound_key_count"`
	FullyUnrestrictedKeys int `json:"fully_unrestricted_keys"`
}

func CredentialFingerprint(rawID string) string {
	h := sha256.New()
	_, _ = h.Write([]byte("cpa-key-billing:credential:v1\x00"))
	_, _ = h.Write([]byte(rawID))
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func validCredentialFingerprint(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil
}

func normalizeCredentialProviderSelector(item CredentialProviderSelector) (CredentialProviderSelector, error) {
	item.Source = strings.ToLower(strings.TrimSpace(item.Source))
	item.Provider = strings.ToLower(strings.TrimSpace(item.Provider))
	if item.Source != CredentialSourceAuthFiles && item.Source != CredentialSourceAIProviders {
		return CredentialProviderSelector{}, invalidf("上游凭证来源必须是 auth-files 或 ai-providers")
	}
	if item.Provider == "" || len(item.Provider) > maxRouteValueBytes || strings.ContainsAny(item.Provider, "*[]\x00") {
		return CredentialProviderSelector{}, invalidf("Provider 无效")
	}
	return item, nil
}

func credentialProviderBindingValue(item CredentialProviderSelector) string {
	return item.Source + "\x00" + item.Provider
}

func parseCredentialProviderBinding(value string) (CredentialProviderSelector, error) {
	source, provider, ok := strings.Cut(value, "\x00")
	if !ok || strings.Contains(provider, "\x00") {
		return CredentialProviderSelector{}, invalidf("API Key 的凭证类别引用无效")
	}
	item, err := normalizeCredentialProviderSelector(CredentialProviderSelector{Source: source, Provider: provider})
	if err != nil {
		return CredentialProviderSelector{}, invalidf("API Key 的凭证类别引用无效")
	}
	return item, nil
}

func NormalizeRouteRule(rule RouteRule) (RouteRule, error) {
	var err error
	rule.Models, err = normalizeRouteStrings(rule.Models, false)
	if err != nil {
		return RouteRule{}, err
	}
	rule.CredentialIDs, err = normalizeRouteStrings(rule.CredentialIDs, true)
	if err != nil {
		return RouteRule{}, err
	}
	if len(rule.CredentialProviders) > maxRouteEntries {
		return RouteRule{}, invalidf("每条路由规则最多包含 %d 个 Provider", maxRouteEntries)
	}
	providers := make([]CredentialProviderSelector, 0, len(rule.CredentialProviders))
	seen := make(map[string]struct{}, len(rule.CredentialProviders))
	for _, item := range rule.CredentialProviders {
		item, err = normalizeCredentialProviderSelector(item)
		if err != nil {
			return RouteRule{}, err
		}
		key := credentialProviderBindingValue(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		providers = append(providers, item)
	}
	rule.CredentialProviders = providers
	if rule.Models == nil {
		rule.Models = []string{}
	}
	if rule.CredentialIDs == nil {
		rule.CredentialIDs = []string{}
	}
	if rule.CredentialProviders == nil {
		rule.CredentialProviders = []CredentialProviderSelector{}
	}
	return rule, nil
}

func normalizeRouteStrings(values []string, fingerprints bool) ([]string, error) {
	if len(values) > maxRouteEntries {
		return nil, invalidf("每条路由规则的单项选择最多为 %d 个", maxRouteEntries)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > maxRouteValueBytes {
			return nil, invalidf("路由规则值无效")
		}
		if fingerprints && !validCredentialFingerprint(value) {
			return nil, invalidf("上游凭证引用无效")
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func NormalizeRouteBindings(bindings []RouteBinding) ([]RouteBinding, error) {
	if len(bindings) > maxRouteEntries {
		return nil, invalidf("每个 API Key 最多绑定 %d 项", maxRouteEntries)
	}
	result := make([]RouteBinding, 0, len(bindings))
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		binding.Kind = strings.ToLower(strings.TrimSpace(binding.Kind))
		binding.Value = strings.TrimSpace(binding.Value)
		if binding.Value == "" || len(binding.Value) > maxRouteValueBytes {
			return nil, invalidf("API Key 的路由绑定无效")
		}
		switch binding.Kind {
		case RouteBindingRoute, RouteBindingModel:
		case RouteBindingCredential:
			if !validCredentialFingerprint(binding.Value) {
				return nil, invalidf("API Key 的上游凭证引用无效")
			}
		case RouteBindingCredentialProvider:
			item, err := parseCredentialProviderBinding(binding.Value)
			if err != nil {
				return nil, err
			}
			binding.Value = credentialProviderBindingValue(item)
		default:
			return nil, invalidf("未知的路由绑定类型 %q", binding.Kind)
		}
		key := binding.Kind + "\x00" + strings.ToLower(binding.Value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, binding)
	}
	return result, nil
}

func (s *State) findRouteIndex(id string) int {
	id = strings.TrimSpace(id)
	return slices.IndexFunc(s.Routes, func(route Route) bool { return route.ID == id })
}

func (s *State) findRoute(id string) (Route, bool) {
	i := s.findRouteIndex(id)
	if i < 0 {
		return Route{}, false
	}
	return s.Routes[i], true
}

func cloneRoute(route Route) Route {
	route.Rule.Models = slices.Clone(route.Rule.Models)
	route.Rule.CredentialIDs = slices.Clone(route.Rule.CredentialIDs)
	route.Rule.CredentialProviders = slices.Clone(route.Rule.CredentialProviders)
	return route
}

func (s *Store) Route(id string) (Route, bool) {
	var result Route
	found := false
	s.read(func(state *State) {
		if route, ok := state.findRoute(id); ok {
			result, found = cloneRoute(route), true
		}
	})
	return result, found
}

func (s *Store) RouteViews() []RouteView {
	views := []RouteView{}
	s.read(func(state *State) {
		for _, route := range state.Routes {
			view := RouteView{Route: cloneRoute(route)}
			for _, key := range state.Keys {
				if key == nil || !key.DeletedAt.IsZero() || !slices.Contains(key.RouteBindings, RouteBinding{Kind: RouteBindingRoute, Value: route.ID}) {
					continue
				}
				view.BoundKeyCount++
				copyKey := *key
				copyKey.RouteBindings = slices.DeleteFunc(slices.Clone(key.RouteBindings), func(b RouteBinding) bool { return b.Kind == RouteBindingRoute && b.Value == route.ID })
				if !routingRestricted(state, &copyKey) {
					view.FullyUnrestrictedKeys++
				}
			}
			views = append(views, view)
		}
	})
	return views
}

func NormalizeRoute(route Route) (Route, error) {
	route.ID = strings.TrimSpace(route.ID)
	if route.ID == "" {
		return Route{}, invalidf("路由规则 ID 不能为空")
	}
	route.Name = strings.TrimSpace(route.Name)
	if route.Name == "" {
		return Route{}, invalidf("请输入规则名称")
	}
	if len([]byte(route.Name)) > maxRouteNameBytes {
		return Route{}, invalidf("规则名称不能超过 %d 字节", maxRouteNameBytes)
	}
	rule, err := NormalizeRouteRule(route.Rule)
	if err != nil {
		return Route{}, err
	}
	route.Rule = rule
	return route, nil
}

func (s *Store) CreateRoute(route Route, scopes []string) (Route, error) {
	scopes = normalizeScopes(scopes)
	return routingUpdateResult(s, func(state *State) (Route, Changes, error) {
		for _, scope := range scopes {
			if state.liveKey(scope) == nil {
				return Route{}, Changes{}, notFoundf("API Key %q 不存在，请先同步 Key 列表", scope)
			}
		}
		if strings.TrimSpace(route.ID) == "" {
			route.ID = freeID(route.Name, "route", func(id string) bool {
				_, exists := state.findRoute(id)
				return exists
			})
		}
		validated, err := NormalizeRoute(route)
		if err != nil {
			return Route{}, Changes{}, err
		}
		route = validated
		if _, ok := state.findRoute(route.ID); ok {
			return Route{}, Changes{}, conflictf("路由规则 %q 已存在", route.ID)
		}
		state.Routes = append(state.Routes, route)
		binding := RouteBinding{Kind: RouteBindingRoute, Value: route.ID}
		for _, scope := range scopes {
			state.Keys[scope].RouteBindings = append(state.Keys[scope].RouteBindings, binding)
		}
		return cloneRoute(route), Changes{Routes: true, Keys: slices.Clone(scopes)}, nil
	})
}

func (s *Store) UpdateRoute(patch RoutePatch, scopes *[]string) (Route, error) {
	patch.ID = strings.TrimSpace(patch.ID)
	if patch.ID == "" {
		return Route{}, invalidf("路由规则 ID 不能为空")
	}
	return routingUpdateResult(s, func(state *State) (Route, Changes, error) {
		changed := []string{}
		i := state.findRouteIndex(patch.ID)
		if i < 0 {
			return Route{}, Changes{}, notFoundf("路由规则 %q 不存在", patch.ID)
		}
		updated := cloneRoute(state.Routes[i])
		if patch.Name != nil {
			updated.Name = *patch.Name
		}
		if patch.Rule != nil {
			updated.Rule = *patch.Rule
		}
		validated, err := NormalizeRoute(updated)
		if err != nil {
			return Route{}, Changes{}, err
		}
		var selected map[string]struct{}
		if scopes != nil {
			normalized := normalizeScopes(*scopes)
			selected = make(map[string]struct{}, len(normalized))
			for _, scope := range normalized {
				if state.liveKey(scope) == nil {
					return Route{}, Changes{}, notFoundf("API Key %q 不存在，请先同步 Key 列表", scope)
				}
				selected[scope] = struct{}{}
			}
		}
		updated = validated
		state.Routes[i] = updated
		binding := RouteBinding{Kind: RouteBindingRoute, Value: patch.ID}
		for scope, key := range state.Keys {
			if key == nil || !key.DeletedAt.IsZero() {
				continue
			}
			hasBinding := slices.Contains(key.RouteBindings, binding)
			if scopes == nil {
				continue
			}
			_, shouldBind := selected[scope]
			if shouldBind && !hasBinding {
				key.RouteBindings = append(key.RouteBindings, binding)
				changed = append(changed, scope)
			} else if !shouldBind && hasBinding {
				key.RouteBindings = slices.DeleteFunc(key.RouteBindings, func(item RouteBinding) bool { return item == binding })
				changed = append(changed, scope)
			}
		}
		return cloneRoute(updated), Changes{Routes: true, Keys: changed}, nil
	})
}

func (s *Store) SetKeyRoutes(scope string, bindings []RouteBinding) error {
	scope = normalizeScope(scope)
	if scope == "" {
		return invalidf("API Key 标识不能为空")
	}
	bindings, err := NormalizeRouteBindings(bindings)
	if err != nil {
		return err
	}
	_, err = routingUpdateResult(s, func(state *State) (struct{}, Changes, error) {
		key := state.liveKey(scope)
		if key == nil {
			return struct{}{}, Changes{}, notFoundf("API Key %q 不存在，请先同步 Key 列表", scope)
		}
		for _, binding := range bindings {
			if binding.Kind == RouteBindingRoute {
				if _, ok := state.findRoute(binding.Value); !ok {
					return struct{}{}, Changes{}, notFoundf("路由规则 %q 不存在", binding.Value)
				}
			}
		}
		key.RouteBindings = slices.Clone(bindings)
		return struct{}{}, Changes{Keys: []string{scope}}, nil
	})
	return err
}

func (s *Store) DeleteRoute(id string) (RouteDeleteResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return RouteDeleteResult{}, invalidf("路由规则 ID 不能为空")
	}
	return routingUpdateResult(s, func(state *State) (RouteDeleteResult, Changes, error) {
		changed := []string{}
		i := state.findRouteIndex(id)
		if i < 0 {
			return RouteDeleteResult{}, Changes{}, notFoundf("路由规则 %q 不存在", id)
		}
		out := RouteDeleteResult{Deleted: id}
		for scope, key := range state.Keys {
			if key == nil {
				continue
			}
			live := key.DeletedAt.IsZero()
			next := slices.DeleteFunc(slices.Clone(key.RouteBindings), func(b RouteBinding) bool { return b.Kind == RouteBindingRoute && b.Value == id })
			if len(next) == len(key.RouteBindings) {
				continue
			}
			key.RouteBindings = next
			changed = append(changed, scope)
			if !live {
				continue
			}
			out.AffectedKeys++
			if !routingRestricted(state, key) {
				out.FullyUnrestrictedKeys++
			}
		}
		state.Routes = slices.Delete(state.Routes, i, i+1)
		return out, Changes{Routes: true, Keys: changed}, nil
	})
}

func cloneStateForRouting(state *State) *State {
	next := *state
	next.Routes = make([]Route, 0, len(state.Routes))
	for _, route := range state.Routes {
		next.Routes = append(next.Routes, cloneRoute(route))
	}
	next.Keys = make(map[string]*KeyState, len(state.Keys))
	for scope, key := range state.Keys {
		if key == nil {
			next.Keys[scope] = nil
			continue
		}
		copyKey := *key
		copyKey.RouteBindings = slices.Clone(key.RouteBindings)
		next.Keys[scope] = &copyKey
	}
	return &next
}

func routingUpdateResult[T any](s *Store, fn func(*State) (T, Changes, error)) (value T, err error) {
	var written bool
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		next := cloneStateForRouting(s.state)
		var current Changes
		value, current, err = fn(next)
		if err != nil {
			return
		}
		changes := s.dirty.merge(current)
		if s.repo == nil {
			s.state = next
			s.dirty = changes
			return
		}
		written = true
		if err = s.repo.Save(next, changes); err != nil {
			return
		}
		s.state = next
		s.dirty = Changes{}
	}()
	if err != nil {
		if written {
			s.recordWriteError(err)
		}
		return value, err
	}
	if written {
		s.recordWriteSuccess()
	}
	return value, nil
}

func routingRestricted(state *State, key *KeyState) bool {
	decision := resolveRoutingState(state, key, "")
	return decision.ConfigurationError != "" || decision.ModelRestricted || decision.CredentialRestricted
}

func (s *Store) ResolveRouting(scope, upstreamModel, routeModel string) RoutingDecision {
	decision := RoutingDecision{ModelAllowed: true}
	s.read(func(state *State) {
		model := state.ResolveBillingModel(upstreamModel, routeModel)
		decision = resolveRoutingState(state, state.Keys[normalizeScope(scope)], model)
	})
	return decision
}

func (s *Store) KeyDescription(scope string) string {
	result := ""
	s.read(func(state *State) {
		result = keyDescription(state.Keys[normalizeScope(scope)])
	})
	return result
}

func resolveRoutingState(state *State, key *KeyState, model string) RoutingDecision {
	model = strings.TrimSpace(model)
	d := RoutingDecision{Model: model, ModelAllowed: true, ModelScope: []string{}, CredentialIDs: []string{}, CredentialProviders: []CredentialProviderSelector{}}
	if key == nil || len(key.RouteBindings) == 0 {
		return d
	}
	modelSet := map[string]string{}
	ids := map[string]string{}
	providers := map[string]CredentialProviderSelector{}
	for _, binding := range key.RouteBindings {
		var rule RouteRule
		switch binding.Kind {
		case RouteBindingRoute:
			route, ok := state.findRoute(binding.Value)
			if !ok {
				d.ConfigurationError = fmt.Sprintf("路由规则 %q 已不存在", binding.Value)
				d.ModelAllowed = false
				return d
			}
			rule = route.Rule
		case RouteBindingModel:
			rule.Models = []string{binding.Value}
		case RouteBindingCredential:
			rule.CredentialIDs = []string{binding.Value}
		case RouteBindingCredentialProvider:
			provider, err := parseCredentialProviderBinding(binding.Value)
			if err != nil {
				d.ConfigurationError = "API Key 的凭证类别绑定无效"
				d.ModelAllowed = false
				return d
			}
			rule.CredentialProviders = []CredentialProviderSelector{provider}
		default:
			d.ConfigurationError = "API Key 的路由绑定无效"
			d.ModelAllowed = false
			return d
		}
		for _, allowed := range rule.Models {
			modelSet[strings.ToLower(allowed)] = allowed
		}
		applicable := model == "" || len(rule.Models) == 0 || slices.ContainsFunc(rule.Models, func(allowed string) bool {
			return strings.EqualFold(allowed, model)
		})
		if !applicable {
			continue
		}
		for _, id := range rule.CredentialIDs {
			ids[strings.ToLower(id)] = id
		}
		for _, provider := range rule.CredentialProviders {
			providers[credentialProviderBindingValue(provider)] = provider
		}
	}
	if len(modelSet) > 0 {
		d.ModelRestricted = true
		for _, display := range modelSet {
			d.ModelScope = append(d.ModelScope, display)
		}
		sort.Slice(d.ModelScope, func(i, j int) bool { return strings.ToLower(d.ModelScope[i]) < strings.ToLower(d.ModelScope[j]) })
		if model != "" {
			_, d.ModelAllowed = modelSet[strings.ToLower(model)]
		}
	}
	if len(ids) > 0 || len(providers) > 0 {
		d.CredentialRestricted = true
		for _, id := range ids {
			d.CredentialIDs = append(d.CredentialIDs, id)
		}
		for _, provider := range providers {
			d.CredentialProviders = append(d.CredentialProviders, provider)
		}
		sort.Strings(d.CredentialIDs)
		sort.Slice(d.CredentialProviders, func(i, j int) bool {
			a, b := d.CredentialProviders[i], d.CredentialProviders[j]
			if a.Source != b.Source {
				return a.Source < b.Source
			}
			return a.Provider < b.Provider
		})
	}
	return d
}

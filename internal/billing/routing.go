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

type RouteBindings struct {
	RouteIDs            []string                     `json:"route_ids"`
	Models              []string                     `json:"models"`
	CredentialIDs       []string                     `json:"credential_ids"`
	CredentialProviders []CredentialProviderSelector `json:"credential_providers"`
}

type Route struct {
	ID   string    `json:"id"`
	Name string    `json:"name"`
	Rule RouteRule `json:"rule"`
}

type RoutePatch struct {
	ID   string     `json:"id"`
	Name *string    `json:"name,omitempty"`
	Rule *RouteRule `json:"rule,omitempty"`
}

type RoutingDecision struct {
	Model               string
	ModelScope          []string
	CredentialIDs       []string
	CredentialProviders []CredentialProviderSelector
	ConfigurationError  string
}

func (d RoutingDecision) RestrictsModels() bool {
	return len(d.ModelScope) > 0
}

func (d RoutingDecision) AllowsModel() bool {
	if d.ConfigurationError != "" {
		return false
	}
	if !d.RestrictsModels() || d.Model == "" {
		return true
	}
	return slices.ContainsFunc(d.ModelScope, func(model string) bool {
		return strings.EqualFold(model, d.Model)
	})
}

func (d RoutingDecision) RestrictsCredentials() bool {
	return len(d.CredentialIDs) > 0 || len(d.CredentialProviders) > 0
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

func NormalizeRouteRule(rule RouteRule) (RouteRule, error) {
	var err error
	rule.Models, err = normalizeRouteStrings(rule.Models)
	if err != nil {
		return RouteRule{}, err
	}
	rule.CredentialIDs, err = normalizeCredentialIDs(rule.CredentialIDs)
	if err != nil {
		return RouteRule{}, err
	}
	rule.CredentialProviders, err = normalizeCredentialProviders(rule.CredentialProviders)
	if err != nil {
		return RouteRule{}, err
	}
	return rule, nil
}

func normalizeCredentialProviders(values []CredentialProviderSelector) ([]CredentialProviderSelector, error) {
	if len(values) > maxRouteEntries {
		return nil, invalidf("每条路由规则最多包含 %d 个 Provider", maxRouteEntries)
	}
	result := make([]CredentialProviderSelector, 0, len(values))
	seen := make(map[CredentialProviderSelector]struct{}, len(values))
	for _, item := range values {
		item, err := normalizeCredentialProviderSelector(item)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result, nil
}

func normalizeRouteStrings(values []string) ([]string, error) {
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
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func normalizeCredentialIDs(values []string) ([]string, error) {
	values, err := normalizeRouteStrings(values)
	if err != nil {
		return nil, err
	}
	for _, value := range values {
		if !validCredentialFingerprint(value) {
			return nil, invalidf("上游凭证引用无效")
		}
	}
	return values, nil
}

func NormalizeRouteBindings(bindings RouteBindings) (RouteBindings, error) {
	if len(bindings.RouteIDs)+len(bindings.Models)+len(bindings.CredentialIDs)+len(bindings.CredentialProviders) > maxRouteEntries {
		return RouteBindings{}, invalidf("每个 API Key 最多绑定 %d 项", maxRouteEntries)
	}
	var err error
	bindings.RouteIDs, err = normalizeRouteStrings(bindings.RouteIDs)
	if err != nil {
		return RouteBindings{}, err
	}
	bindings.Models, err = normalizeRouteStrings(bindings.Models)
	if err != nil {
		return RouteBindings{}, err
	}
	bindings.CredentialIDs, err = normalizeCredentialIDs(bindings.CredentialIDs)
	if err != nil {
		return RouteBindings{}, err
	}
	bindings.CredentialProviders, err = normalizeCredentialProviders(bindings.CredentialProviders)
	if err != nil {
		return RouteBindings{}, err
	}
	return bindings, nil
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
				if key == nil || !key.DeletedAt.IsZero() || !slices.Contains(key.RouteBindings.RouteIDs, route.ID) {
					continue
				}
				view.BoundKeyCount++
				copyKey := *key
				copyKey.RouteBindings.RouteIDs = slices.DeleteFunc(slices.Clone(key.RouteBindings.RouteIDs), func(id string) bool { return id == route.ID })
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
	var errApply error
	stored := updateResult(s, func(state *State) (Route, Changes) {
		for _, scope := range scopes {
			if state.liveKey(scope) == nil {
				errApply = notFoundf("API Key %q 不存在，请先同步 Key 列表", scope)
				return Route{}, Changes{}
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
			errApply = err
			return Route{}, Changes{}
		}
		route = validated
		if _, ok := state.findRoute(route.ID); ok {
			errApply = conflictf("路由规则 %q 已存在", route.ID)
			return Route{}, Changes{}
		}
		state.Routes = append(state.Routes, route)
		for _, scope := range scopes {
			state.Keys[scope].RouteBindings.RouteIDs = append(state.Keys[scope].RouteBindings.RouteIDs, route.ID)
		}
		return cloneRoute(route), Changes{Routes: true, Keys: scopes}
	})
	return stored, errApply
}

func (s *Store) UpdateRoute(patch RoutePatch, scopes *[]string) (Route, error) {
	patch.ID = strings.TrimSpace(patch.ID)
	if patch.ID == "" {
		return Route{}, invalidf("路由规则 ID 不能为空")
	}
	var errApply error
	stored := updateResult(s, func(state *State) (Route, Changes) {
		var changed []string
		i := state.findRouteIndex(patch.ID)
		if i < 0 {
			errApply = notFoundf("路由规则 %q 不存在", patch.ID)
			return Route{}, Changes{}
		}
		updated := state.Routes[i]
		if patch.Name != nil {
			updated.Name = *patch.Name
		}
		if patch.Rule != nil {
			updated.Rule = *patch.Rule
		}
		validated, err := NormalizeRoute(updated)
		if err != nil {
			errApply = err
			return Route{}, Changes{}
		}
		var selected map[string]struct{}
		if scopes != nil {
			normalized := normalizeScopes(*scopes)
			selected = make(map[string]struct{}, len(normalized))
			for _, scope := range normalized {
				if state.liveKey(scope) == nil {
					errApply = notFoundf("API Key %q 不存在，请先同步 Key 列表", scope)
					return Route{}, Changes{}
				}
				selected[scope] = struct{}{}
			}
		}
		updated = validated
		state.Routes[i] = updated
		if scopes != nil {
			for scope, key := range state.Keys {
				if key == nil || !key.DeletedAt.IsZero() {
					continue
				}
				hasBinding := slices.Contains(key.RouteBindings.RouteIDs, patch.ID)
				_, shouldBind := selected[scope]
				if shouldBind && !hasBinding {
					key.RouteBindings.RouteIDs = append(key.RouteBindings.RouteIDs, patch.ID)
					changed = append(changed, scope)
				} else if !shouldBind && hasBinding {
					key.RouteBindings.RouteIDs = slices.DeleteFunc(key.RouteBindings.RouteIDs, func(id string) bool { return id == patch.ID })
					changed = append(changed, scope)
				}
			}
		}
		return cloneRoute(updated), Changes{Routes: true, Keys: changed}
	})
	return stored, errApply
}

func (s *Store) SetKeyRoutes(scope string, bindings RouteBindings) error {
	scope = normalizeScope(scope)
	if scope == "" {
		return invalidf("API Key 标识不能为空")
	}
	bindings, err := NormalizeRouteBindings(bindings)
	if err != nil {
		return err
	}
	var errApply error
	updateResult(s, func(state *State) (struct{}, Changes) {
		key := state.liveKey(scope)
		if key == nil {
			errApply = notFoundf("API Key %q 不存在，请先同步 Key 列表", scope)
			return struct{}{}, Changes{}
		}
		for _, id := range bindings.RouteIDs {
			if _, ok := state.findRoute(id); !ok {
				errApply = notFoundf("路由规则 %q 不存在", id)
				return struct{}{}, Changes{}
			}
		}
		key.RouteBindings = bindings
		return struct{}{}, Changes{Keys: []string{scope}}
	})
	return errApply
}

func (s *Store) DeleteRoute(id string) (RouteDeleteResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return RouteDeleteResult{}, invalidf("路由规则 ID 不能为空")
	}
	var errApply error
	result := updateResult(s, func(state *State) (RouteDeleteResult, Changes) {
		var changed []string
		i := state.findRouteIndex(id)
		if i < 0 {
			errApply = notFoundf("路由规则 %q 不存在", id)
			return RouteDeleteResult{}, Changes{}
		}
		out := RouteDeleteResult{Deleted: id}
		for scope, key := range state.Keys {
			if key == nil {
				continue
			}
			before := len(key.RouteBindings.RouteIDs)
			key.RouteBindings.RouteIDs = slices.DeleteFunc(key.RouteBindings.RouteIDs, func(routeID string) bool { return routeID == id })
			if len(key.RouteBindings.RouteIDs) == before {
				continue
			}
			changed = append(changed, scope)
			if !key.DeletedAt.IsZero() {
				continue
			}
			out.AffectedKeys++
			if !routingRestricted(state, key) {
				out.FullyUnrestrictedKeys++
			}
		}
		state.Routes = slices.Delete(state.Routes, i, i+1)
		return out, Changes{Routes: true, Keys: changed}
	})
	return result, errApply
}

func routingRestricted(state *State, key *KeyState) bool {
	decision := resolveRoutingState(state, key, "")
	return decision.ConfigurationError != "" || decision.RestrictsModels() || decision.RestrictsCredentials()
}

func (s *Store) ResolveRouting(scope, upstreamModel, routeModel string) RoutingDecision {
	var decision RoutingDecision
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
	d := RoutingDecision{Model: model, ModelScope: []string{}, CredentialIDs: []string{}, CredentialProviders: []CredentialProviderSelector{}}
	if key == nil {
		return d
	}
	modelSet := map[string]string{}
	ids := map[string]string{}
	providers := map[CredentialProviderSelector]struct{}{}
	for _, id := range key.RouteBindings.RouteIDs {
		route, ok := state.findRoute(id)
		if !ok {
			d.ConfigurationError = fmt.Sprintf("路由规则 %q 已不存在", id)
			return d
		}
		rule := route.Rule
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
			providers[provider] = struct{}{}
		}
	}
	for _, allowed := range key.RouteBindings.Models {
		modelSet[strings.ToLower(allowed)] = allowed
	}
	for _, id := range key.RouteBindings.CredentialIDs {
		ids[strings.ToLower(id)] = id
	}
	for _, provider := range key.RouteBindings.CredentialProviders {
		providers[provider] = struct{}{}
	}
	if len(modelSet) > 0 {
		for _, display := range modelSet {
			d.ModelScope = append(d.ModelScope, display)
		}
		sort.Slice(d.ModelScope, func(i, j int) bool { return strings.ToLower(d.ModelScope[i]) < strings.ToLower(d.ModelScope[j]) })
	}
	if len(ids) > 0 || len(providers) > 0 {
		for _, id := range ids {
			d.CredentialIDs = append(d.CredentialIDs, id)
		}
		for provider := range providers {
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

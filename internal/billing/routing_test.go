package billing

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRoutingModelPrecedesCredentialPolicy(t *testing.T) {
	store := newStore(t)
	ref := CredentialFingerprint("auth-a")
	store.ReplaceAll(func(state *State) {
		state.Routes = []Route{SystemAllRoute(), {ID: "codex", Name: "Codex", Rule: RouteRule{Models: []string{"gpt-5.6-sol"}, CredentialProviders: []CredentialProviderSelector{{Source: CredentialSourceAuthFiles, Provider: "codex"}}}}}
		state.Keys["scope-a"] = &KeyState{RouteBindings: []RouteBinding{{Kind: RouteBindingRoute, Value: "codex"}, {Kind: RouteBindingCredential, Value: ref}}}
	})

	denied := store.ResolveRouting("scope-a", "claude-sonnet-4-6", "claude-sonnet-4-6")
	if denied.ModelAllowed || !denied.ModelRestricted {
		t.Fatalf("decision=%+v, want model denial", denied)
	}
	allowed := store.ResolveRouting("scope-a", "gpt-5.6-sol", "gpt-5.6-sol")
	if !allowed.ModelAllowed || !allowed.CredentialRestricted || !slices.Contains(allowed.CredentialIDs, ref) {
		t.Fatalf("decision=%+v", allowed)
	}
	if !slices.ContainsFunc(allowed.CredentialProviders, func(item CredentialProviderSelector) bool {
		return item.Source == CredentialSourceAuthFiles && item.Provider == "codex"
	}) {
		t.Fatalf("providers=%+v", allowed.CredentialProviders)
	}
}

func TestConditionalRoutesUnionModelsAndFilterCredentialsByRequestedModel(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) {
		state.Routes = []Route{
			SystemAllRoute(),
			{ID: "gpt", Name: "GPT", Rule: RouteRule{Models: []string{"gpt-5.6-sol"}, CredentialProviders: []CredentialProviderSelector{{Source: CredentialSourceAuthFiles, Provider: "codex"}}}},
			{ID: "claude", Name: "Claude", Rule: RouteRule{Models: []string{"claude-sonnet-4-6"}, CredentialProviders: []CredentialProviderSelector{{Source: CredentialSourceAuthFiles, Provider: "claude"}}}},
		}
		state.Keys["scope-a"] = &KeyState{RouteBindings: []RouteBinding{{Kind: RouteBindingRoute, Value: "gpt"}, {Kind: RouteBindingRoute, Value: "claude"}}}
	})

	gpt := store.ResolveRouting("scope-a", "gpt-5.6-sol", "gpt-5.6-sol")
	if !gpt.ModelAllowed || len(gpt.ModelScope) != 2 || len(gpt.CredentialProviders) != 1 || gpt.CredentialProviders[0].Provider != "codex" {
		t.Fatalf("gpt decision=%+v", gpt)
	}
	claude := store.ResolveRouting("scope-a", "claude-sonnet-4-6", "claude-sonnet-4-6")
	if !claude.ModelAllowed || len(claude.CredentialProviders) != 1 || claude.CredentialProviders[0].Provider != "claude" {
		t.Fatalf("claude decision=%+v", claude)
	}
}

func TestVirtualModelAndCredentialBindingsComposeAcrossPhases(t *testing.T) {
	store := newStore(t)
	ref := CredentialFingerprint("auth-a")
	store.ReplaceAll(func(state *State) {
		state.Routes = []Route{SystemAllRoute()}
		state.Keys["scope-a"] = &KeyState{RouteBindings: []RouteBinding{{Kind: RouteBindingModel, Value: "gpt-5.6-sol"}, {Kind: RouteBindingCredential, Value: ref}}}
	})

	allowed := store.ResolveRouting("scope-a", "gpt-5.6-sol", "gpt-5.6-sol")
	if !allowed.ModelAllowed || !allowed.ModelRestricted || !allowed.CredentialRestricted || !slices.Equal(allowed.CredentialIDs, []string{ref}) {
		t.Fatalf("allowed decision=%+v", allowed)
	}
	denied := store.ResolveRouting("scope-a", "claude-sonnet-4-6", "claude-sonnet-4-6")
	if denied.ModelAllowed || !denied.CredentialRestricted {
		t.Fatalf("denied decision=%+v", denied)
	}
}

func TestRouteWritesReplaceOnlyThatRoutesKeyBindings(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) {
		state.Routes = []Route{SystemAllRoute()}
		state.Keys["a"] = &KeyState{}
		state.Keys["b"] = &KeyState{RouteBindings: []RouteBinding{{Kind: RouteBindingModel, Value: "gpt-5.5"}}}
	})
	route, err := store.CreateRoute(Route{Name: "Codex", Rule: RouteRule{Models: []string{"gpt-5.6-sol"}}}, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	binding := RouteBinding{Kind: RouteBindingRoute, Value: route.ID}
	bindings := func(scope string) []RouteBinding {
		for _, key := range store.KeyViews() {
			if key.Scope == scope {
				return key.RouteBindings
			}
		}
		return nil
	}
	if !slices.Contains(bindings("a"), binding) || !slices.Contains(bindings("b"), binding) {
		t.Fatalf("route was not bound at create")
	}
	selected := []string{"a"}
	name := "Codex updated"
	if _, err = store.UpdateRoute(RoutePatch{ID: route.ID, Name: &name}, &selected); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(bindings("a"), binding) {
		t.Fatal("selected key lost route binding")
	}
	bBindings := bindings("b")
	if slices.Contains(bBindings, binding) || !slices.Contains(bBindings, RouteBinding{Kind: RouteBindingModel, Value: "gpt-5.5"}) {
		t.Fatalf("unselected key bindings=%+v", bBindings)
	}
}

func TestCreateRouteRollsBackForUnknownKey(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) { state.Routes = []Route{SystemAllRoute()} })
	if _, err := store.CreateRoute(Route{Name: "Codex"}, []string{"missing"}); err == nil {
		t.Fatal("expected unknown key error")
	}
	if routes := store.RouteViews(); len(routes) != 1 || routes[0].ID != SystemAllRouteID {
		t.Fatalf("routes changed after failed create: %+v", routes)
	}
}

func TestRoutingUsesTheBillingModelIdentity(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) {
		state.Prices = []PriceRule{{Pattern: "chat/fast"}, {Pattern: "chat/slow"}}
		state.Routes = []Route{SystemAllRoute(), {ID: "fast", Name: "Fast", Rule: RouteRule{Models: []string{"CHAT/Fast"}}}}
		state.Keys["scope-a"] = &KeyState{RouteBindings: []RouteBinding{{Kind: RouteBindingRoute, Value: "fast"}}}
	})
	tests := []struct {
		name, upstream, requested string
		want                      bool
	}{
		{name: "exact", upstream: "chat/fast", requested: "chat/fast", want: true},
		{name: "case folded", upstream: "chat/fast", requested: "Chat/Fast", want: true},
		{name: "thinking suffix", upstream: "chat/fast", requested: "chat/fast(high)", want: true},
		{name: "refused suffix", upstream: "chat/slow", requested: "chat/slow(max)", want: false},
		{name: "alias cannot borrow upstream grant", upstream: "chat/fast", requested: "chat/slow", want: false},
		{name: "unpriced route", upstream: "", requested: "chat/fast", want: true},
		{name: "unnamed model", upstream: "", requested: "", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := store.ResolveRouting("scope-a", test.upstream, test.requested)
			if decision.ModelAllowed != test.want {
				t.Fatalf("decision=%+v", decision)
			}
			if !decision.ModelAllowed && strings.Contains(decision.Model, "(") {
				t.Fatalf("refused model kept thinking suffix: %q", decision.Model)
			}
		})
	}
}

func TestRoutingSeparatesConfiguredSuffixedModels(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) {
		state.Prices = []PriceRule{{Pattern: "chat/fast"}, {Pattern: "chat/fast(high)"}}
		state.Routes = []Route{SystemAllRoute()}
		state.Keys["scope-a"] = &KeyState{RouteBindings: []RouteBinding{{Kind: RouteBindingModel, Value: "chat/fast"}}}
	})
	if decision := store.ResolveRouting("scope-a", "chat/fast", "chat/fast(high)"); decision.ModelAllowed {
		t.Fatalf("configured suffixed model inherited base grant: %+v", decision)
	}
	if err := store.SetKeyRoutes("scope-a", []RouteBinding{{Kind: RouteBindingModel, Value: "chat/fast"}, {Kind: RouteBindingModel, Value: "chat/fast(high)"}}); err != nil {
		t.Fatal(err)
	}
	if decision := store.ResolveRouting("scope-a", "chat/fast", "chat/fast(high)"); !decision.ModelAllowed {
		t.Fatalf("explicit suffixed grant was denied: %+v", decision)
	}
}

func TestMissingRouteFailsClosed(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) {
		state.Routes = []Route{SystemAllRoute()}
		state.Keys["scope-a"] = &KeyState{RouteBindings: []RouteBinding{{Kind: RouteBindingRoute, Value: "missing"}}}
	})
	decision := store.ResolveRouting("scope-a", "gpt-5.6", "gpt-5.6")
	if decision.ModelAllowed || decision.ConfigurationError == "" {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestRoutingMutationPublishesOnlyAfterRepositoryCommit(t *testing.T) {
	store, repo := newStoreWithRepository(t)
	store.ReplaceAll(func(state *State) { state.Routes = []Route{SystemAllRoute()}; state.Keys["scope-a"] = &KeyState{} })
	route, err := store.CreateRoute(Route{Name: "Restricted", Rule: RouteRule{Models: []string{"gpt"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo.fail = errors.New("disk full")
	if err := store.SetKeyRoutes("scope-a", []RouteBinding{{Kind: RouteBindingRoute, Value: route.ID}}); err == nil {
		t.Fatal("SetKeyRoutes succeeded despite repository failure")
	}
	store.Read(func(state *State) {
		if len(state.Keys["scope-a"].RouteBindings) != 0 {
			t.Fatalf("uncommitted bindings were published: %+v", state.Keys["scope-a"].RouteBindings)
		}
	})
}

func TestCredentialProviderSourceIsPartOfIdentity(t *testing.T) {
	rule, err := NormalizeRouteRule(RouteRule{CredentialProviders: []CredentialProviderSelector{{Source: "auth-files", Provider: "codex"}, {Source: "ai-providers", Provider: "codex"}}})
	if err != nil || len(rule.CredentialProviders) != 2 {
		t.Fatalf("rule=%+v err=%v", rule, err)
	}
	if _, err := NormalizeRouteRule(RouteRule{CredentialProviders: []CredentialProviderSelector{{Provider: "codex"}}}); KindOf(err) != KindInvalid {
		t.Fatalf("missing source error=%v", err)
	}
}

func TestVirtualCredentialProviderBindingRestrictsOneSource(t *testing.T) {
	store := newStore(t)
	provider := CredentialProviderSelector{Source: CredentialSourceAuthFiles, Provider: "Codex"}
	store.ReplaceAll(func(state *State) {
		state.Routes = []Route{SystemAllRoute()}
		state.Keys["scope-a"] = &KeyState{}
	})
	if err := store.SetKeyRoutes("scope-a", []RouteBinding{{Kind: RouteBindingCredentialProvider, Value: credentialProviderBindingValue(provider)}}); err != nil {
		t.Fatal(err)
	}
	decision := store.ResolveRouting("scope-a", "gpt-5.6-sol", "gpt-5.6-sol")
	if !decision.ModelAllowed || !decision.CredentialRestricted || len(decision.CredentialProviders) != 1 {
		t.Fatalf("decision=%+v", decision)
	}
	allowed := decision.CredentialProviders[0]
	if allowed.Source != CredentialSourceAuthFiles || allowed.Provider != "codex" {
		t.Fatalf("provider=%+v", allowed)
	}
	if _, err := NormalizeRouteBindings([]RouteBinding{{Kind: RouteBindingCredentialProvider, Value: "codex"}}); KindOf(err) != KindInvalid {
		t.Fatalf("invalid provider binding error=%v", err)
	}
}

func TestSystemAllBindingIsExclusiveAndCanonical(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) { state.Routes = []Route{SystemAllRoute()}; state.Keys["scope-a"] = &KeyState{} })
	if err := store.SetKeyRoutes("scope-a", []RouteBinding{{Kind: RouteBindingRoute, Value: SystemAllRouteID}}); err != nil {
		t.Fatal(err)
	}
	store.Read(func(state *State) {
		if len(state.Keys["scope-a"].RouteBindings) != 0 {
			t.Fatalf("bindings=%+v", state.Keys["scope-a"].RouteBindings)
		}
	})
	err := store.SetKeyRoutes("scope-a", []RouteBinding{{Kind: RouteBindingRoute, Value: SystemAllRouteID}, {Kind: RouteBindingModel, Value: "gpt"}})
	if KindOf(err) != KindInvalid {
		t.Fatalf("mixed system route error=%v", err)
	}
	if _, err = NormalizeStoredRoute(Route{ID: "system:other", Name: "Other"}); KindOf(err) != KindInvalid {
		t.Fatalf("stored system route error=%v", err)
	}
}

func TestDeleteRouteCascadesBindingsAndReportsWidening(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) {
		state.Routes = []Route{SystemAllRoute(), {ID: "only", Name: "Only", Rule: RouteRule{Models: []string{"gpt"}, CredentialProviders: []CredentialProviderSelector{{Source: CredentialSourceAIProviders, Provider: "xai"}}}}}
		state.Keys["scope-a"] = &KeyState{RouteBindings: []RouteBinding{{Kind: RouteBindingRoute, Value: "only"}}}
		state.Keys["scope-deleted"] = &KeyState{DeletedAt: time.Now(), RouteBindings: []RouteBinding{{Kind: RouteBindingRoute, Value: "only"}}}
	})
	views := store.RouteViews()
	if len(views) != 2 || views[1].BoundKeyCount != 1 {
		t.Fatalf("views=%+v", views)
	}
	result, err := store.DeleteRoute("only")
	if err != nil || result.AffectedKeys != 1 || result.FullyUnrestrictedKeys != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	store.Read(func(state *State) {
		if len(state.Keys["scope-deleted"].RouteBindings) != 0 {
			t.Fatalf("deleted key retained route binding: %+v", state.Keys["scope-deleted"].RouteBindings)
		}
	})
}

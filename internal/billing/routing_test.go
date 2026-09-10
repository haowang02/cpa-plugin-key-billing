package billing

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRoutesAndDirectBindingsUnionModelsAndCredentialsIndependently(t *testing.T) {
	store := newStore(t)
	refA, refB, refDirect := CredentialFingerprint("auth-a"), CredentialFingerprint("auth-b"), CredentialFingerprint("auth-direct")
	codex := CredentialProviderSelector{Source: CredentialSourceAuthFiles, Provider: "codex"}
	claude := CredentialProviderSelector{Source: CredentialSourceAuthFiles, Provider: "claude"}
	configCodex := CredentialProviderSelector{Source: CredentialSourceAIProviders, Provider: "codex"}
	store.ReplaceAll(func(state *State) {
		state.Routes = []Route{
			{ID: "gpt", Name: "GPT", Rule: RouteRule{Models: []string{"gpt-5.6-sol"}, CredentialIDs: []string{refA}, CredentialProviders: []CredentialProviderSelector{codex}}},
			{ID: "claude", Name: "Claude", Rule: RouteRule{Models: []string{"claude-sonnet-4-6"}, CredentialIDs: []string{refB}, CredentialProviders: []CredentialProviderSelector{claude}}},
		}
		state.Keys["scope-a"] = &KeyState{RouteBindings: RouteBindings{
			RouteIDs: []string{"gpt", "claude"}, RouteRule: RouteRule{Models: []string{"gpt-5.6-luna", "gpt-5.6-sol"},
				CredentialIDs: []string{refA, refDirect}, CredentialProviders: []CredentialProviderSelector{codex, configCodex}},
		}}
	})

	wantModels := []string{"claude-sonnet-4-6", "gpt-5.6-luna", "gpt-5.6-sol"}
	wantIDs := []string{refA, refB, refDirect}
	slices.Sort(wantIDs)
	wantProviders := []CredentialProviderSelector{configCodex, claude, codex}
	for _, test := range []struct {
		model string
		allow bool
	}{
		{model: "gpt-5.6-sol", allow: true},
		{model: "claude-sonnet-4-6", allow: true},
		{model: "gpt-5.6-luna", allow: true},
		{model: "unknown-model", allow: false},
		{model: "", allow: true},
	} {
		t.Run(test.model, func(t *testing.T) {
			decision := store.ResolveRouting("scope-a", test.model, test.model)
			if decision.AllowsModel() != test.allow || !slices.Equal(decision.Models, wantModels) {
				t.Fatalf("model policy=%+v", decision)
			}
			if !slices.Equal(decision.CredentialIDs, wantIDs) || !slices.Equal(decision.CredentialProviders, wantProviders) {
				t.Fatalf("credential union changed with request model: %+v", decision)
			}
		})
	}
	t.Run("deny precedence", func(t *testing.T) {
		ref := CredentialFingerprint("dummy-denied")
		provider := CredentialProviderSelector{Source: CredentialSourceAuthFiles, Provider: "codex"}
		store := newStore(t)
		store.ReplaceAll(func(state *State) {
			state.Routes = []Route{
				{ID: "allow", Name: "allow", Rule: RouteRule{Models: []string{"gpt"}, CredentialIDs: []string{ref}}},
				{ID: "deny", Name: "deny", Rule: RouteRule{DeniedModels: []string{"GPT"}, DeniedCredentialProviders: []CredentialProviderSelector{provider}}},
			}
			state.Keys["scope"] = &KeyState{RouteBindings: RouteBindings{RouteIDs: []string{"allow", "deny"}, RouteRule: RouteRule{Models: []string{"gpt"}, CredentialProviders: []CredentialProviderSelector{provider}}}}
		})
		for _, model := range []string{"gpt", "GPT", "other"} {
			d := store.ResolveRouting("scope", model, model)
			if !d.RestrictsModels() || d.AllowsModel() || len(d.Models) != 1 {
				t.Fatalf("empty effective allowlist became unrestricted: %+v", d)
			}
			if d.AllowsCredential(ref, "auth-files", "codex") {
				t.Fatal("direct/exact allow bypassed provider deny")
			}
			if !d.AllowsCredential(ref, "ai-providers", "codex") {
				t.Fatal("provider deny crossed credential sources")
			}
		}
		if err := store.SetKeyRoutes("scope", RouteBindings{RouteIDs: []string{"allow"}, RouteRule: RouteRule{DeniedModels: []string{"gpt"}, DeniedCredentialIDs: []string{ref}}}); err != nil {
			t.Fatal(err)
		}
		d := store.ResolveRouting("scope", "gpt", "gpt")
		if d.AllowsModel() || d.AllowsCredential(ref, "ai-providers", "codex") {
			t.Fatal("bound allow bypassed direct deny")
		}
	})
}

func TestDirectModelKeepsBoundRouteCredentialRestrictions(t *testing.T) {
	ref := CredentialFingerprint("auth-a")
	provider := CredentialProviderSelector{Source: CredentialSourceAuthFiles, Provider: "codex"}
	for _, rule := range []RouteRule{
		{Models: []string{"gpt-5.6-sol", "gpt-5.6-terra"}, CredentialIDs: []string{ref}},
		{Models: []string{"gpt-5.6-sol", "gpt-5.6-terra"}, CredentialProviders: []CredentialProviderSelector{provider}},
	} {
		store := newStore(t)
		store.ReplaceAll(func(state *State) {
			state.Routes = []Route{{ID: "codex", Name: "Codex", Rule: rule}}
			state.Keys["scope-a"] = &KeyState{RouteBindings: RouteBindings{RouteIDs: []string{"codex"}, RouteRule: RouteRule{Models: []string{"gpt-5.6-luna"}}}}
		})
		decision := store.ResolveRouting("scope-a", "gpt-5.6-luna", "gpt-5.6-luna")
		if !decision.AllowsModel() || !decision.RestrictsCredentials() || !slices.Equal(decision.CredentialIDs, rule.CredentialIDs) || !slices.Equal(decision.CredentialProviders, rule.CredentialProviders) {
			t.Fatalf("direct model lost bound route credentials: %+v", decision)
		}
	}
}

func TestRoutingEmptyDimensionsRemainIndependent(t *testing.T) {
	ref := CredentialFingerprint("auth-a")
	for _, test := range []struct {
		name        string
		rules       []RouteRule
		models      bool
		credentials bool
	}{
		{name: "unbound"},
		{name: "empty route", rules: []RouteRule{{}}},
		{name: "models only", rules: []RouteRule{{Models: []string{"allowed-model"}}}, models: true},
		{name: "denied models only", rules: []RouteRule{{DeniedModels: []string{"other-model"}}}, models: true},
		{name: "denied credentials only", rules: []RouteRule{{DeniedCredentialIDs: []string{ref}}}, credentials: true},
		{name: "credentials only", rules: []RouteRule{{CredentialIDs: []string{ref}}}, credentials: true},
		{name: "separate routes", rules: []RouteRule{{Models: []string{"allowed-model"}}, {CredentialIDs: []string{ref}}, {}}, models: true, credentials: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newStore(t)
			store.ReplaceAll(func(state *State) {
				key := &KeyState{}
				for i, rule := range test.rules {
					id := "route-" + strconv.Itoa(i)
					state.Routes = append(state.Routes, Route{ID: id, Name: id, Rule: rule})
					key.RouteBindings.RouteIDs = append(key.RouteBindings.RouteIDs, id)
				}
				state.Keys["scope-a"] = key
			})
			for _, model := range []string{"allowed-model", "other-model"} {
				decision := store.ResolveRouting("scope-a", model, model)
				if decision.RestrictsModels() != test.models || decision.RestrictsCredentials() != test.credentials || decision.AllowsModel() != (!test.models || model == "allowed-model") {
					t.Fatalf("independent dimensions: %+v", decision)
				}
			}
		})
	}
}

func TestDirectModelAndCredentialBindingsComposeAcrossPhases(t *testing.T) {
	ref := CredentialFingerprint("auth-a")
	otherRef := CredentialFingerprint("auth-b")
	for _, test := range []struct {
		name         string
		rule         RouteRule
		allowedModel string
		deniedModel  string
		allowedRef   string
		deniedRef    string
	}{
		{"allow", RouteRule{Models: []string{"gpt-5.6-sol"}, CredentialIDs: []string{ref}}, "GPT-5.6-SOL", "other", ref, otherRef},
		{"deny only", RouteRule{DeniedModels: []string{"gpt-5.6-sol"}, DeniedCredentialIDs: []string{ref}}, "other", "GPT-5.6-SOL", otherRef, ref},
		{"provider with exception", RouteRule{DeniedModels: []string{"gpt-5.6-sol"}, CredentialProviders: []CredentialProviderSelector{{Source: CredentialSourceAuthFiles, Provider: "codex"}}, DeniedCredentialIDs: []string{ref}}, "other", "GPT-5.6-SOL", otherRef, ref},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newStore(t)
			store.ReplaceAll(func(state *State) { state.Keys["scope-a"] = &KeyState{} })
			if err := store.SetKeyRoutes("scope-a", RouteBindings{RouteRule: test.rule}); err != nil {
				t.Fatal(err)
			}
			for _, model := range []string{test.allowedModel, test.deniedModel, ""} {
				d := store.ResolveRouting("scope-a", model, model)
				if d.AllowsModel() != (model != test.deniedModel) || !d.RestrictsModels() || !d.RestrictsCredentials() {
					t.Fatalf("model policy: %+v", d)
				}
				if !d.AllowsCredential(test.allowedRef, " AUTH-FILES ", " Codex ") || d.AllowsCredential(test.deniedRef, "auth-files", "codex") {
					t.Fatalf("credential policy changed with request model: %+v", d)
				}
			}
		})
	}
}

func TestRouteWritesReplaceOnlyThatRoutesKeyBindings(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) {
		state.Keys["a"] = &KeyState{}
		state.Keys["b"] = &KeyState{RouteBindings: RouteBindings{RouteRule: RouteRule{Models: []string{"gpt-5.5"}}}}
	})
	route, err := store.CreateRoute(Route{Name: "Codex", Rule: RouteRule{Models: []string{"gpt-5.6-sol"}}}, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	bindings := func(scope string) RouteBindings {
		key, _ := store.KeyViewForScope(scope)
		return key.RouteBindings
	}
	if !slices.Contains(bindings("a").RouteIDs, route.ID) || !slices.Contains(bindings("b").RouteIDs, route.ID) {
		t.Fatalf("route was not bound at create")
	}
	selected := []string{"a"}
	name := "Codex updated"
	if _, err = store.UpdateRoute(RoutePatch{ID: route.ID, Name: &name}, &selected); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(bindings("a").RouteIDs, route.ID) {
		t.Fatal("selected key lost route binding")
	}
	bBindings := bindings("b")
	if slices.Contains(bBindings.RouteIDs, route.ID) || !slices.Contains(bBindings.Models, "gpt-5.5") {
		t.Fatalf("unselected key bindings=%+v", bBindings)
	}
	t.Run("views preserve stored permissions", func(t *testing.T) {
		store := newStore(t)
		store.ReplaceAll(func(state *State) { state.Keys["scope"] = &KeyState{} })
		route, err := store.CreateRoute(Route{ID: "deny", Name: "deny", Rule: RouteRule{DeniedModels: []string{"gpt"}}}, []string{"scope"})
		if err != nil {
			t.Fatal(err)
		}
		route.Rule.DeniedModels[0] = "other"
		view := store.RouteViews()[0]
		view.Rule.DeniedModels[0] = "other"
		if err := store.SetKeyRoutes("scope", RouteBindings{RouteIDs: []string{"deny"}, RouteRule: RouteRule{DeniedCredentialIDs: []string{CredentialFingerprint("dummy")}}}); err != nil {
			t.Fatal(err)
		}
		key, _ := store.KeyViewForScope("scope")
		key.RouteBindings.DeniedCredentialIDs[0] = CredentialFingerprint("changed")
		d := store.ResolveRouting("scope", "gpt", "gpt")
		if d.AllowsModel() || d.AllowsCredential(CredentialFingerprint("dummy"), "", "") {
			t.Fatal("mutable view changed stored blacklist")
		}
		result, err := store.DeleteRoute("deny")
		if err != nil || result.FullyUnrestrictedKeys != 0 {
			t.Fatalf("deletion ignored remaining blacklist: %+v, %v", result, err)
		}
	})
}

func TestRoutingUsesTheBillingModelIdentity(t *testing.T) {
	for _, mode := range []string{"allow", "deny"} {
		t.Run(mode, func(t *testing.T) {
			store := newStore(t)
			store.ReplaceAll(func(state *State) {
				state.Prices = map[string]CustomPrice{"chat/fast": {ModelID: "chat/fast"}, "chat/slow": {ModelID: "chat/slow"}}
				rule := RouteRule{Models: []string{"CHAT/Fast"}}
				if mode == "deny" {
					rule.Models, rule.DeniedModels = nil, rule.Models
				}
				state.Routes = []Route{{ID: "fast", Name: "Fast", Rule: rule}}
				state.Keys["scope-a"] = &KeyState{RouteBindings: RouteBindings{RouteIDs: []string{"fast"}}}
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
					want := test.want
					if mode == "deny" && test.requested != "" {
						want = !want
					}
					if decision.AllowsModel() != want {
						t.Fatalf("decision=%+v", decision)
					}
					if !decision.AllowsModel() && strings.Contains(decision.Model, "(") {
						t.Fatalf("refused model kept thinking suffix: %q", decision.Model)
					}
				})
			}
		})
	}
}

func TestRoutingSeparatesConfiguredSuffixedModels(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) {
		state.Prices = map[string]CustomPrice{"chat/fast": {ModelID: "chat/fast"}, "chat/fast(high)": {ModelID: "chat/fast(high)"}}
		state.Keys["scope-a"] = &KeyState{RouteBindings: RouteBindings{RouteRule: RouteRule{Models: []string{"chat/fast"}}}}
	})
	if decision := store.ResolveRouting("scope-a", "chat/fast", "chat/fast(high)"); decision.AllowsModel() {
		t.Fatalf("configured suffixed model inherited base grant: %+v", decision)
	}
	if err := store.SetKeyRoutes("scope-a", RouteBindings{RouteRule: RouteRule{Models: []string{"chat/fast", "chat/fast(high)"}}}); err != nil {
		t.Fatal(err)
	}
	if decision := store.ResolveRouting("scope-a", "chat/fast", "chat/fast(high)"); !decision.AllowsModel() {
		t.Fatalf("explicit suffixed grant was denied: %+v", decision)
	}
}

func TestMissingRouteFailsClosed(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) {
		state.Keys["scope-a"] = &KeyState{RouteBindings: RouteBindings{RouteIDs: []string{"missing"}}}
	})
	decision := store.ResolveRouting("scope-a", "gpt-5.6", "gpt-5.6")
	if decision.AllowsModel() || decision.ConfigurationError == "" {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestRouteRuleNormalizesSelections(t *testing.T) {
	ref, deniedRef := CredentialFingerprint("dummy-ref"), CredentialFingerprint("dummy-denied")
	provider := CredentialProviderSelector{Source: CredentialSourceAuthFiles, Provider: "codex"}
	deniedProvider := CredentialProviderSelector{Source: CredentialSourceAuthFiles, Provider: "claude"}
	rule, err := NormalizeRouteRule(RouteRule{
		Models: []string{" GPT ", "gpt"}, DeniedModels: []string{" Claude ", "claude"},
		CredentialIDs: []string{ref, ref}, DeniedCredentialIDs: []string{deniedRef, deniedRef},
		CredentialProviders: []CredentialProviderSelector{
			{Source: " AUTH-FILES ", Provider: " CODEX "}, provider,
			{Source: CredentialSourceAIProviders, Provider: "codex"},
		},
		DeniedCredentialProviders: []CredentialProviderSelector{{Source: " AUTH-FILES ", Provider: " CLAUDE "}, deniedProvider},
	})
	if err != nil || !slices.Equal(rule.Models, []string{"GPT"}) || !slices.Equal(rule.DeniedModels, []string{"Claude"}) ||
		!slices.Equal(rule.CredentialIDs, []string{ref}) || !slices.Equal(rule.DeniedCredentialIDs, []string{deniedRef}) ||
		!slices.Equal(rule.CredentialProviders, []CredentialProviderSelector{provider, {Source: CredentialSourceAIProviders, Provider: "codex"}}) ||
		!slices.Equal(rule.DeniedCredentialProviders, []CredentialProviderSelector{deniedProvider}) {
		t.Fatalf("normalized selections: %+v, %v", rule, err)
	}
	for _, rule := range []RouteRule{
		{Models: []string{" "}},
		{DeniedModels: []string{" "}},
		{CredentialIDs: []string{"dummy-plaintext-key"}},
		{DeniedCredentialIDs: []string{"dummy-plaintext-key"}},
		{CredentialProviders: []CredentialProviderSelector{{Provider: "codex"}}},
		{DeniedCredentialProviders: []CredentialProviderSelector{{Provider: "codex"}}},
		{Models: []string{"gpt"}, DeniedModels: []string{"GPT"}},
		{CredentialIDs: []string{ref}, DeniedCredentialIDs: []string{ref}},
		{CredentialProviders: []CredentialProviderSelector{provider}, DeniedCredentialProviders: []CredentialProviderSelector{provider}},
	} {
		if _, err := NormalizeRouteRule(rule); KindOf(err) != KindInvalid {
			t.Fatalf("accepted invalid selection: %+v, %v", rule, err)
		}
	}
}

func TestDirectCredentialProviderBindingRestrictsOneSource(t *testing.T) {
	provider := CredentialProviderSelector{Source: CredentialSourceAuthFiles, Provider: "Codex"}
	for _, mode := range []string{"allow", "deny"} {
		t.Run(mode, func(t *testing.T) {
			store := newStore(t)
			store.ReplaceAll(func(state *State) { state.Keys["scope-a"] = &KeyState{} })
			rule := RouteRule{CredentialProviders: []CredentialProviderSelector{provider}}
			if mode == "deny" {
				rule.CredentialProviders, rule.DeniedCredentialProviders = nil, rule.CredentialProviders
			}
			if err := store.SetKeyRoutes("scope-a", RouteBindings{RouteRule: rule}); err != nil {
				t.Fatal(err)
			}
			d := store.ResolveRouting("scope-a", "gpt-5.6-sol", "gpt-5.6-sol")
			if !d.AllowsModel() || !d.RestrictsCredentials() {
				t.Fatalf("decision=%+v", d)
			}
			for _, test := range []struct {
				source, provider    string
				allowOnly, denyOnly bool
			}{
				{"auth-files", "codex", true, false},
				{"", "codex", false, false},
				{"auth-files", "", false, false},
				{"", "", false, false},
				{"ai-providers", "codex", false, true},
				{"auth-files", "claude", false, true},
				{"", "claude", false, true},
			} {
				want := test.allowOnly
				if mode == "deny" {
					want = test.denyOnly
				}
				if d.AllowsCredential(CredentialFingerprint("dummy"), test.source, test.provider) != want {
					t.Fatalf("classification source=%q provider=%q", test.source, test.provider)
				}
			}
		})
	}
}

func TestDeleteRouteCascadesBindingsAndReportsWidening(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) {
		state.Routes = []Route{{ID: "only", Name: "Only", Rule: RouteRule{Models: []string{"gpt"}, CredentialProviders: []CredentialProviderSelector{{Source: CredentialSourceAIProviders, Provider: "xai"}}}}}
		state.Keys["scope-a"] = &KeyState{RouteBindings: RouteBindings{RouteIDs: []string{"only"}}}
		state.Keys["scope-deleted"] = &KeyState{DeletedAt: time.Now(), RouteBindings: RouteBindings{RouteIDs: []string{"only"}}}
	})
	views := store.RouteViews()
	if len(views) != 1 || views[0].BoundKeyCount != 2 || views[0].DeletedKeyCount != 1 {
		t.Fatalf("views=%+v", views)
	}
	selected := []string{"scope-a", "scope-deleted"}
	name := "Renamed"
	if _, err := store.UpdateRoute(RoutePatch{ID: "only", Name: &name}, &selected); err != nil {
		t.Fatal(err)
	}
	store.Read(func(state *State) {
		if len(state.Keys["scope-deleted"].RouteBindings.RouteIDs) != 1 {
			t.Fatal("editing lost a selected binding")
		}
	})

	result, err := store.DeleteRoute("only")
	if err != nil || result.AffectedKeys != 2 || result.DeletedKeys != 1 || result.FullyUnrestrictedKeys != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	store.Read(func(state *State) {
		if len(state.Keys["scope-deleted"].RouteBindings.RouteIDs) != 0 {
			t.Fatalf("deleted key retained route binding: %+v", state.Keys["scope-deleted"].RouteBindings)
		}
	})
}

func TestRouteEditCanUnbindDeletedKeys(t *testing.T) {
	store := newStore(t)
	store.ReplaceAll(func(state *State) {
		state.Routes = []Route{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}
		state.Keys["deleted"] = &KeyState{Preview: "sk-tes…0001", DeletedAt: time.Now(),
			RouteBindings: RouteBindings{RouteIDs: []string{"a", "b"}, RouteRule: RouteRule{Models: []string{"gpt-5.5"}}}}
	})
	name := "Renamed"
	if _, err := store.UpdateRoute(RoutePatch{ID: "a", Name: &name}, nil); err != nil {
		t.Fatal(err)
	}
	selected := []string{"deleted"}
	if _, err := store.UpdateRoute(RoutePatch{ID: "a"}, &selected); err != nil {
		t.Fatal(err)
	}
	selected = []string{}
	if _, err := store.UpdateRoute(RoutePatch{ID: "a"}, &selected); err != nil {
		t.Fatal(err)
	}
	store.Read(func(state *State) {
		key := state.Keys["deleted"]
		if len(key.RouteBindings.RouteIDs) != 1 || key.RouteBindings.RouteIDs[0] != "b" ||
			len(key.RouteBindings.Models) != 1 || key.DeletedAt.IsZero() {
			t.Fatalf("unbinding changed unrelated settings: %+v", key)
		}
	})
	selected = []string{"deleted"}
	if _, err := store.UpdateRoute(RoutePatch{ID: "a"}, &selected); err == nil {
		t.Fatal("rebound a deleted key through route editing")
	}
}

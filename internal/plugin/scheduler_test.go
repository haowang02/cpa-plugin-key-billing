package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func schedulerRequest(scope string, candidates ...SchedulerAuthCandidate) SchedulerPickRequest {
	request := SchedulerPickRequest{Model: "gpt-5.6", Candidates: candidates}
	request.Options.Metadata = map[string]any{MetadataCallerScope: scope}
	return request
}

func TestSchedulerSeparatesSameProviderBySource(t *testing.T) {
	app, scope := configuredRoutingApp(t, billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAIProviders, Provider: "codex"}}})
	req := schedulerRequest(scope, SchedulerAuthCandidate{ID: "file-codex"}, SchedulerAuthCandidate{ID: "config-codex"})
	raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, req))
	if err != nil {
		t.Fatal(err)
	}
	var response SchedulerPickResponse
	decodeResult(t, raw, &response)
	if !response.Handled || response.AuthID != "config-codex" {
		t.Fatalf("response=%+v", response)
	}
}

func TestSchedulerKeepsRestrictedPoolsLocal(t *testing.T) {
	for _, tc := range []struct {
		rule    billing.RouteRule
		handled bool
	}{
		{billing.RouteRule{}, false},
		{billing.RouteRule{Models: []string{"gpt-5.6"}}, false},
		{billing.RouteRule{CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAIProviders, Provider: "codex"}}}, true},
		{billing.RouteRule{DeniedCredentialIDs: []string{billing.CredentialFingerprint("outside")}}, true},
	} {
		app, scope := configuredRoutingApp(t, tc.rule)
		raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, schedulerRequest(scope, SchedulerAuthCandidate{ID: "config-codex"})))
		if err != nil {
			t.Fatal(err)
		}
		var response SchedulerPickResponse
		decodeResult(t, raw, &response)
		if response.Handled != tc.handled || (tc.handled && response.AuthID != "config-codex") {
			t.Fatalf("rule=%+v response=%+v", tc.rule, response)
		}
	}
}

func TestSchedulerFailsClosedWithoutAUsableRoutedCandidate(t *testing.T) {
	tests := []struct {
		name       string
		rule       billing.RouteRule
		candidates []SchedulerAuthCandidate
		want       routingShortfall
	}{
		{
			name:       "allowed credential unavailable",
			rule:       billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("wanted")}},
			candidates: []SchedulerAuthCandidate{{ID: "other"}},
			want:       routeUnavailable,
		},
		{
			name:       "all denied",
			rule:       billing.RouteRule{DeniedCredentialIDs: []string{billing.CredentialFingerprint("wanted"), billing.CredentialFingerprint("other"), billing.CredentialFingerprint("a")}},
			candidates: []SchedulerAuthCandidate{{ID: "a"}},
			want:       routeNoCredential,
		},
		{
			name:       "non-positive weight",
			rule:       billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("a")}},
			candidates: []SchedulerAuthCandidate{{ID: "a", Attributes: map[string]string{"weight": "0"}}, {ID: "other"}},
			want:       routeUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, scope := configuredRoutingApp(t, test.rule)
			app.SetHostCaller(routingTestHost("wanted", "other", "a"))
			app.scheduler.configure(billing.RoutingConfig{Strategy: "weighted-round-robin", SessionAffinityTTL: "1h"}, true)
			raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, schedulerRequest(scope, test.candidates...)))
			if err != nil {
				t.Fatal(err)
			}
			var envelope Envelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.OK || envelope.Error == nil || envelope.Error.HTTPStatus != test.want.status || envelope.Error.Message != test.want.message("gpt-5.6") {
				t.Fatalf("envelope=%+v, want %q", envelope, test.want.message("gpt-5.6"))
			}
		})
	}
	t.Run("host failure", func(t *testing.T) {
		app, scope := configuredRoutingApp(t, billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("a")}})
		app.SetHostCaller(func(string, any) (json.RawMessage, error) { return nil, errors.New("dummy host failure") })
		if _, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, schedulerRequest(scope, SchedulerAuthCandidate{ID: "a"}))); err == nil {
			t.Fatal("scheduler picked without reading upstream credentials")
		}
	})
}

func TestSchedulerUsesConfiguredStrategies(t *testing.T) {
	candidates := []SchedulerAuthCandidate{
		{ID: "a", Attributes: map[string]string{"weight": "2"}},
		{ID: "b", Attributes: map[string]string{"weight": "1"}},
		{ID: "outside", Attributes: map[string]string{"weight": "1000"}},
	}
	for _, test := range []struct {
		name string
		rule billing.RouteRule
	}{
		{"allow", billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("a"), billing.CredentialFingerprint("b")}}},
		{"deny", billing.RouteRule{DeniedCredentialIDs: []string{billing.CredentialFingerprint("outside")}}},
		{"provider with exception", billing.RouteRule{
			CredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAuthFiles, Provider: "codex"}},
			DeniedCredentialIDs: []string{billing.CredentialFingerprint("outside")},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, strategy := range []struct {
				name string
				a, b int
			}{
				{"round-robin", 15, 15}, {"weighted-round-robin", 20, 10}, {"fill-first", 30, 0},
			} {
				t.Run(strategy.name, func(t *testing.T) {
					app, scope := configuredRoutingApp(t, test.rule)
					app.scheduler.configure(billing.RoutingConfig{Strategy: strategy.name, SessionAffinityTTL: "1h"}, true)
					counts := map[string]int{}
					for range 30 {
						raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, schedulerRequest(scope, candidates...)))
						if err != nil {
							t.Fatal(err)
						}
						var response SchedulerPickResponse
						decodeResult(t, raw, &response)
						if !response.Handled {
							t.Fatalf("response=%+v, want routed selection", response)
						}
						counts[response.AuthID]++
					}
					if counts["a"] != strategy.a || counts["b"] != strategy.b || counts["outside"] != 0 {
						t.Fatalf("counts=%v", counts)
					}
				})
			}
		})
	}
	for _, strategy := range []string{"round-robin", "fill-first", "weighted-round-robin"} {
		var scheduler subsetScheduler
		scheduler.configure(billing.RoutingConfig{Strategy: strategy, SessionAffinityTTL: "1h"}, true)
		want := "a"
		if strategy == "weighted-round-robin" {
			want = "b"
		}
		got := scheduler.pick("key", "pool", "", []SchedulerAuthCandidate{{ID: "a", Priority: 10, Attributes: map[string]string{"weight": "0"}}, {ID: "b"}}, time.Now())
		if got != want {
			t.Fatalf("%s: zero-weight priority picked %q, want %q", strategy, got, want)
		}
	}
}

func TestSubsetSchedulerKeepsAPIKeysAndTemporaryCandidateChangesIndependent(t *testing.T) {
	var scheduler subsetScheduler
	scheduler.configure(billing.RoutingConfig{Strategy: "weighted-round-robin", SessionAffinityTTL: "1h"}, true)
	now := time.Now()
	candidates := []SchedulerAuthCandidate{
		{ID: "a", Attributes: map[string]string{"weight": "1"}},
		{ID: "b", Attributes: map[string]string{"weight": "1"}},
	}
	firstA := scheduler.pick("key-a", "pool", "", candidates, now)
	firstB := scheduler.pick("key-b", "pool", "", candidates, now)
	if firstA != "a" || firstB != "a" {
		t.Fatalf("first picks key-a=%q key-b=%q, want independent pools", firstA, firstB)
	}
	if picked := scheduler.pick("key-a", "pool", "", candidates[1:], now); picked != "b" {
		t.Fatalf("temporary subset pick=%q", picked)
	}
	if picked := scheduler.pick("key-a", "pool", "", candidates, now); picked != "b" {
		t.Fatalf("restored candidate pick=%q, want preserved smooth scores", picked)
	}
	if picked := scheduler.pick("key-b", "pool", "", candidates, now); picked != "b" {
		t.Fatalf("key-b second pick=%q, key-a must not advance it", picked)
	}
	if picked := scheduler.pick("key-a", "pool", "", candidates, now); picked != "a" {
		t.Fatalf("key-a third pick=%q", picked)
	}
	scheduler.prune(map[string]struct{}{"key-a": {}})
	if picked := scheduler.pick("key-a", "pool", "", candidates, now); picked != "b" {
		t.Fatalf("retained key pick=%q", picked)
	}
	if picked := scheduler.pick("key-b", "pool", "", candidates, now); picked != "a" {
		t.Fatalf("pruned key pick=%q", picked)
	}
	// Prune both score maps even when repeated weight changes reset Current.
	for i := 0; i < maxCredentialsPerPool+3; i++ {
		scheduler.pick("key-a", "weights", "", []SchedulerAuthCandidate{
			{ID: "a", Attributes: map[string]string{"weight": fmt.Sprint(1 + i%2)}},
			{ID: fmt.Sprintf("rotating-%d", i)},
		}, now)
	}
	if state := scheduler.keys["key-a"]["weights"]; len(state.Current) > maxCredentialsPerPool || len(state.Weights) > maxCredentialsPerPool {
		t.Fatalf("unbounded weight history: current=%d weights=%d", len(state.Current), len(state.Weights))
	}
	// Pool eviction must preserve progress for recently used models.
	scheduler.configure(billing.RoutingConfig{Strategy: "round-robin", SessionAffinityTTL: "1h"}, true)
	scheduler.pick("key-a", "active", "", candidates, now)
	for i := 0; i < maxPoolsPerKey; i++ {
		scheduler.pick("key-a", fmt.Sprintf("pool-%d", i), "", candidates, now.Add(time.Duration(i)*time.Second))
		if i == maxPoolsPerKey-2 {
			scheduler.pick("key-a", "active", "", candidates, now.Add(time.Duration(i)*time.Second))
		}
	}
	if len(scheduler.keys["key-a"]) != maxPoolsPerKey || scheduler.keys["key-a"]["active"] == nil || scheduler.keys["key-a"]["pool-0"] != nil {
		t.Fatal("pool eviction discarded recent progress or exceeded capacity")
	}
}

// Issue #25: route membership is applied before priority, for Codex auth files
// as well as configured providers. Unavailable candidates are removed by CPA.
func TestRoutePriorityIssue25(t *testing.T) {
	a := SchedulerAuthCandidate{ID: "codex-a", Priority: 10}
	b := SchedulerAuthCandidate{ID: "codex-b", Priority: 0}
	for _, tc := range []struct {
		name       string
		allowed    []string
		candidates []SchedulerAuthCandidate
		want       string
	}{
		{"only A", []string{a.ID}, []SchedulerAuthCandidate{a, b}, a.ID},
		{"only B", []string{b.ID}, []SchedulerAuthCandidate{a, b}, b.ID},
		{"both preserve host priority", []string{a.ID, b.ID}, []SchedulerAuthCandidate{a, b}, a.ID},
		{"A disabled or cooling down", []string{a.ID, b.ID}, []SchedulerAuthCandidate{b}, b.ID},
		{"allowed subset retains priority", []string{a.ID, b.ID}, []SchedulerAuthCandidate{a, b, {ID: "outside", Priority: 20}}, a.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids := []string{}
			for _, id := range tc.allowed {
				ids = append(ids, billing.CredentialFingerprint(id))
			}
			app, scope := configuredRoutingApp(t, billing.RouteRule{CredentialIDs: ids})
			raw, err := app.pickCredential(mustMarshal(t, SchedulerPickRequest{Model: "gpt-5.6", Options: struct {
				Metadata map[string]any `json:"Metadata"`
			}{map[string]any{MetadataCallerScope: scope}}, Candidates: tc.candidates}))
			if err != nil {
				t.Fatal(err)
			}
			var result SchedulerPickResponse
			decodeResult(t, raw, &result)
			if !result.Handled || result.AuthID != tc.want {
				t.Fatalf("decision=%+v", result)
			}
		})
	}
}

func TestSchedulerSessionAffinity(t *testing.T) {
	var scheduler subsetScheduler
	scheduler.configure(billing.RoutingConfig{Strategy: "round-robin", SessionAffinity: true, SessionAffinityTTL: "10s"}, true)
	a := SchedulerAuthCandidate{ID: "a", Priority: 10}
	b := SchedulerAuthCandidate{ID: "b", Priority: 0}
	equal := []SchedulerAuthCandidate{a, {ID: "b", Priority: 10}}
	now := time.Now()
	for _, tc := range []struct {
		name, scope, pool, session string
		candidates                 []SchedulerAuthCandidate
		seconds                    int
		want                       string
	}{
		{"cold low priority", "key-a", "pool", "session", []SchedulerAuthCandidate{b}, 0, "b"},
		{"binding survives higher priority recovery", "key-a", "pool", "session", []SchedulerAuthCandidate{a, b}, 1, "b"},
		{"new session uses highest priority", "key-a", "pool", "new", []SchedulerAuthCandidate{a, b}, 2, "a"},
		{"failover", "key-a", "pool", "session", []SchedulerAuthCandidate{a}, 3, "a"},
		{"new binding retained", "key-a", "pool", "session", []SchedulerAuthCandidate{a, b}, 4, "a"},
		{"caller isolation", "key-b", "pool", "session", equal, 5, "a"},
		{"route isolation", "key-a", "another-pool", "session", equal, 5, "a"},
		{"ttl bind", "key-a", "ttl", "session", equal, 10, "a"},
		{"missing session rotates normally", "key-a", "ttl", "", equal, 15, "b"},
		{"ttl refreshed on hit", "key-a", "ttl", "session", equal, 19, "a"},
		{"anonymous rotation advances", "key-a", "ttl", "", equal, 20, "a"},
		{"binding outlives original expiry", "key-a", "ttl", "session", equal, 21, "a"},
		{"idle expiry reselects", "key-a", "ttl", "session", equal, 32, "b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := scheduler.pick(tc.scope, tc.pool, tc.session, tc.candidates, now.Add(time.Duration(tc.seconds)*time.Second)); got != tc.want {
				t.Fatalf("picked %q, want %q", got, tc.want)
			}
		})
	}
	scheduler.pick("key-b", "pool", "session", equal, now.Add(33*time.Second))
	scheduler.prune(map[string]struct{}{"key-a": {}})
	for key := range scheduler.sessions {
		if key.Scope != "key-a" {
			t.Fatal("removed caller retained a session binding")
		}
	}
}

func TestSchedulerConfigurationUpdatesAndSessionMetadata(t *testing.T) {
	app, path := newAppWithPriceAndState(t, true)
	app.SetHostCaller(routingTestHost("a", "b"))
	candidates := []SchedulerAuthCandidate{{ID: "a"}, {ID: "b"}}
	if _, err := app.store.SyncKeys([]string{"dummy-scheduler-key"}, false); err != nil {
		t.Fatal(err)
	}
	caller := billing.CallerScope("dummy-scheduler-key")
	if _, err := app.store.CreateRoute(billing.Route{Name: "restricted", Rule: billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("a"), billing.CredentialFingerprint("b")}}}, []string{caller}); err != nil {
		t.Fatal(err)
	}
	reconfigure := func(strategy, ttl string, affinity bool) error {
		raw := mustMarshal(t, LifecycleRequest{ConfigYAML: []byte(fmt.Sprintf("enabled: true\nstate_file: %q\nrouting:\n  strategy: %s\n  session-affinity: %t\n  session-affinity-ttl: %s\n", path, strategy, affinity, ttl))})
		_, err := app.HandleMethod(MethodPluginReconfigure, raw)
		return err
	}
	if err := reconfigure("round-robin", "1h", true); err != nil {
		t.Fatal(err)
	}
	req := schedulerRequest(caller, candidates...)
	req.Options.Metadata["canonical_session_id"] = "session"
	pick := func() string {
		raw, err := app.HandleMethod(MethodSchedulerPick, mustMarshal(t, req))
		if err != nil {
			t.Fatal(err)
		}
		var result SchedulerPickResponse
		decodeResult(t, raw, &result)
		if !result.Handled {
			t.Fatal("restricted route delegated to host")
		}
		return result.AuthID
	}
	if pick() != "a" || pick() != "a" {
		t.Fatal("scheduler did not consume host session metadata")
	}
	if err := reconfigure("rr", "60m", true); err != nil {
		t.Fatal(err)
	}
	if len(app.scheduler.sessions) != 1 {
		t.Fatal("equivalent reconfiguration lost session state")
	}
	if err := reconfigure("invalid", "1h", true); err == nil {
		t.Fatal("accepted invalid routing strategy")
	}
	if pick() != "a" {
		t.Fatal("failed reconfiguration changed active bindings")
	}
	for _, tc := range []struct {
		metadata map[string]any
		want     string
	}{
		{map[string]any{"derived_session_id": "session"}, "b"},
		{map[string]any{"execution_session_id": "session", "derived_session_id": "session"}, "a"},
		{map[string]any{"canonical_session_id": "session", "execution_session_id": "other"}, "a"},
		{map[string]any{"canonical_session_id": false, "derived_session_id": "session"}, "b"},
	} {
		req.Options.Metadata = tc.metadata
		req.Options.Metadata[MetadataCallerScope] = caller
		if got := pick(); got != tc.want {
			t.Fatalf("session identity precedence or isolation failed: metadata=%+v picked=%q", tc.metadata, got)
		}
	}
	// Model spelling, provider and thinking suffixes follow CPA's pool identity;
	// billing-price normalization must not merge case-sensitive model routes.
	req.Options.Metadata = map[string]any{MetadataCallerScope: caller, "derived_session_id": "session"}
	for _, tc := range []struct{ model, provider, want string }{
		{"GPT-5.6", "", "a"},
		{"gpt-5.6", "", "b"},
		{"gpt-5.6(high)", "", "b"},
		{"gpt-5.6", "codex", "a"},
	} {
		req.Model, req.Provider = tc.model, tc.provider
		if got := pick(); got != tc.want {
			t.Fatalf("model=%q provider=%q picked=%q, want %q", tc.model, tc.provider, got, tc.want)
		}
	}
	req.Model, req.Provider = "gpt-5.6", ""
	req.Options.Metadata = map[string]any{MetadataCallerScope: caller, "canonical_session_id": "session"}
	if err := reconfigure("round-robin", "1h", false); err != nil {
		t.Fatal(err)
	}
	if len(app.scheduler.sessions) != 0 {
		t.Fatal("disabling affinity retained bindings")
	}
	if pick() != "a" || pick() != "b" {
		t.Fatal("disabled affinity did not rotate")
	}
	if err := reconfigure("fill-first", "1h", true); err != nil {
		t.Fatal(err)
	}
	if pick() != "a" || pick() != "a" {
		t.Fatal("fill-first was not applied")
	}
	// Updating permissions must invalidate the old route namespace immediately.
	if err := app.store.SetKeyRoutes(caller, billing.RouteBindings{RouteRule: billing.RouteRule{CredentialIDs: []string{billing.CredentialFingerprint("b")}}}); err != nil {
		t.Fatal(err)
	}
	if pick() != "b" {
		t.Fatal("session escaped updated route permissions")
	}
}

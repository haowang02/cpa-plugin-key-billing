package billing

import (
	"testing"
	"time"
)

// The credential shapes CLIProxyAPI publishes usage for: two signed-in accounts,
// and three providers that are one API key configured three ways, which is what
// makes the provider the part that separates them.
func TestCredentialNameOfEveryUpstreamShape(t *testing.T) {
	const upstreamKey = "sk-upstream-key-0001"
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)

	for _, learned := range []struct {
		authIndex, provider, authType, account, want string
	}{
		{"9f14c2a70b6d38e5", "codex", "oauth", "haowang4455@gmail.com", "codex · haowang4455@gmail.com"},
		{"3b8d40e1af92c576", "xai", "oauth", "00f7ghqi90@haowang.im", "xai · 00f7ghqi90@haowang.im"},
		{"c07a1e5b9d2f4836", "codex", "apikey", upstreamKey, "codex · sk-ups…0001"},
		{"51fa8c39d7e0b264", "claude", "apikey", upstreamKey, "claude · sk-ups…0001"},
		{"2e6b04c8fa137d95", "openai-compatible-deepseek", "apikey", upstreamKey, "deepseek · sk-ups…0001"},
		// A provider whose account repeats its own name is named once.
		{"7c3f19ab5e04d268", "openai-compatible-local", "apikey", "", "local"},
	} {
		event := subsetEvent("scope-a", now)
		event.AuthIndex = learned.authIndex
		event.Provider = learned.provider
		event.AuthType = learned.authType
		event.Account = learned.account
		store.RecordUsage(event)
		store.Read(func(state *State) {
			if got := state.Credentials[learned.authIndex].Name(); got != learned.want {
				t.Errorf("%s: Name() = %q, want %q", learned.authIndex, got, learned.want)
			}
		})
	}

	store.Read(func(state *State) {
		names := make(map[string]string, len(state.Credentials))
		for authIndex, credential := range state.Credentials {
			if other, collides := names[credential.Name()]; collides {
				t.Errorf("%s and %s share the name %q", authIndex, other, credential.Name())
			}
			names[credential.Name()] = authIndex
		}
	})
}

func TestCredentialLearningNeverPersistsAnUnclassifiedOrDownstreamSecret(t *testing.T) {
	const downstreamKey = "sk-downstream-secret-0001"
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)

	for _, learned := range []struct {
		authIndex string
		authType  string
		account   string
	}{
		{authIndex: "unknown-auth", account: "unknown-secret"},
		{authIndex: "oauth-fallback", authType: "oauth", account: downstreamKey},
	} {
		event := subsetEvent(CallerScope(downstreamKey), now)
		event.AuthIndex = learned.authIndex
		event.Provider = "provider"
		event.AuthType = learned.authType
		event.Account = learned.account
		store.RecordUsage(event)
	}

	store.Read(func(state *State) {
		for _, authIndex := range []string{"unknown-auth", "oauth-fallback"} {
			if credential := state.Credentials[authIndex]; credential.Account != "" || credential.Provider != "provider" {
				t.Errorf("%s credential = %+v", authIndex, credential)
			}
		}
	})
}

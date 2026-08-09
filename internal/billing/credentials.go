package billing

import "strings"

// MaxCredentials caps the learned credential table so a host that churns
// through auth indices cannot grow the state document without bound.
const MaxCredentials = 256

const (
	// authKindAPIKey is CLIProxyAPI's credential kind for an API key, whose
	// account identifier is a secret and must be masked.
	authKindAPIKey = "apikey"
	// openAICompatiblePrefix namespaces every OpenAI-compatible provider an
	// operator configures, and says nothing the name behind it does not.
	openAICompatiblePrefix = "openai-compatible-"
)

// Credential names one upstream credential the way CLIProxyAPI resolves it for
// its own usage records, which is what cpa-usage-keeper displays too.
type Credential struct {
	Provider string `json:"provider,omitempty"`
	// Account is the address an OAuth credential signed in with, or the masked
	// key of an API key.
	Account string `json:"account,omitempty"`
}

// Name is the credential as the billing log shows it.
//
// The provider leads because neither half identifies an upstream on its own:
// one account can sign into several providers, and one API key can be
// configured as several providers at once.
func (c Credential) Name() string {
	provider := strings.TrimPrefix(c.Provider, openAICompatiblePrefix)
	switch {
	case provider == "":
		return c.Account
	case c.Account == "" || strings.EqualFold(c.Account, provider):
		return provider
	default:
		return provider + " · " + c.Account
	}
}

// LearnCredential records what an upstream credential is, from the usage record
// CLIProxyAPI publishes for every request it serves.
//
// Usage is the only account the host gives of a provider configured in
// config.yaml: its credential lookup describes auth files alone.
func (s *Store) LearnCredential(authIndex, provider, authType, account string) {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return
	}
	if strings.EqualFold(strings.TrimSpace(authType), authKindAPIKey) {
		account = PreviewKey(account)
	}
	learned := Credential{Provider: strings.TrimSpace(provider), Account: strings.TrimSpace(account)}
	if learned == (Credential{}) {
		return
	}
	updateResult(s, func(state *State) (struct{}, bool) {
		if state.Credentials == nil {
			state.Credentials = make(map[string]Credential)
		}
		if known, exists := state.Credentials[authIndex]; exists {
			if known == learned {
				return struct{}{}, false
			}
		} else if len(state.Credentials) >= MaxCredentials {
			pruneCredentialOrphans(state)
			if len(state.Credentials) >= MaxCredentials {
				return struct{}{}, false
			}
		}
		state.Credentials[authIndex] = learned
		return struct{}{}, true
	})
}

// pruneCredentialOrphans drops the credentials no retained log entry refers to.
// Their names are only ever read through the log, so nothing else can miss them.
func pruneCredentialOrphans(state *State) {
	referenced := make(map[string]struct{}, len(state.Credentials))
	for _, entry := range state.Log {
		referenced[entry.AuthIndex] = struct{}{}
	}
	for authIndex := range state.Credentials {
		if _, keep := referenced[authIndex]; !keep {
			delete(state.Credentials, authIndex)
		}
	}
}

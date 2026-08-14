package billing

import "strings"

const (
	// authKindAPIKey is CLIProxyAPI's credential kind for an API key, whose
	// account identifier is a secret and must be masked.
	authKindAPIKey = "apikey"
	// openAICompatiblePrefix namespaces every OpenAI-compatible provider an
	// operator configures, and says nothing the name behind it does not.
	openAICompatiblePrefix = "openai-compatible-"
)

type Credential struct {
	Provider string `json:"provider,omitempty"`
	// Account is the address an OAuth credential signed in with, or the masked
	// key of an API key.
	Account string `json:"account,omitempty"`
}

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
	updateResult(s, func(state *State) (struct{}, Changes) {
		if state.Credentials[authIndex] == learned {
			return struct{}{}, Changes{}
		}
		if state.Credentials == nil {
			state.Credentials = make(map[string]Credential)
		}
		state.Credentials[authIndex] = learned
		return struct{}{}, Changes{Credentials: true}
	})
}

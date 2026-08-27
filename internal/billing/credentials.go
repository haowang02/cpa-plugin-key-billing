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

func learnCredential(state *State, scope, authIndex, provider, authType, account string) bool {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return false
	}
	learned := usageCredential(scope, provider, authType, account)
	if learned == (Credential{}) {
		return false
	}
	if state.Credentials[authIndex] == learned {
		return false
	}
	if state.Credentials == nil {
		state.Credentials = make(map[string]Credential)
	}
	state.Credentials[authIndex] = learned
	return true
}

func usageCredential(scope, provider, authType, account string) Credential {
	switch strings.ToLower(strings.TrimSpace(authType)) {
	case authKindAPIKey:
		account = PreviewKey(account)
	case "oauth":
		// Some credentials have no account identity, in which case the host's
		// source fallback is the downstream API key. Never persist that plaintext.
		if CallerScope(account) == normalizeScope(scope) {
			account = ""
		}
	default:
		// An unclassified source may be a secret. The provider remains useful on
		// its own, so do not persist an account value whose kind is unknown.
		account = ""
	}
	return Credential{Provider: strings.TrimSpace(provider), Account: strings.TrimSpace(account)}
}

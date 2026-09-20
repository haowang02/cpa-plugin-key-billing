package plugin

import (
	"net/http"
	"sort"
	"strings"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/messages"
)

type accountIdentity struct {
	Preview string `json:"preview,omitempty"`
	Label   string `json:"label,omitempty"`
}

type accountSubscription struct {
	Name string `json:"name,omitempty"`
	billing.QuotaView
}

type accountConcurrency struct {
	Limit   int `json:"limit"`
	Current int `json:"current"`
}

type accountProfileResponse struct {
	Tracked  bool            `json:"tracked"`
	Identity accountIdentity `json:"identity"`
}

type accountSubscriptionResponse struct {
	Subscription accountSubscription `json:"subscription"`
	Concurrency  accountConcurrency  `json:"concurrency"`
}

type accountRoutingResponse struct {
	Models            []string                 `json:"models"`
	Credentials       []accountRouteCredential `json:"credentials"`
	DeniedModels      []string                 `json:"denied_models"`
	DeniedCredentials []accountRouteCredential `json:"denied_credentials"`
	RoutingValid      bool                     `json:"routing_valid"`
	Warnings          []string                 `json:"warnings"`
	WarningMessages   []messages.Message       `json:"warning_messages,omitempty"`
}

type accountRouteCredential struct {
	Denied       bool             `json:"denied,omitempty"`
	Source       string           `json:"source,omitempty"`
	Provider     string           `json:"provider,omitempty"`
	Name         string           `json:"name,omitempty"`
	NameMessage  messages.Message `json:"name_message,omitzero"`
	Status       string           `json:"status,omitempty"`
	ProviderWide bool             `json:"provider_wide,omitempty"`
}

func (a *App) accountProfile(access viewAccess) ManagementResponse {
	response := accountProfileResponse{Tracked: access.Tracked}
	if access.Tracked {
		response.Identity = accountIdentity{Preview: access.Key.Preview, Label: access.Key.Label}
	}
	return apiKeyJSON(http.StatusOK, response)
}

func (a *App) accountSubscription(access viewAccess) ManagementResponse {
	if !access.Tracked {
		return apiKeyUnauthorized()
	}
	view := access.Key
	return apiKeyJSON(http.StatusOK, accountSubscriptionResponse{
		Subscription: accountSubscription{Name: view.PlanName, QuotaView: view.QuotaView},
		Concurrency:  accountConcurrency{Limit: view.ConcurrencyLimit, Current: view.CurrentConcurrency},
	})
}

func (a *App) accountRouting(access viewAccess) ManagementResponse {
	if !access.Tracked {
		return apiKeyUnauthorized()
	}
	decision := a.store.ResolveRouting(access.Scope, "", "")
	response := accountRoutingResponse{
		Models: decision.Models, DeniedModels: decision.DeniedModels,
		Credentials: []accountRouteCredential{}, DeniedCredentials: []accountRouteCredential{},
		RoutingValid: decision.ConfigurationError == "", Warnings: []string{},
	}
	if decision.ConfigurationError != "" {
		response.Warnings = append(response.Warnings, "The routing rule no longer exists; contact your administrator")
		response.WarningMessages = append(response.WarningMessages, messages.New("The routing rule no longer exists; contact your administrator"))
	}
	if !decision.RestrictsCredentials() {
		return apiKeyJSON(http.StatusOK, response)
	}
	if err := a.refreshCredentialInventory(); err != nil {
		response.Warnings = append(response.Warnings, "Failed to load upstream credentials")
		response.WarningMessages = append(response.WarningMessages, messages.New("Failed to load upstream credentials"))
	}
	inventory := a.credentialInventory()
	var warnings []messages.Message
	response.Credentials, warnings = accountRoutingCredentials(inventory, decision.CredentialIDs, decision.CredentialProviders, decision)
	for _, warning := range warnings {
		response.Warnings = append(response.Warnings, warning.Text)
		response.WarningMessages = append(response.WarningMessages, warning)
	}
	// Denied provider selectors stay provider-wide, including future credentials.
	for _, selector := range decision.DeniedCredentialProviders {
		response.DeniedCredentials = append(response.DeniedCredentials, accountRouteCredential{
			Source: selector.Source, Provider: selector.Provider, ProviderWide: true, Denied: true,
		})
	}
	denied, _ := accountRoutingCredentials(inventory, decision.DeniedCredentialIDs, nil, decision)
	response.DeniedCredentials = append(response.DeniedCredentials, denied...)
	return apiKeyJSON(http.StatusOK, response)
}

func accountRoutingCredentials(inventory []credentialView, refs []string, providers []billing.CredentialProviderSelector, decision billing.RoutingDecision) ([]accountRouteCredential, []messages.Message) {
	warnings := []messages.Message{}
	byRef := map[string]credentialView{}
	for _, item := range inventory {
		byRef[item.Ref] = item
	}
	credentials := make([]accountRouteCredential, 0, len(refs)+len(providers))
	seenRefs := map[string]struct{}{}
	addCredential := func(item credentialView) {
		if _, exists := seenRefs[item.Ref]; exists {
			return
		}
		seenRefs[item.Ref] = struct{}{}
		value := accountCredential(item)
		value.Denied = !decision.AllowsCredential(item.Ref, item.Source, item.Provider)
		credentials = append(credentials, value)
	}
	missingCredential := false
	for _, ref := range refs {
		if credential, ok := byRef[ref]; ok {
			addCredential(credential)
		} else if !missingCredential {
			credentials = append(credentials, accountRouteCredential{Name: "The selected upstream credential is unavailable", NameMessage: messages.New("The selected upstream credential is unavailable"), Status: "missing", Denied: !decision.AllowsCredential(ref, "", "")})
			missingCredential = true
		}
	}
	for _, selector := range providers {
		matched := false
		for _, credential := range inventory {
			if credential.Source == selector.Source && credential.Provider == selector.Provider {
				addCredential(credential)
				matched = true
			}
		}
		if matched {
			continue
		}
		credentials = append(credentials, accountRouteCredential{
			Source: selector.Source, Provider: selector.Provider, Status: "missing", ProviderWide: true,
			Denied: !decision.AllowsCredential("", selector.Source, selector.Provider),
		})
		switch selector.Source {
		case billing.CredentialSourceAuthFiles:
			warnings = append(warnings, messages.New("No auth-file credentials match provider %q", selector.Provider))
		case billing.CredentialSourceAIProviders:
			warnings = append(warnings, messages.New("No configured API keys match provider %q", selector.Provider))
		default:
			warnings = append(warnings, messages.New("No upstream credentials match %q", selector.Source+" · "+selector.Provider))
		}
	}
	sort.Slice(credentials, func(i, j int) bool {
		left, right := credentials[i], credentials[j]
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		return left.Name < right.Name
	})
	return credentials, warnings
}

func accountCredential(item credentialView) accountRouteCredential {
	status := item.Status
	if status == "" {
		status = "active"
	}
	return accountRouteCredential{Source: item.Source, Provider: item.Provider, Name: maskEmailText(item.DisplayName), NameMessage: item.DisplayMessage, Status: status}
}
func accountScope(headers http.Header) (string, bool) {
	values := headers.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 8192 {
		return "", false
	}
	scope := billing.CallerScope(parts[1])
	return scope, scope != ""
}

func apiKeyUnauthorized() ManagementResponse {
	response := apiKeyJSONError(http.StatusUnauthorized, "unauthorized", "Invalid API key")
	response.Headers.Set("WWW-Authenticate", `Bearer realm="cpa-key-billing-account"`)
	return response
}

func apiKeyJSON(status int, payload any) ManagementResponse {
	response := JSONResponse(status, payload)
	secureAPIKeyResponse(&response)
	return response
}

func apiKeyJSONError(status int, code, message string) ManagementResponse {
	response := JSONError(status, code, message)
	secureAPIKeyResponse(&response)
	return response
}

func secureAPIKeyResponse(response *ManagementResponse) {
	response.Headers.Set("Cache-Control", "private, no-store")
	response.Headers.Set("Pragma", "no-cache")
	response.Headers.Set("Vary", "Authorization")
	response.Headers.Set("Referrer-Policy", "no-referrer")
	response.Headers.Set("X-Content-Type-Options", "nosniff")
}

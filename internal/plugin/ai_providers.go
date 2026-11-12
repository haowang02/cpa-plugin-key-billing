package plugin

import (
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/messages"
)

type aiProviderView struct {
	Ref string `json:"ref"`
	billing.ConfigCredential
	InFlight int                           `json:"in_flight"`
	Usage    *billing.CredentialUsageStats `json:"usage"`
}

type aiProviderListResponse struct {
	Providers []aiProviderView `json:"providers"`
}

func (a *App) visibleConfigCredentials(access viewAccess) map[string]billing.ConfigCredential {
	credentials := a.store.ConfigCredentials()
	if access.APIKey {
		decision := a.store.ResolveRouting(access.Scope, "", "")
		for ref, credential := range credentials {
			if !decision.AllowsCredential(ref, billing.CredentialSourceAIProviders, credential.Provider) {
				delete(credentials, ref)
			}
		}
	}
	return credentials
}

func (a *App) aiProviders(req ManagementRequest, access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return apiKeyUnauthorized()
	}
	var from, to time.Time
	if err := timeRangeParams(req.Query, &from, &to); err != nil {
		return viewErrorResponse(access, err)
	}
	providers := make([]aiProviderView, 0)
	indexes := make([]string, 0)
	concurrency := a.store.CredentialConcurrency(access.usageScope())
	for ref, credential := range a.visibleConfigCredentials(access) {
		providers = append(providers, aiProviderView{Ref: ref, ConfigCredential: credential, InFlight: concurrency[ref]})
		if credential.AuthIndex != "" {
			indexes = append(indexes, credential.AuthIndex)
		}
	}
	usage, err := a.store.CredentialUsageStats(indexes, access.usageScope(), from, to)
	if err != nil {
		return viewErrorResponse(access, err)
	}
	for i := range providers {
		if providers[i].AuthIndex != "" {
			stats := usage[providers[i].AuthIndex]
			providers[i].Usage = &stats
		}
	}
	sort.Slice(providers, func(i, j int) bool {
		left, right := providers[i], providers[j]
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		if left.BaseURL != right.BaseURL {
			return left.BaseURL < right.BaseURL
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.Ref < right.Ref
	})
	return viewJSON(access, http.StatusOK, aiProviderListResponse{Providers: providers})
}

func (a *App) syncAIProviderBalance(req ManagementRequest) ManagementResponse {
	var body struct {
		AuthIndex string                    `json:"auth_index"`
		Balance   billing.CredentialBalance `json:"balance"`
	}
	if err := decodeStrict(req.Body, &body); err != nil {
		return errorResponse(err)
	}
	if body.Balance.FetchedAt.IsZero() || len(body.Balance.Balances) == 0 {
		return JSONError(http.StatusBadRequest, "invalid", "Invalid balance snapshot")
	}
	if err := a.store.SetCredentialBalance(body.AuthIndex, &body.Balance); err != nil {
		return errorResponse(err)
	}
	return JSONResponse(http.StatusOK, body.Balance)
}

func (a *App) setAIProviderNote(req ManagementRequest) ManagementResponse {
	var body struct {
		Ref  string `json:"ref"`
		Note string `json:"note"`
	}
	if err := decodeStrict(req.Body, &body); err != nil {
		return errorResponse(err)
	}
	if err := a.store.SetCredentialNote(strings.ToLower(strings.TrimSpace(body.Ref)), cleanText(body.Note)); err != nil {
		return errorResponse(err)
	}
	return JSONResponse(http.StatusOK, struct{}{})
}

// syncConfiguredCredentials stores the AI provider entries the UI read from
// the host configuration. A loaded entry is the host credential behind its auth
// index, so its ref fingerprints the runtime ID the host assigned; an entry the
// host has not loaded is known only by its configuration identity.
func (a *App) syncConfiguredCredentials(req ManagementRequest) ManagementResponse {
	var body struct {
		Credentials []billing.ConfigCredentialDetails `json:"credentials"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	if len(body.Credentials) > 4096 {
		return JSONError(http.StatusBadRequest, "invalid", "Too many configured credentials")
	}

	runtimeIDs := map[string]string{}
	if slices.ContainsFunc(body.Credentials, func(item billing.ConfigCredentialDetails) bool { return item.AuthIndex != "" }) {
		credentials, err := a.hostCredentials()
		if err != nil {
			return jsonMessageError(http.StatusBadGateway, "host_unavailable", messages.New("Failed to load upstream credentials: %s", err.Error()))
		}
		for _, credential := range credentials {
			runtimeIDs[credential.AuthIndex] = credential.ID
		}
	}
	next := make(map[string]billing.ConfigCredentialDetails, len(body.Credentials))
	for _, credential := range body.Credentials {
		credential.Provider = strings.ToLower(strings.TrimSpace(credential.Provider))
		credential.ConfigRef = strings.ToLower(strings.TrimSpace(credential.ConfigRef))
		if !billing.ValidCredentialFingerprint(credential.ConfigRef) || credential.Provider == "" || len(credential.Provider) > 160 ||
			cleanText(credential.Provider) != credential.Provider {
			return JSONError(http.StatusBadRequest, "invalid", "Invalid configured credential identifier")
		}
		credential.KeyPreview = billing.PreviewKey(cleanText(credential.KeyPreview))
		ref := credential.ConfigRef
		if id, loaded := runtimeIDs[credential.AuthIndex]; loaded {
			ref = billing.CredentialFingerprint(id)
		}
		next[ref] = credential
	}

	if err := func() error {
		a.routingMu.Lock()
		defer a.routingMu.Unlock()
		previous := a.store.ConfigCredentials()
		if err := a.store.SyncConfigCredentials(next); err != nil {
			return err
		}
		a.replaceSyncedCredentials(previous, a.store.ConfigCredentials())
		return nil
	}(); err != nil {
		return errorResponse(err)
	}
	return JSONResponse(http.StatusOK, map[string]any{"credentials": a.credentialInventory()})
}

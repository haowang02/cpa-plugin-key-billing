package plugin

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
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

func (a *App) syncConfiguredCredentials(req ManagementRequest) ManagementResponse {
	var body struct {
		Credentials []struct {
			Ref string `json:"ref"`
			billing.ConfigCredentialDetails
		} `json:"credentials"`
	}
	if errDecode := decodeStrict(req.Body, &body); errDecode != nil {
		return errorResponse(errDecode)
	}
	if len(body.Credentials) > 4096 {
		return JSONError(http.StatusBadRequest, "invalid", "Too many configured credentials")
	}

	next := make(map[string]billing.ConfigCredentialDetails, len(body.Credentials))
	for _, item := range body.Credentials {
		ref := strings.ToLower(strings.TrimSpace(item.Ref))
		provider := strings.ToLower(strings.TrimSpace(item.Provider))
		if !billing.ValidCredentialFingerprint(ref) || provider == "" || len(provider) > 160 || cleanText(provider) != provider {
			return JSONError(http.StatusBadRequest, "invalid", "Invalid configured credential identifier")
		}
		credential := item.ConfigCredentialDetails
		credential.Provider = provider
		credential.ConfigRef = strings.ToLower(strings.TrimSpace(credential.ConfigRef))
		if credential.ConfigRef != "" && !billing.ValidCredentialFingerprint(credential.ConfigRef) {
			return JSONError(http.StatusBadRequest, "invalid", "Invalid configured credential identifier")
		}
		credential.KeyPreview = billing.PreviewKey(cleanText(item.KeyPreview))
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

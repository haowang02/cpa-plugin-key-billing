package plugin

import (
	"cmp"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/messages"
)

type credentialView struct {
	Ref            string           `json:"ref"`
	Source         string           `json:"source"`
	Provider       string           `json:"provider"`
	ProviderName   string           `json:"provider_name"`
	DisplayName    string           `json:"display_name"`
	DisplayMessage messages.Message `json:"display_name_message,omitzero"`
	Status         string           `json:"status,omitempty"`
	Disabled       bool             `json:"disabled"`
	Unavailable    bool             `json:"unavailable"`
}

var secretLikeToken = regexp.MustCompile(`(?i)(?:(?:sk|key|token)-[a-z0-9_\-]{4,}|[a-z0-9_\-]{24,})`)
var emailLikeToken = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)

func cleanText(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
}

func safeCredentialName(raw, account, provider, ref string) string {
	name := strings.TrimSpace(filepath.Base(strings.ReplaceAll(raw, "\\", "/")))
	if account = strings.TrimSpace(account); account != "" {
		name = strings.ReplaceAll(name, account, billing.PreviewKey(account))
	}
	name = secretLikeToken.ReplaceAllStringFunc(name, billing.PreviewKey)
	name = emailLikeToken.ReplaceAllStringFunc(name, billing.PreviewKey)
	name = cleanText(name)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = strings.TrimSpace(provider) + " upstream credential " + shortCredentialRef(ref)
	}
	if len([]byte(name)) > 160 {
		name = string([]byte(name)[:160])
	}
	return name
}

// CPA keys an OpenAI-compatible provider by its lowercased configured name.
func providerName(provider string) string { return strings.TrimPrefix(provider, "openai-compatible-") }

func shortCredentialRef(ref string) string {
	value := strings.TrimPrefix(ref, "sha256:")
	if len(value) > 8 {
		value = value[:8]
	}
	return value
}

// Views cover every host credential and every synced configuration entry, loaded or not.
func (a *App) credentialViews() map[string]credentialView {
	directory := a.cachedDirectory()
	configured := a.store.ConfigCredentials()
	views := make(map[string]credentialView, len(directory.Credentials)+len(configured))
	for ref, item := range configured {
		views[ref] = configuredCredentialView(ref, item, item.Provider)
	}
	for _, credential := range directory.Credentials {
		views[credential.Ref] = hostCredentialView(credential, configured)
	}
	return views
}

func hostCredentialView(credential directoryCredential, configured map[string]billing.ConfigCredential) credentialView {
	ref := credential.Ref
	if file := credential.File; file != nil {
		return credentialView{Ref: ref, Source: credential.Source, Provider: credential.Provider, ProviderName: providerName(credential.Provider),
			DisplayName: credentialDisplayName(*file, credential.Source, credential.Provider, ref), Status: file.Status,
			Disabled: file.Disabled, Unavailable: file.Unavailable}
	}
	if item, synced := configured[ref]; synced {
		return configuredCredentialView(ref, item, credential.Provider)
	}
	short := shortCredentialRef(ref)
	return credentialView{Ref: ref, Source: credential.Source, Provider: credential.Provider, ProviderName: providerName(credential.Provider),
		DisplayName: "Configured credential " + short, DisplayMessage: messages.New("Configured credential %s", short)}
}

func credentialDisplayName(file hostAuthFile, source, provider, ref string) string {
	if source == billing.CredentialSourceAuthFiles {
		return authFileTitle(file)
	}
	return safeCredentialName(cmp.Or(strings.TrimSpace(file.Label), file.Name), file.Account, provider, ref)
}

func configuredCredentialView(ref string, item billing.ConfigCredential, provider string) credentialView {
	name := item.KeyPreview
	var detail messages.Message
	if name == "" {
		name = "No API key configured"
		detail = messages.New("No API key configured")
	}
	status := "active"
	if item.Disabled {
		status = "disabled"
	}
	return credentialView{
		Ref: ref, Source: billing.CredentialSourceAIProviders, Provider: provider, ProviderName: cmp.Or(item.Name, providerName(provider)),
		DisplayName: name, DisplayMessage: detail, Status: status, Disabled: item.Disabled,
	}
}

// Route logs name credentials without reading the host.
func (a *App) selectedCredentialView(id string) (credentialView, bool) {
	credential, known := a.cachedDirectory().Credentials[id]
	if !known {
		return credentialView{}, false
	}
	return hostCredentialView(credential, a.store.ConfigCredentials()), true
}

func credentialInventory(views map[string]credentialView) []credentialView {
	result := make([]credentialView, 0, len(views))
	for _, item := range views {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Source != result[j].Source {
			return result[i].Source < result[j].Source
		}
		if result[i].Provider != result[j].Provider {
			return result[i].Provider < result[j].Provider
		}
		return result[i].DisplayName < result[j].DisplayName
	})
	return result
}

// Table summaries use known labels without reading the host.
func credentialLabels(views map[string]credentialView, refs []string) map[string]string {
	labels := make(map[string]string, len(refs))
	for _, ref := range refs {
		if item, ok := views[ref]; ok {
			labels[ref] = item.ProviderName + " · " + item.DisplayName
		}
	}
	return labels
}

func (a *App) listCredentials(_ ManagementRequest) ManagementResponse {
	if _, err := a.readCredentialDirectory(); err != nil {
		return jsonMessageError(http.StatusBadGateway, "host_unavailable", messages.FromError(err))
	}
	return JSONResponse(http.StatusOK, map[string]any{"credentials": credentialInventory(a.credentialViews())})
}

package plugin

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
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
		short := strings.TrimPrefix(ref, "sha256:")
		if len(short) > 8 {
			short = short[:8]
		}
		name = strings.TrimSpace(provider) + " upstream credential " + short
	}
	if len([]byte(name)) > 160 {
		name = string([]byte(name)[:160])
	}
	return name
}

// providerName names a provider its configuration leaves unnamed. CPA keys an
// OpenAI-compatible provider by its lowercased configured name.
func providerName(provider string) string { return strings.TrimPrefix(provider, "openai-compatible-") }

func credentialDisplayName(file hostAuthFile, source, provider, ref string) string {
	if source == billing.CredentialSourceAuthFiles {
		return authFileTitle(file)
	}
	name := file.Label
	if strings.TrimSpace(name) == "" {
		name = file.Name
	}
	return safeCredentialName(name, file.Account, provider, ref)
}

func credentialSourceFromHost(file hostAuthFile) string {
	source := strings.ToLower(strings.TrimSpace(file.Source))
	if file.RuntimeOnly {
		return billing.CredentialSourceAIProviders
	}
	if source == "file" || strings.TrimSpace(file.Path) != "" {
		return billing.CredentialSourceAuthFiles
	}
	if source == "memory" || source == "config" || strings.HasPrefix(source, "config:") {
		return billing.CredentialSourceAIProviders
	}
	return ""
}

func routingAllowsCredential(rawID, source, provider string, decision billing.RoutingDecision) bool {
	ref := ""
	if rawID = strings.TrimSpace(rawID); rawID != "" {
		ref = billing.CredentialFingerprint(rawID)
	}
	return decision.AllowsCredential(ref, source, provider)
}

func routingAllowsAuthFile(file hostAuthFile, decision billing.RoutingDecision) bool {
	source := credentialSourceFromHost(file)
	if source == billing.CredentialSourceAIProviders {
		return false
	}
	if source == "" {
		source = billing.CredentialSourceAuthFiles
	}
	provider := file.Provider
	if strings.TrimSpace(provider) == "" {
		provider = file.Type
	}
	return routingAllowsCredential(file.ID, source, provider, decision)
}

func credentialSourceFromCandidate(candidate SchedulerAuthCandidate) string {
	backend := strings.ToLower(strings.TrimSpace(candidate.Attributes["source_backend"]))
	source := strings.ToLower(strings.TrimSpace(candidate.Attributes["source"]))
	runtimeOnly := strings.EqualFold(strings.TrimSpace(candidate.Attributes["runtime_only"]), "true")
	if backend == "config" || backend == "memory" || runtimeOnly || strings.HasPrefix(source, "config:") || source == "memory" || source == "runtime" || source == "runtime_only" {
		return billing.CredentialSourceAIProviders
	}
	if backend == "file" || backend == "git" || backend == "objectstore" || backend == "postgres" || strings.TrimSpace(candidate.Attributes["path"]) != "" || source == "file" || source == "filesystem" || source == "git" || source == "objectstore" || source == "postgres" {
		return billing.CredentialSourceAuthFiles
	}
	return ""
}

// Display data synchronized by the UI is not evidence of credential identity.
// Config credentials use the immutable identity observed in host candidates;
// physical auth files are checked against the current host runtime entry.
func (a *App) selectedCredentialAllowed(req RequestInterceptRequest, decision billing.RoutingDecision) (bool, error) {
	id := metadataString(req.Metadata, MetadataSelectedAuth)
	if id == "" {
		return false, errors.New("The host did not report the selected upstream credential")
	}
	ref := billing.CredentialFingerprint(id)
	if slices.Contains(decision.DeniedCredentialIDs, ref) ||
		len(decision.CredentialProviders) == 0 && len(decision.CredentialIDs) > 0 && !slices.Contains(decision.CredentialIDs, ref) {
		return false, nil
	}
	if len(decision.DeniedCredentialProviders) == 0 && decision.AllowsCredential(ref, "", "") {
		return true, nil
	}
	a.routingMu.Lock()
	identity := a.credentialIdentities[id]
	a.routingMu.Unlock()
	if identity.Source != billing.CredentialSourceAIProviders {
		index := metadataString(req.Metadata, MetadataSelectedIndex)
		if index == "" || a.hostCaller == nil {
			return false, errors.New("The host did not provide enough information to verify the selected upstream credential")
		}
		raw, err := a.hostCaller(hostAuthGetRuntime, map[string]string{"auth_index": index})
		if err != nil {
			return false, errors.New("The host could not provide runtime information for the selected upstream credential")
		}
		var response struct {
			Auth hostAuthFile `json:"auth"`
		}
		if err := json.Unmarshal(raw, &response); err != nil || response.Auth.ID != id || response.Auth.AuthIndex != index {
			return false, errors.New("The host returned inconsistent identity information for the selected upstream credential")
		}
		identity = billing.CredentialProviderSelector{
			Source:   credentialSourceFromHost(response.Auth),
			Provider: strings.ToLower(strings.TrimSpace(firstNonEmptyString(response.Auth.Provider, response.Auth.Type))),
		}
	}
	if identity.Source == "" || identity.Provider == "" {
		return false, errors.New("The host did not identify the source and provider of the selected upstream credential")
	}
	a.routingMu.Lock()
	a.credentialIdentities[id] = identity
	a.routingMu.Unlock()
	return routingAllowsCredential(id, identity.Source, identity.Provider, decision), nil
}

// host.auth.models backs model catalog filtering and explains routed requests
// left without a credential. Older hosts fail this probe.
func (a *App) probeHostAuthModels() bool {
	if a.hostCaller == nil {
		return false
	}
	if _, err := a.hostCaller(hostAuthModels, map[string]any{}); err != nil {
		a.store.AddPluginLog(billing.PluginLogDebug, "Upstream credential models are unavailable: %v", err)
		return false
	}
	return true
}

// credentialModels collects the models served by credentials the rules allow.
// Sources matter only to provider selectors. host.auth.list omits exactly the
// config-backed credentials, so a credential it does not list is an AI
// provider key.
func (a *App) credentialModels(decision billing.RoutingDecision) (map[string]bool, error) {
	raw, err := a.hostCaller(hostAuthModels, map[string]any{})
	if err != nil {
		return nil, err
	}
	var response struct {
		Credentials []struct {
			ID       string   `json:"id"`
			Provider string   `json:"provider"`
			Models   []string `json:"models"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("Parse credential models: %w", err)
	}
	sources := map[string]string{}
	if len(decision.CredentialProviders) > 0 || len(decision.DeniedCredentialProviders) > 0 {
		files, err := a.listHostAuthFiles()
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			sources[file.ID] = credentialSourceFromHost(file)
		}
	}
	provided := map[string]bool{}
	for _, credential := range response.Credentials {
		source, listed := sources[credential.ID]
		if !listed {
			source = billing.CredentialSourceAIProviders
		}
		if routingAllowsCredential(credential.ID, source, credential.Provider, decision) {
			for _, model := range credential.Models {
				provided[model] = true
			}
		}
	}
	return provided, nil
}

func (a *App) refreshCredentialInventory() error {
	files, err := a.listHostAuthFiles()
	if err != nil {
		return err
	}
	next := make(map[string]credentialView, len(files))
	raw := make(map[string]billing.CredentialProviderSelector, len(files))
	for _, file := range files {
		id := strings.TrimSpace(file.ID)
		if id == "" {
			continue
		}
		ref := billing.CredentialFingerprint(id)
		provider := strings.ToLower(strings.TrimSpace(file.Provider))
		if provider == "" {
			provider = strings.ToLower(strings.TrimSpace(file.Type))
		}
		source := credentialSourceFromHost(file)
		if source == "" {
			continue
		}
		next[ref] = credentialView{Ref: ref, Source: source, Provider: provider, ProviderName: providerName(provider),
			DisplayName: credentialDisplayName(file, source, provider, ref), Status: file.Status,
			Disabled: file.Disabled, Unavailable: file.Unavailable}
		raw[id] = billing.CredentialProviderSelector{Source: source, Provider: provider}
	}
	a.routingMu.Lock()
	// host.auth.list may omit config-backed credentials.
	for ref, item := range a.credentials {
		if item.Source == billing.CredentialSourceAIProviders {
			if _, ok := next[ref]; !ok {
				next[ref] = item
			}
		}
	}
	for id, identity := range a.credentialIdentities {
		if _, listed := raw[id]; !listed && identity.Source == billing.CredentialSourceAIProviders {
			if _, ok := next[billing.CredentialFingerprint(id)]; ok {
				raw[id] = identity
			}
		}
	}
	a.credentials, a.credentialIdentities = next, raw
	a.routingMu.Unlock()
	return nil
}

func (a *App) observeCandidates(candidates []SchedulerAuthCandidate) {
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	for _, candidate := range candidates {
		id := strings.TrimSpace(candidate.ID)
		if id == "" {
			continue
		}
		ref := billing.CredentialFingerprint(id)
		source := credentialSourceFromCandidate(candidate)
		if source == "" {
			delete(a.credentialIdentities, id)
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(candidate.Provider))
		short := shortCredentialRef(ref)
		view := credentialView{Ref: ref, Source: source, Provider: provider, ProviderName: providerName(provider), Status: candidate.Status,
			DisplayName: "Configured credential " + short, DisplayMessage: messages.New("Configured credential %s", short)}
		if source == billing.CredentialSourceAuthFiles {
			// A file credential's ID is its path under the auth directory.
			view.DisplayName, view.DisplayMessage = authFileTitle(hostAuthFile{Name: filepath.Base(id)}), messages.Message{}
		}
		if existing, ok := a.credentials[ref]; ok && existing.DisplayName != "" {
			view.ProviderName, view.DisplayName, view.DisplayMessage = existing.ProviderName, existing.DisplayName, existing.DisplayMessage
		}
		a.credentials[ref] = view
		a.credentialIdentities[id] = billing.CredentialProviderSelector{Source: source, Provider: provider}
	}
}

func (a *App) observeCredentialUsage(authIndex, authType, account string) {
	if !strings.EqualFold(strings.TrimSpace(authType), "apikey") || account == "" {
		return
	}
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	ref := a.credentialRefsByIndex[strings.TrimSpace(authIndex)]
	credential, ok := a.credentials[ref]
	if !ok || credential.Source != billing.CredentialSourceAIProviders {
		return
	}
	credential.DisplayName = account
	credential.DisplayMessage = messages.Message{}
	a.credentials[ref] = credential
}

// Caller holds routingMu across persistence and inventory publication.
func (a *App) replaceSyncedCredentials(previous, next map[string]billing.ConfigCredential) {
	for id := range a.credentialIdentities {
		ref := billing.CredentialFingerprint(id)
		if _, synced := previous[ref]; synced {
			if _, retained := next[ref]; !retained {
				delete(a.credentialIdentities, id)
			}
		}
	}
	for ref := range previous {
		delete(a.credentials, ref)
	}
	for ref, item := range next {
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
		a.credentials[ref] = credentialView{
			Ref: ref, Source: billing.CredentialSourceAIProviders, Provider: item.Provider, ProviderName: cmp.Or(item.Name, providerName(item.Provider)),
			DisplayName: name, DisplayMessage: detail, Status: status, Disabled: item.Disabled,
		}
	}
}

func shortCredentialRef(ref string) string {
	value := strings.TrimPrefix(ref, "sha256:")
	if len(value) > 8 {
		value = value[:8]
	}
	return value
}

func (a *App) credentialInventory() []credentialView {
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	result := make([]credentialView, 0, len(a.credentials))
	for _, item := range a.credentials {
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

// Table summaries use known labels without discovering host credentials.
func (a *App) credentialLabels(refs []string) map[string]string {
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	labels := make(map[string]string, len(refs))
	for _, ref := range refs {
		if item, ok := a.credentials[ref]; ok {
			labels[ref] = item.ProviderName + " · " + item.DisplayName
		}
	}
	return labels
}

func (a *App) listCredentials(_ ManagementRequest) ManagementResponse {
	if err := a.refreshCredentialInventory(); err != nil {
		return jsonMessageError(http.StatusBadGateway, "host_unavailable", messages.FromError(err))
	}
	return JSONResponse(http.StatusOK, map[string]any{"credentials": a.credentialInventory()})
}

func (a *App) credentialByRawID(id string) (credentialView, bool) {
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	id = strings.TrimSpace(id)
	item, ok := a.credentials[billing.CredentialFingerprint(id)]
	if identity, known := a.credentialIdentities[id]; known {
		// A synced name belongs to the provider the UI reported.
		if identity.Provider != item.Provider {
			item.ProviderName = providerName(identity.Provider)
		}
		item.Source, item.Provider = identity.Source, identity.Provider
	}
	return item, ok
}

func (a *App) missingCredentialRef(refs []string) string {
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	for _, ref := range refs {
		if _, ok := a.credentials[strings.ToLower(strings.TrimSpace(ref))]; !ok {
			return ref
		}
	}
	return ""
}

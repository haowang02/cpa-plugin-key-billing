package plugin

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"cpa-key-billing/internal/billing"
)

type credentialView struct {
	Ref         string `json:"ref"`
	Source      string `json:"source"`
	Provider    string `json:"provider"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status,omitempty"`
	Disabled    bool   `json:"disabled"`
	Unavailable bool   `json:"unavailable"`
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
		name = strings.TrimSpace(provider) + " 上游凭证 " + short
	}
	if len([]byte(name)) > 160 {
		name = string([]byte(name)[:160])
	}
	return name
}

func credentialDisplayName(file hostAuthFile, source, provider, ref string) string {
	if source == billing.CredentialSourceAuthFiles {
		// Authentication-file identity comes only from CPA's explicit email.
		email := cleanText(file.Email)
		if email == "" {
			return "未提供邮箱"
		}
		return email
	}
	name := file.Label
	if strings.TrimSpace(name) == "" {
		name = file.Name
	}
	return safeCredentialName(name, file.Account, provider, ref)
}

func credentialSourceFromHost(file hostAuthFile) string {
	source := strings.ToLower(strings.TrimSpace(file.Source))
	if file.RuntimeOnly || source == "memory" || source == "config" || strings.HasPrefix(source, "config:") {
		return billing.CredentialSourceAIProviders
	}
	if source == "file" || strings.TrimSpace(file.Path) != "" {
		return billing.CredentialSourceAuthFiles
	}
	return ""
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

func (a *App) refreshCredentialInventory() error {
	files, err := a.listHostAuthFiles()
	if err != nil {
		return err
	}
	next := make(map[string]credentialView, len(files))
	raw := make(map[string]string, len(files))
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
		next[ref] = credentialView{Ref: ref, Source: source, Provider: provider,
			DisplayName: credentialDisplayName(file, source, provider, ref), Status: file.Status,
			Disabled: file.Disabled, Unavailable: file.Unavailable}
		raw[id] = ref
	}
	a.routingMu.Lock()
	// host.auth.list does not enumerate every config-backed AI Provider on all
	// supported CPA builds. Keep safe request-time discoveries until restart.
	for ref, item := range a.credentials {
		if item.Source == billing.CredentialSourceAIProviders {
			if _, ok := next[ref]; !ok {
				next[ref] = item
			}
		}
	}
	for id, ref := range a.credentialsByRawID {
		if _, ok := next[ref]; ok {
			raw[id] = ref
		}
	}
	a.credentials, a.credentialsByRawID = next, raw
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
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(candidate.Provider))
		name := provider + " 上游凭证 " + shortCredentialRef(ref)
		if source == billing.CredentialSourceAuthFiles {
			name = "未提供邮箱"
		}
		if existing, ok := a.credentials[ref]; ok && existing.DisplayName != "" {
			name = existing.DisplayName
		}
		a.credentials[ref] = credentialView{Ref: ref, Source: source, Provider: provider, DisplayName: name, Status: candidate.Status}
		a.credentialsByRawID[id] = ref
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

func (a *App) credentialByRawID(id string) (credentialView, bool) {
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	ref, ok := a.credentialsByRawID[strings.TrimSpace(id)]
	if !ok {
		return credentialView{}, false
	}
	item, ok := a.credentials[ref]
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

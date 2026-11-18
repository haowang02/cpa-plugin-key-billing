package plugin

import (
	"slices"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

const credentialDirectoryTTL = time.Minute

// The credential directory lists every credential the host has loaded, with the models it serves.
type credentialDirectory struct {
	ReadAt      time.Time
	Credentials map[string]directoryCredential
}

type directoryCredential struct {
	Ref      string
	Source   string
	Provider string
	// CPA matches models case-insensitively.
	Models map[string]bool
	// File is nil for a config API key.
	File *hostAuthFile
}

func (a *App) readCredentialDirectory() (credentialDirectory, error) {
	readAt := a.store.Now()
	credentials, err := a.hostCredentials()
	if err != nil {
		return credentialDirectory{}, err
	}
	files, err := a.listHostAuthFiles()
	if err != nil {
		return credentialDirectory{}, err
	}
	listed := make(map[string]hostAuthFile, len(files))
	for _, file := range files {
		listed[file.ID] = file
	}
	directory := credentialDirectory{ReadAt: readAt, Credentials: make(map[string]directoryCredential, len(credentials))}
	for _, credential := range credentials {
		entry := directoryCredential{
			Ref: billing.CredentialFingerprint(credential.ID), Source: billing.CredentialSourceAIProviders,
			Provider: strings.ToLower(strings.TrimSpace(credential.Provider)), Models: make(map[string]bool, len(credential.Models)),
		}
		for _, model := range credential.Models {
			entry.Models[strings.ToLower(model)] = true
		}
		if file, ok := listed[credential.ID]; ok {
			entry.Source, entry.File = credentialSource(file), &file
		}
		directory.Credentials[credential.ID] = entry
	}
	a.routingMu.Lock()
	if !readAt.Before(a.directory.ReadAt) {
		a.directory = directory
	}
	a.routingMu.Unlock()
	return directory, nil
}

// host.auth.list lists every credential except config API keys; runtime-only ones are provider connections.
func credentialSource(file hostAuthFile) string {
	if file.RuntimeOnly {
		return billing.CredentialSourceAIProviders
	}
	return billing.CredentialSourceAuthFiles
}

func (a *App) cachedDirectory() credentialDirectory {
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	return a.directory
}

// routingDirectory decides from the last read while it is recent, knows every one of ids and admits the request;
// anything else, refusals included, takes a fresh read.
func (a *App) routingDirectory(admits func(credentialDirectory) bool, ids ...string) (credentialDirectory, error) {
	directory := a.cachedDirectory()
	unknown := func(id string) bool {
		_, known := directory.Credentials[id]
		return !known
	}
	if a.store.Now().Sub(directory.ReadAt) < credentialDirectoryTTL && !slices.ContainsFunc(ids, unknown) && admits(directory) {
		return directory, nil
	}
	return a.readCredentialDirectory()
}

func (d credentialDirectory) allows(id string, routing billing.RouteRule) bool {
	credential, known := d.Credentials[id]
	return known && credential.allowed(routing)
}

// CPA selects credentials for a model without its thinking suffix.
func (d credentialDirectory) serves(routing billing.RouteRule, model string) bool {
	model = strings.ToLower(billing.ModelWithoutThinkingSuffix(model))
	for _, credential := range d.Credentials {
		if credential.Models[model] && credential.allowed(routing) {
			return true
		}
	}
	return false
}

func (d credentialDirectory) servedModels(routing billing.RouteRule) map[string]bool {
	served := map[string]bool{}
	for _, credential := range d.Credentials {
		if credential.allowed(routing) {
			for model := range credential.Models {
				served[model] = true
			}
		}
	}
	return served
}

func (c directoryCredential) allowed(routing billing.RouteRule) bool {
	return routing.AllowsCredential(c.Ref, c.Source, c.Provider)
}

func routingAllowsAuthFile(file hostAuthFile, routing billing.RouteRule) bool {
	source := credentialSource(file)
	return source == billing.CredentialSourceAuthFiles &&
		routing.AllowsCredential(billing.CredentialFingerprint(file.ID), source, file.Provider)
}

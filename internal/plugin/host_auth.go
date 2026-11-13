package plugin

import (
	"encoding/json"
	"fmt"
	"time"

	"cpa-key-billing/internal/messages"
)

const (
	hostAuthList       = "host.auth.list"
	hostAuthGet        = "host.auth.get"
	hostAuthGetRuntime = "host.auth.get_runtime"
	hostAuthModels     = "host.auth.models"
)

// Neither host.auth.list nor host.auth.get_runtime reports config API keys.
type hostAuthFile struct {
	ID          string    `json:"id"`
	AuthIndex   string    `json:"auth_index"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Provider    string    `json:"provider"`
	Label       string    `json:"label"`
	Note        string    `json:"note"`
	Status      string    `json:"status"`
	Source      string    `json:"source"`
	Path        string    `json:"path"`
	Account     string    `json:"account"`
	Disabled    bool      `json:"disabled"`
	Unavailable bool      `json:"unavailable"`
	Priority    int       `json:"priority"`
	Email       string    `json:"email"`
	ProjectID   string    `json:"project_id"`
	AccountType string    `json:"account_type"`
	RuntimeOnly bool      `json:"runtime_only"`
	ModTime     time.Time `json:"modtime"`
}

type hostAuthListResponse struct {
	Files []hostAuthFile `json:"files"`
}

func (a *App) listHostAuthFiles() ([]hostAuthFile, error) {
	if a.hostCaller == nil {
		return nil, messages.Errorf("This CLIProxyAPI version does not support reading auth files")
	}
	raw, errCall := a.hostCaller(hostAuthList, map[string]any{})
	if errCall != nil {
		return nil, messages.Errorf("Read auth file list: %w", errCall)
	}
	var response hostAuthListResponse
	if errDecode := json.Unmarshal(raw, &response); errDecode != nil {
		return nil, messages.Errorf("Parse auth file list: %w", errDecode)
	}
	return response.Files, nil
}

func (a *App) hostAuthRuntime(authIndex string) (hostAuthFile, error) {
	raw, err := a.hostCaller(hostAuthGetRuntime, map[string]string{"auth_index": authIndex})
	if err != nil {
		return hostAuthFile{}, err
	}
	var response struct {
		Auth hostAuthFile `json:"auth"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return hostAuthFile{}, fmt.Errorf("Parse auth runtime: %w", err)
	}
	return response.Auth, nil
}

func (a *App) readHostAuthFile(authIndex string) (map[string]any, error) {
	raw, err := a.hostCaller(hostAuthGet, map[string]string{"auth_index": authIndex})
	if err != nil {
		return nil, messages.Errorf("Read auth file: %w", err)
	}
	var response struct {
		JSON json.RawMessage `json:"json"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, messages.Errorf("Parse auth file: %w", err)
	}
	var metadata map[string]any
	if json.Unmarshal(response.JSON, &metadata) != nil {
		return nil, messages.Errorf("Invalid auth file contents")
	}
	return metadata, nil
}

type hostCredential struct {
	ID        string   `json:"id"`
	AuthIndex string   `json:"auth_index"`
	Provider  string   `json:"provider"`
	Models    []string `json:"models"`
}

// hostCredentials includes config API keys, with the models each serves.
func (a *App) hostCredentials() ([]hostCredential, error) {
	raw, err := a.hostCaller(hostAuthModels, map[string]any{})
	if err != nil {
		return nil, err
	}
	var response struct {
		Credentials []hostCredential `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("Parse credential models: %w", err)
	}
	return response.Credentials, nil
}

// Package catalogproxy filters CPA model discovery using Key Billing routing.
// It runs in a separate HTTP process, outside the embedded plugin runtime.
package catalogproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

const routingPath = "/v0/resource/plugins/cpa-key-billing/routing"

type permissions struct {
	models map[string]bool
	owners map[string]bool
}

// New forwards traffic to one CPA origin and filters successful model catalogs.
func New(origin string) (*httputil.ReverseProxy, error) {
	target, err := url.Parse(origin)
	if err != nil || target == nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" ||
		target.User != nil || (target.Path != "" && target.Path != "/") || target.RawQuery != "" || target.Fragment != "" {
		return nil, fmt.Errorf("upstream must be an HTTP(S) origin without credentials, a path, query, or fragment")
	}
	target.Path = ""
	client := &http.Client{
		// A permissions redirect must never receive the caller's credential.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			if isCatalog(request.In) {
				request.Out.Header.Set("Accept-Encoding", "identity")
				request.Out.Header.Del("If-None-Match")
				request.Out.Header.Del("If-Modified-Since")
			}
		},
		ModifyResponse: func(response *http.Response) error {
			if !isCatalog(response.Request) || response.StatusCode != http.StatusOK {
				return nil
			}
			allowed, err := fetchPermissions(client, target, response.Request)
			if err != nil {
				return err
			}
			return filterCatalog(response, allowed)
		},
		ErrorHandler: func(writer http.ResponseWriter, _ *http.Request, _ error) {
			// Upstream errors may contain URLs or headers; do not expose them.
			writer.Header().Set("Cache-Control", "private, no-store")
			http.Error(writer, "model catalog upstream unavailable", http.StatusBadGateway)
		},
	}, nil
}

func isCatalog(request *http.Request) bool {
	if request == nil || request.Method != http.MethodGet {
		return false
	}
	switch request.URL.Path {
	case "/v1/models", "/models", "/backend-api/codex/models", "/codex/models":
		return true
	}
	return false
}

func fetchPermissions(client *http.Client, target *url.URL, incoming *http.Request) (*permissions, error) {
	endpoint := *target
	endpoint.Path = routingPath
	request, err := http.NewRequestWithContext(incoming.Context(), http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create routing request")
	}
	key, err := callerKey(incoming)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch routing permissions")
	}
	body, err := readBody(response.Body, 1<<20)
	if err != nil || response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("routing permissions unavailable")
	}
	var routing struct {
		Valid       *bool    `json:"routing_valid"`
		Models      []string `json:"models"`
		Credentials []struct {
			Provider string `json:"provider"`
			Status   string `json:"status"`
		} `json:"credentials"`
	}
	if json.Unmarshal(body, &routing) != nil || routing.Valid == nil || !*routing.Valid || routing.Models == nil || routing.Credentials == nil {
		return nil, fmt.Errorf("invalid routing permissions")
	}
	allowed := &permissions{models: make(map[string]bool, len(routing.Models))}
	for _, model := range routing.Models {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "" {
			return nil, fmt.Errorf("invalid routing model ID")
		}
		allowed.models[model] = true
	}
	if len(routing.Credentials) > 0 {
		allowed.owners = make(map[string]bool)
		for _, credential := range routing.Credentials {
			if credential.Status == "missing" || credential.Status == "disabled" {
				continue
			}
			provider := strings.ToLower(strings.TrimSpace(credential.Provider))
			if provider == "codex" {
				provider = "openai"
			}
			if provider != "" {
				allowed.owners[provider] = true
			}
		}
	}
	return allowed, nil
}

// Use one unambiguous credential for CPA authentication and routing lookup.
func callerKey(request *http.Request) (string, error) {
	var candidates []string
	for _, authorization := range request.Header.Values("Authorization") {
		parts := strings.Fields(authorization)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return "", fmt.Errorf("invalid caller authentication")
		}
		candidates = append(candidates, parts[1])
	}
	for _, header := range []string{"X-Api-Key", "X-Goog-Api-Key"} {
		candidates = append(candidates, request.Header.Values(header)...)
	}
	for _, parameter := range []string{"key", "api_key"} {
		candidates = append(candidates, request.URL.Query()[parameter]...)
	}
	key := ""
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if len(candidate) > 8192 || (key != "" && candidate != key) {
			return "", fmt.Errorf("ambiguous caller authentication")
		}
		key = candidate
	}
	if key == "" {
		return "", fmt.Errorf("caller authentication required")
	}
	return key, nil
}

func readBody(body io.ReadCloser, limit int64) ([]byte, error) {
	data, errRead := io.ReadAll(io.LimitReader(body, limit+1))
	errClose := body.Close()
	if errRead != nil || errClose != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("invalid upstream body")
	}
	return data, nil
}

func filterCatalog(response *http.Response, allowed *permissions) error {
	body, err := readBody(response.Body, 16<<20)
	if err != nil {
		return err
	}
	var catalog map[string]json.RawMessage
	if json.Unmarshal(body, &catalog) != nil {
		return fmt.Errorf("invalid model catalog")
	}
	found := false
	for _, field := range []string{"data", "models"} {
		raw, exists := catalog[field]
		if !exists {
			continue
		}
		found = true
		var models []map[string]json.RawMessage
		if json.Unmarshal(raw, &models) != nil || models == nil {
			return fmt.Errorf("invalid model entries")
		}
		filtered := make([]map[string]json.RawMessage, 0, len(models))
		for _, model := range models {
			id := modelID(model)
			if id == "" {
				return fmt.Errorf("model identifier missing")
			}
			if len(allowed.models) > 0 && !allowed.models[strings.ToLower(id)] {
				continue
			}
			if allowed.owners != nil {
				var owner string
				_ = json.Unmarshal(model["owned_by"], &owner)
				// The native Codex catalog omits ownership and only contains Codex models.
				if owner == "" && field == "models" {
					owner = "openai"
				}
				if !allowed.owners[strings.ToLower(strings.TrimSpace(owner))] {
					continue
				}
			}
			filtered = append(filtered, model)
		}
		catalog[field], err = json.Marshal(filtered)
		if err != nil {
			return fmt.Errorf("encode filtered models")
		}
	}
	if !found {
		return fmt.Errorf("unsupported model catalog")
	}
	body, err = json.Marshal(catalog)
	if err != nil {
		return fmt.Errorf("encode model catalog")
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Set("Content-Type", "application/json")
	response.Header.Set("Content-Length", strconv.Itoa(len(body)))
	response.Header.Set("Cache-Control", "private, no-store")
	response.Header.Set("Pragma", "no-cache")
	response.Header.Add("Vary", "Authorization, X-Api-Key, X-Goog-Api-Key")
	for _, header := range []string{"ETag", "Last-Modified", "Content-MD5", "Digest"} {
		response.Header.Del(header)
	}
	return nil
}

func modelID(model map[string]json.RawMessage) string {
	for _, field := range []string{"id", "slug"} {
		var id string
		if json.Unmarshal(model[field], &id) == nil && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

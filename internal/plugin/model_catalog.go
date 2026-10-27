package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"cpa-key-billing/internal/billing"
)

// Model catalogs list only the models a key's routing rules let it call. This
// keeps client model pickers accurate; admission still enforces the rules, so
// a request may simply ask for the full catalog.
const (
	modelCatalogHeader       = "X-CPA-Key-Billing-Models"
	claudeCloakedModelPrefix = "claude-fable-5-dd-"
)

func (a *App) filtersModelCatalogs() bool { return a.store.Enabled() && a.authModelsSupported.Load() }

// Every non-streaming execution response also passes through this hook, with
// its full bodies. Only a model catalog names no model, so decode the rest of
// the request only for one.
func (a *App) interceptResponse(raw []byte) ([]byte, error) {
	if !a.filtersModelCatalogs() {
		return OKEnvelope(ResponseInterceptResponse{})
	}
	var head struct {
		Model          string `json:"Model"`
		RequestedModel string `json:"RequestedModel"`
	}
	if errUnmarshal := json.Unmarshal(raw, &head); errUnmarshal != nil {
		return nil, fmt.Errorf("Parse response interception parameters: %w", errUnmarshal)
	}
	if head.Model != "" || head.RequestedModel != "" {
		return OKEnvelope(ResponseInterceptResponse{})
	}
	var req ResponseInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("Parse response interception parameters: %w", errUnmarshal)
	}
	return OKEnvelope(ResponseInterceptResponse{Body: a.filterModelCatalog(req)})
}

func (a *App) filterModelCatalog(req ResponseInterceptRequest) []byte {
	if requestsFullCatalog(req.RequestHeaders) {
		return nil
	}
	scope := a.store.LiveScope(requestKeyScopes(req.RequestHeaders))
	if scope == "" {
		return nil
	}
	decision := a.store.ResolveRouting(scope, "", "")
	if !decision.RestrictsModels() && !decision.RestrictsCredentials() {
		return nil
	}
	catalog, ok := parseModelCatalog(req.Body)
	if !ok {
		return nil
	}
	keep := a.store.AllowedModels(scope, catalog.ids)
	if decision.RestrictsCredentials() {
		provided, err := a.credentialModels(decision)
		if err != nil {
			a.store.AddPluginLog(billing.PluginLogError, "Model catalog ignores credential rules: %v", err)
		} else {
			for i, id := range catalog.ids {
				keep[i] = keep[i] && provided[id]
			}
		}
	}
	body, listed := catalog.filter(keep)
	if body != nil {
		a.store.AddPluginLog(billing.PluginLogDebug, "Model catalog for %s lists %d of %d models", a.store.KeyDescription(scope), listed, len(keep))
	}
	return body
}

// Oh My CPA discovers models server-side, without a browser Referer.
func requestsFullCatalog(headers http.Header) bool {
	if strings.EqualFold(strings.TrimSpace(headers.Get(modelCatalogHeader)), "all") ||
		strings.HasPrefix(strings.TrimSpace(headers.Get("User-Agent")), "Oh-My-CPA/") {
		return true
	}
	referer, err := url.Parse(headers.Get("Referer"))
	return err == nil && strings.HasSuffix(referer.Path, "/management.html")
}

// CLIProxyAPI authenticates a request with the first of these headers that
// holds a configured key. Keys passed in the query string never reach the hook.
func requestKeyScopes(headers http.Header) []string {
	bearer := headers.Get("Authorization")
	if scheme, token, ok := strings.Cut(bearer, " "); ok && strings.EqualFold(scheme, "Bearer") {
		bearer = strings.TrimSpace(token)
	}
	var scopes []string
	for _, key := range []string{bearer, headers.Get("X-Goog-Api-Key"), headers.Get("X-Api-Key")} {
		if scope := billing.CallerScope(key); scope != "" {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

type modelCatalog struct {
	fields  map[string]json.RawMessage
	list    string
	entries []json.RawMessage
	ids     []string
}

// OpenAI, Grok and Claude catalogs list data[].id, Codex models[].slug, and
// Gemini models[].name. Anything else, or any entry without a model ID, is
// left alone.
func parseModelCatalog(body []byte) (modelCatalog, bool) {
	var catalog modelCatalog
	if json.Unmarshal(body, &catalog.fields) != nil {
		return catalog, false
	}
	for _, list := range []string{"data", "models"} {
		if raw, ok := catalog.fields[list]; ok {
			catalog.list = list
			if json.Unmarshal(raw, &catalog.entries) != nil {
				return catalog, false
			}
			break
		}
	}
	if catalog.list == "" {
		return catalog, false
	}
	catalog.ids = make([]string, len(catalog.entries))
	for i, entry := range catalog.entries {
		var model struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
			Name string `json:"name"`
		}
		if json.Unmarshal(entry, &model) != nil {
			return catalog, false
		}
		switch {
		case catalog.list == "data":
			catalog.ids[i] = uncloakedModelID(model.ID)
		case model.Slug != "":
			catalog.ids[i] = model.Slug
		default:
			catalog.ids[i] = strings.TrimPrefix(model.Name, "models/")
		}
		if catalog.ids[i] == "" {
			return catalog, false
		}
	}
	return catalog, true
}

// Claude catalogs disguise other providers' models as claude-fable-5-dd-
// followed by the reversed model ID.
func uncloakedModelID(id string) string {
	encoded, ok := strings.CutPrefix(id, claudeCloakedModelPrefix)
	if !ok || encoded == "" {
		return id
	}
	runes := []rune(encoded)
	slices.Reverse(runes)
	return string(runes)
}

// filter drops the entries keep rejects and returns the new body, which is nil
// when every entry stays. Claude catalogs also name their first and last IDs.
func (c modelCatalog) filter(keep []bool) (body []byte, listed int) {
	kept := make([]json.RawMessage, 0, len(c.entries))
	for i, entry := range c.entries {
		if keep[i] {
			kept = append(kept, entry)
		}
	}
	if len(kept) == len(c.entries) {
		return nil, len(kept)
	}
	fields := make(map[string]any, len(c.fields))
	for name, value := range c.fields {
		fields[name] = value
	}
	fields[c.list] = kept
	if _, claude := c.fields["first_id"]; claude {
		fields["first_id"], fields["last_id"] = "", ""
		if len(kept) > 0 {
			fields["first_id"], fields["last_id"] = catalogEntryID(kept[0]), catalogEntryID(kept[len(kept)-1])
		}
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(fields) != nil {
		return nil, len(c.entries)
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), len(kept)
}

func catalogEntryID(entry json.RawMessage) string {
	var model struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(entry, &model)
	return model.ID
}

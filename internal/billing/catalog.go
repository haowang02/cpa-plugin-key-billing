package billing

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ModelsDevCatalogURL = "https://models.dev/catalog.json"
	catalogCacheName    = "cpa-key-billing-models-dev-catalog-v1.json"
	maxCatalogBytes     = 128 << 20
	catalogHTTPTimeout  = 30 * time.Second
	catalogRetryDelay   = 30 * time.Second
	catalogCacheEnv     = "CPA_KEY_BILLING_CATALOG_CACHE"
	catalogSourceEnv    = "CPA_KEY_BILLING_CATALOG_URL"
)

type sourceCatalog struct {
	Models    map[string]json.RawMessage `json:"models"`
	Providers map[string]sourceProvider  `json:"providers"`
}

type sourceProvider struct {
	ID     string                     `json:"id"`
	Models map[string]json.RawMessage `json:"models"`
}

type sourceModel struct {
	ID   string      `json:"id"`
	Cost *sourceCost `json:"cost"`
}

type sourceCost struct {
	Input       *float64         `json:"input"`
	Output      *float64         `json:"output"`
	Reasoning   *float64         `json:"reasoning"`
	CacheRead   *float64         `json:"cache_read"`
	CacheWrite  *float64         `json:"cache_write"`
	InputAudio  *float64         `json:"input_audio"`
	OutputAudio *float64         `json:"output_audio"`
	Tiers       []sourceCostTier `json:"tiers"`
}

type sourceCostTier struct {
	Input       *float64 `json:"input"`
	Output      *float64 `json:"output"`
	Reasoning   *float64 `json:"reasoning"`
	CacheRead   *float64 `json:"cache_read"`
	CacheWrite  *float64 `json:"cache_write"`
	InputAudio  *float64 `json:"input_audio"`
	OutputAudio *float64 `json:"output_audio"`
	Tier        struct {
		Type string `json:"type"`
		Size int64  `json:"size"`
	} `json:"tier"`
}

type catalog struct {
	info   CatalogInfo
	rules  []PriceRule
	byName map[string]*PriceRule
}

type CatalogInfo struct {
	Source    string    `json:"source"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	Models    int       `json:"models"`
	CacheFile string    `json:"cache_file"`
}

var runtimeCatalog struct {
	sync.Mutex
	loaded     *catalog
	retryAfter time.Time
	lastError  error
}

func CatalogCachePath() string {
	if override := strings.TrimSpace(os.Getenv(catalogCacheEnv)); override != "" {
		return override
	}
	return filepath.Join(os.TempDir(), catalogCacheName)
}

func catalogSourceURL() string {
	if override := strings.TrimSpace(os.Getenv(catalogSourceEnv)); override != "" {
		return override
	}
	return ModelsDevCatalogURL
}

// EnsureBuiltinCatalog loads the process cache, reuses the system temporary
// file when present, or downloads it on first need.
func EnsureBuiltinCatalog() (CatalogInfo, error) {
	loaded, errLoad := loadBuiltinCatalog(false)
	if errLoad != nil {
		return CatalogInfo{}, errLoad
	}
	return loaded.info, nil
}

// RefreshBuiltinCatalog always downloads a fresh models.dev document and
// atomically replaces the system temporary cache.
func RefreshBuiltinCatalog() (CatalogInfo, error) {
	loaded, errLoad := loadBuiltinCatalog(true)
	if errLoad != nil {
		return CatalogInfo{}, errLoad
	}
	return loaded.info, nil
}

func cachedBuiltinCatalog() *catalog {
	runtimeCatalog.Lock()
	defer runtimeCatalog.Unlock()
	if runtimeCatalog.loaded != nil {
		return runtimeCatalog.loaded
	}
	path := CatalogCachePath()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil
	}
	parsed, errParse := parseSourceCatalog(raw, catalogFileTime(path))
	if errParse != nil {
		return nil
	}
	runtimeCatalog.loaded = parsed
	return parsed
}

func loadBuiltinCatalog(force bool) (*catalog, error) {
	runtimeCatalog.Lock()
	defer runtimeCatalog.Unlock()
	now := time.Now()
	if !force && runtimeCatalog.loaded != nil {
		if _, errStat := os.Stat(CatalogCachePath()); errStat == nil {
			return runtimeCatalog.loaded, nil
		}
		// The operating system or an operator removed the cache. Drop the
		// in-memory copy too so this need triggers a fresh download.
		runtimeCatalog.loaded = nil
	}

	path := CatalogCachePath()
	if !force {
		if raw, errRead := os.ReadFile(path); errRead == nil {
			if parsed, errParse := parseSourceCatalog(raw, catalogFileTime(path)); errParse == nil {
				runtimeCatalog.loaded = parsed
				runtimeCatalog.retryAfter = time.Time{}
				runtimeCatalog.lastError = nil
				return parsed, nil
			}
		}
		if runtimeCatalog.lastError != nil && now.Before(runtimeCatalog.retryAfter) {
			return nil, runtimeCatalog.lastError
		}
	}

	raw, errDownload := downloadCatalog()
	if errDownload != nil {
		rememberCatalogFailure(errDownload, now)
		return nil, errDownload
	}
	fetchedAt := now.UTC()
	parsed, errParse := parseSourceCatalog(raw, fetchedAt)
	if errParse != nil {
		errParse = fmt.Errorf("解析 models.dev 价格目录：%w", errParse)
		rememberCatalogFailure(errParse, now)
		return nil, errParse
	}
	if errWrite := writeFileAtomic(path, raw); errWrite != nil {
		errWrite = fmt.Errorf("缓存 models.dev 价格目录：%w", errWrite)
		rememberCatalogFailure(errWrite, now)
		return nil, errWrite
	}
	parsed.info.FetchedAt = fetchedAt
	runtimeCatalog.loaded = parsed
	runtimeCatalog.retryAfter = time.Time{}
	runtimeCatalog.lastError = nil
	return parsed, nil
}

// rememberCatalogFailure prevents a temporary models.dev outage from making
// every request wait for the same network timeout. A manual refresh bypasses
// this short cooldown.
func rememberCatalogFailure(err error, now time.Time) {
	runtimeCatalog.lastError = err
	runtimeCatalog.retryAfter = now.Add(catalogRetryDelay)
}

func downloadCatalog() ([]byte, error) {
	transport := &http.Transport{DisableKeepAlives: true}
	client := &http.Client{Timeout: catalogHTTPTimeout, Transport: transport}
	defer transport.CloseIdleConnections()
	sourceURL := catalogSourceURL()
	resp, errGet := client.Get(sourceURL)
	if errGet != nil {
		return nil, fmt.Errorf("下载 models.dev 价格目录：%w", errGet)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载 models.dev 价格目录：HTTP %s", resp.Status)
	}
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	if errRead != nil {
		return nil, fmt.Errorf("读取 models.dev 价格目录：%w", errRead)
	}
	if len(raw) > maxCatalogBytes {
		return nil, fmt.Errorf("models.dev 价格目录超过 %d MiB 限制", maxCatalogBytes>>20)
	}
	return raw, nil
}

func catalogFileTime(path string) time.Time {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return time.Time{}
	}
	return info.ModTime().UTC()
}

func parseSourceCatalog(raw []byte, fetchedAt time.Time) (*catalog, error) {
	var source sourceCatalog
	if errUnmarshal := json.Unmarshal(raw, &source); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	canonicalModels := make(map[string]struct{}, len(source.Models))
	for name := range source.Models {
		canonicalModels[normalizeCatalogID(name, "")] = struct{}{}
	}

	rules := make(map[string]PriceRule)
	aliases := make(map[string][]catalogAliasCandidate)
	blockedAliases := make(map[string]struct{})
	for providerKey, provider := range source.Providers {
		providerID := normalizeCatalogID(provider.ID, providerKey)
		for modelKey, rawModel := range provider.Models {
			var model sourceModel
			if json.Unmarshal(rawModel, &model) != nil || model.Cost == nil || model.Cost.Input == nil || model.Cost.Output == nil {
				continue
			}
			modelID := normalizeCatalogID(model.ID, modelKey)
			if providerID == "" || modelID == "" {
				continue
			}
			fullID := providerID + "/" + modelID
			_, canonical := canonicalModels[fullID]
			alias := catalogModelSuffix(modelID)
			if !supportedTokenCost(model.Cost) {
				if canonical {
					blockedAliases[alias] = struct{}{}
				}
				continue
			}
			rule := priceRuleFromSource(fullID, model.Cost)
			rules[fullID] = rule
			aliases[alias] = append(aliases[alias], catalogAliasCandidate{rule: rule, canonical: canonical})
		}
	}
	for alias, candidates := range aliases {
		if alias == "" {
			continue
		}
		if _, blocked := blockedAliases[alias]; blocked {
			continue
		}
		if rule, ok := safeCatalogAlias(candidates); ok {
			rule.Pattern = alias
			rules[alias] = rule
		}
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("models.dev 目录中没有可用的 Token 价格")
	}

	names := make([]string, 0, len(rules))
	for name := range rules {
		names = append(names, name)
	}
	sort.Strings(names)
	parsed := &catalog{
		info:   CatalogInfo{Source: catalogSourceURL(), FetchedAt: fetchedAt, Models: len(names), CacheFile: CatalogCachePath()},
		rules:  make([]PriceRule, 0, len(names)),
		byName: make(map[string]*PriceRule, len(names)),
	}
	for _, name := range names {
		parsed.rules = append(parsed.rules, rules[name])
	}
	for i := range parsed.rules {
		parsed.byName[parsed.rules[i].Pattern] = &parsed.rules[i]
	}
	return parsed, nil
}

func normalizeCatalogID(id, fallback string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id != "" {
		return id
	}
	return strings.ToLower(strings.TrimSpace(fallback))
}

func catalogModelSuffix(id string) string {
	if slash := strings.LastIndex(id, "/"); slash >= 0 {
		return id[slash+1:]
	}
	return id
}

func priceRuleFromSource(pattern string, value *sourceCost) PriceRule {
	rule := PriceRule{
		Pattern:         pattern,
		InputPer1M:      *value.Input,
		OutputPer1M:     *value.Output,
		CacheReadPer1M:  value.CacheRead,
		CacheWritePer1M: value.CacheWrite,
	}
	var contextTiers []sourceCostTier
	for _, tier := range value.Tiers {
		if tier.Tier.Type == "context" && tier.Tier.Size > 0 && tier.Input != nil && tier.Output != nil {
			contextTiers = append(contextTiers, tier)
		}
	}
	if len(contextTiers) == 1 {
		tier := contextTiers[0]
		rule.LongContext = &LongContextPrice{
			ThresholdInputTokens: tier.Tier.Size,
			InputPer1M:           *tier.Input,
			OutputPer1M:          *tier.Output,
			CacheReadPer1M:       tier.CacheRead,
			CacheWritePer1M:      tier.CacheWrite,
		}
	}
	return rule
}

func supportedTokenCost(value *sourceCost) bool {
	if !optionalPriceMatches(value.Reasoning, value.Output) ||
		!optionalPriceMatches(value.InputAudio, value.Input) ||
		!optionalPriceMatches(value.OutputAudio, value.Output) {
		return false
	}
	contextTiers := 0
	for _, tier := range value.Tiers {
		// The editor intentionally supports one long-context threshold. Never
		// flatten a multi-threshold or unknown tier into the base rate: an
		// unpriced row is safer and visible to the operator.
		if tier.Tier.Type != "context" || tier.Tier.Size <= 0 || tier.Input == nil || tier.Output == nil {
			return false
		}
		contextTiers++
		if contextTiers > 1 {
			return false
		}
		if !optionalPriceMatches(tier.Reasoning, tier.Output) ||
			!optionalPriceMatches(tier.InputAudio, tier.Input) ||
			!optionalPriceMatches(tier.OutputAudio, tier.Output) {
			return false
		}
	}
	return true
}

func optionalPriceMatches(special, standard *float64) bool {
	return special == nil || (standard != nil && *special == *standard)
}

type catalogAliasCandidate struct {
	rule      PriceRule
	canonical bool
}

func safeCatalogAlias(candidates []catalogAliasCandidate) (PriceRule, bool) {
	if len(candidates) == 0 {
		return PriceRule{}, false
	}
	canonical := make([]catalogAliasCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.canonical {
			canonical = append(canonical, candidate)
		}
	}
	if len(canonical) > 0 {
		candidates = canonical
	} else {
		paid := make([]catalogAliasCandidate, 0, len(candidates))
		for _, candidate := range candidates {
			if !freeCatalogPrice(candidate.rule) {
				paid = append(paid, candidate)
			}
		}
		if len(paid) > 0 {
			candidates = paid
		}
	}
	first := candidates[0].rule
	for _, candidate := range candidates[1:] {
		if !samePrice(first, candidate.rule) {
			return PriceRule{}, false
		}
	}
	return first, true
}

func freeCatalogPrice(rule PriceRule) bool {
	return rule.InputPer1M == 0 && rule.OutputPer1M == 0 &&
		(rule.CacheReadPer1M == nil || *rule.CacheReadPer1M == 0) &&
		(rule.CacheWritePer1M == nil || *rule.CacheWritePer1M == 0)
}

func builtinCatalog() *catalog {
	loaded, errLoad := loadBuiltinCatalog(false)
	if errLoad != nil {
		return &catalog{info: CatalogInfo{Source: catalogSourceURL(), CacheFile: CatalogCachePath()}, byName: map[string]*PriceRule{}}
	}
	return loaded
}

func SearchCatalog(query string, limit int) []PriceRule {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return []PriceRule{}
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	groups := [3][]PriceRule{}
	for _, rule := range builtinCatalog().rules {
		name := strings.ToLower(rule.Pattern)
		group := -1
		switch {
		case name == query:
			group = 0
		case strings.HasPrefix(name, query):
			group = 1
		case strings.Contains(name, query):
			group = 2
		}
		if group >= 0 && len(groups[group]) < limit {
			groups[group] = append(groups[group], rule)
		}
	}
	results := make([]PriceRule, 0, limit)
	for _, group := range groups {
		for _, rule := range group {
			results = append(results, rule)
			if len(results) == limit {
				return results
			}
		}
	}
	return results
}

func lookupBuiltin(model, alias string) (PriceRule, string, bool) {
	return lookupCatalog(builtinCatalog(), model, alias)
}

func lookupCatalog(loaded *catalog, model, alias string) (PriceRule, string, bool) {
	if loaded == nil {
		return PriceRule{}, "", false
	}
	candidates := [...]struct{ value, label string }{{model, "model"}, {alias, "alias"}}
	for _, candidate := range candidates {
		name := strings.ToLower(strings.TrimSpace(candidate.value))
		if name != "" {
			if rule := loaded.byName[name]; rule != nil {
				return *rule, candidate.label, true
			}
		}
	}
	for _, candidate := range candidates {
		name := strings.ToLower(strings.TrimSpace(candidate.value))
		if slash := strings.LastIndex(name, "/"); slash >= 0 && slash < len(name)-1 {
			if rule := loaded.byName[name[slash+1:]]; rule != nil {
				return *rule, candidate.label + "-suffix", true
			}
		}
	}
	return PriceRule{}, "", false
}

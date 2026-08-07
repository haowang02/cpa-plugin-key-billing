package billing

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// catalogRaw is the distilled public price table, generated immediately before
// compilation and embedded in the resulting plugin. Runtime billing therefore
// needs no outbound internet access, while the source repository does not need
// to carry a static catalog snapshot.
//
//go:embed catalog_data.json
var catalogRaw []byte

// catalogDocument is the generated file's shape. Each price row is
// [input, output, cache read, cache write] in USD per 1,000,000 tokens, with
// null for a price the provider does not publish.
type catalogDocument struct {
	Source    string                `json:"source"`
	FetchedAt string                `json:"fetched_at"`
	Count     int                   `json:"count"`
	Models    map[string][]*float64 `json:"models"`
}

// catalog is the parsed, indexed built-in price table.
type catalog struct {
	info CatalogInfo
	// rules is the whole table, sorted by pattern for stable presentation.
	rules []PriceRule
	// byName indexes rules under the catalog's own key.
	byName map[string]*PriceRule
}

// CatalogInfo describes the embedded price table.
type CatalogInfo struct {
	Source    string `json:"source"`
	FetchedAt string `json:"fetched_at"`
	Models    int    `json:"models"`
}

var (
	catalogOnce   sync.Once
	loadedCatalog *catalog
)

func builtinCatalog() *catalog {
	catalogOnce.Do(func() {
		loadedCatalog = parseCatalog(catalogRaw)
	})
	return loadedCatalog
}

func parseCatalog(raw []byte) *catalog {
	parsed := &catalog{byName: map[string]*PriceRule{}}

	var doc catalogDocument
	if errUnmarshal := json.Unmarshal(raw, &doc); errUnmarshal != nil {
		// A corrupt embedded catalog must not take the plugin down: every model
		// simply resolves to "no price", which the /status counters surface.
		parsed.info = CatalogInfo{Source: "unavailable"}
		return parsed
	}
	parsed.info = CatalogInfo{Source: doc.Source, FetchedAt: doc.FetchedAt}

	names := make([]string, 0, len(doc.Models))
	for name := range doc.Models {
		names = append(names, name)
	}
	sort.Strings(names)

	parsed.rules = make([]PriceRule, 0, len(names))
	for _, name := range names {
		prices := doc.Models[name]
		if len(prices) < 2 {
			continue
		}
		rule := PriceRule{Pattern: name}
		if prices[0] != nil {
			rule.InputPer1M = *prices[0]
		}
		if prices[1] != nil {
			rule.OutputPer1M = *prices[1]
		}
		if len(prices) > 2 {
			rule.CacheReadPer1M = prices[2]
		}
		if len(prices) > 3 {
			rule.CacheWritePer1M = prices[3]
		}
		parsed.rules = append(parsed.rules, rule)
	}
	parsed.info.Models = len(parsed.rules)

	for i := range parsed.rules {
		parsed.byName[parsed.rules[i].Pattern] = &parsed.rules[i]
	}
	return parsed
}

// BuiltinCatalog describes the embedded price table.
func BuiltinCatalog() CatalogInfo {
	return builtinCatalog().info
}

// CatalogDefault returns the published price for a model, if the catalog knows
// it. The returned rule carries the caller's spelling of the name, since that
// is what the price table is keyed by.
func CatalogDefault(model string) (PriceRule, bool) {
	rule, _, ok := lookupBuiltin(model, "")
	if !ok {
		return PriceRule{Pattern: strings.TrimSpace(model)}, false
	}
	rule.Pattern = strings.TrimSpace(model)
	return rule, true
}

// lookupBuiltin resolves a model or alias against the catalog. It tries exact
// catalog keys first for both names, then a safe bare-name alias generated from
// models.dev's canonical model map.
func lookupBuiltin(model, alias string) (PriceRule, string, bool) {
	loaded := builtinCatalog()
	candidates := [...]struct {
		value string
		label string
	}{
		{model, "model"},
		{alias, "alias"},
	}
	for _, candidate := range candidates {
		name := strings.ToLower(strings.TrimSpace(candidate.value))
		if name == "" {
			continue
		}
		if rule := loaded.byName[name]; rule != nil {
			return *rule, candidate.label, true
		}
	}
	for _, candidate := range candidates {
		name := strings.ToLower(strings.TrimSpace(candidate.value))
		if name == "" {
			continue
		}
		if slash := strings.LastIndex(name, "/"); slash >= 0 && slash < len(name)-1 {
			// The request carried a prefixed name; try its bare form.
			if rule := loaded.byName[name[slash+1:]]; rule != nil {
				return *rule, candidate.label + "-suffix", true
			}
		}
	}
	return PriceRule{}, "", false
}

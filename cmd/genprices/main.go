// Command genprices distills models.dev's public model catalog into the
// compact catalog embedded in the plugin at build time.
//
// The catalog is embedded rather than fetched at runtime for three reasons: the
// proxy must keep billing correctly with no outbound internet access, prices
// must not change under a running deployment without an operator noticing, and
// a multi-megabyte document has no business being parsed inside a request path.
// GitHub Actions runs this generator before checks and release builds:
//
//	go run ./cmd/genprices -out internal/billing/catalog_data.json
//
// Prices are USD per 1,000,000 tokens. A missing cache price is written as null
// rather than 0, because "not published" must fall back to the input price.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const defaultSource = "https://models.dev/catalog.json"

// sourceCatalog combines provider-specific prices with models.dev's canonical
// model identities. The latter let us choose a safe default for bare model IDs
// when several providers publish different prices for the same model.
type sourceCatalog struct {
	Models    map[string]json.RawMessage `json:"models"`
	Providers map[string]provider        `json:"providers"`
}

// provider is the subset of a models.dev provider record the generator needs.
// RawMessage keeps one malformed model from invalidating the whole catalog.
type provider struct {
	ID     string                     `json:"id"`
	Models map[string]json.RawMessage `json:"models"`
}

// model is the subset of a models.dev model record this plugin needs.
type model struct {
	ID         string     `json:"id"`
	Modalities modalities `json:"modalities"`
	Cost       *cost      `json:"cost"`
}

type modalities struct {
	Output []string `json:"output"`
}

type cost struct {
	Input           *float64          `json:"input"`
	Output          *float64          `json:"output"`
	Reasoning       *float64          `json:"reasoning"`
	CacheRead       *float64          `json:"cache_read"`
	CacheWrite      *float64          `json:"cache_write"`
	InputAudio      *float64          `json:"input_audio"`
	OutputAudio     *float64          `json:"output_audio"`
	Tiers           []json.RawMessage `json:"tiers"`
	ContextOver200K json.RawMessage   `json:"context_over_200k"`
}

type aliasCandidate struct {
	prices    []*float64
	canonical bool
}

// document is the generated catalog. Prices are stored as a fixed 4-element
// array — input, output, cache read, cache write — which keeps the embedded
// file about a third of the size that named fields would cost.
type document struct {
	Source    string                `json:"source"`
	FetchedAt string                `json:"fetched_at"`
	Count     int                   `json:"count"`
	Models    map[string][]*float64 `json:"models"`
}

func main() {
	source := flag.String("url", defaultSource, "models.dev catalog URL")
	input := flag.String("file", "", "read the models.dev catalog from this file instead of the network")
	output := flag.String("out", "internal/billing/catalog_data.json", "generated catalog path")
	flag.Parse()

	if errRun := run(*source, *input, *output); errRun != nil {
		fmt.Fprintf(os.Stderr, "genprices: %v\n", errRun)
		os.Exit(1)
	}
}

func run(source, input, output string) error {
	raw, errLoad := load(source, input)
	if errLoad != nil {
		return errLoad
	}

	var table sourceCatalog
	if errUnmarshal := json.Unmarshal(raw, &table); errUnmarshal != nil {
		return fmt.Errorf("decode models.dev catalog: %w", errUnmarshal)
	}
	canonicalModels := make(map[string]struct{}, len(table.Models))
	for key := range table.Models {
		canonicalModels[normalizedID(key, "")] = struct{}{}
	}

	doc := document{
		Source:    source,
		FetchedAt: time.Now().UTC().Format("2006-01-02"),
		Models:    make(map[string][]*float64),
	}
	if input != "" {
		doc.Source = input
	}

	malformed := 0
	unsupported := 0
	aliases := make(map[string][]aliasCandidate)
	blockedAliases := make(map[string]struct{})
	for providerKey, providerRecord := range table.Providers {
		providerID := normalizedID(providerRecord.ID, providerKey)
		for modelKey, rawModel := range providerRecord.Models {
			var record model
			if errUnmarshal := json.Unmarshal(rawModel, &record); errUnmarshal != nil {
				malformed++
				continue
			}
			modelID := normalizedID(record.ID, modelKey)
			if providerID == "" || modelID == "" {
				malformed++
				continue
			}
			fullID := providerID + "/" + modelID
			_, canonical := canonicalModels[fullID]
			alias := modelSuffix(modelID)
			if record.Cost == nil || (record.Cost.Input == nil && record.Cost.Output == nil) || !hasTextOutput(record.Modalities) {
				continue
			}
			if !hasFlatTokenPricing(record.Cost) {
				unsupported++
				if canonical {
					blockedAliases[alias] = struct{}{}
				}
				continue
			}
			prices := []*float64{
				record.Cost.Input,
				record.Cost.Output,
				record.Cost.CacheRead,
				record.Cost.CacheWrite,
			}
			doc.Models[fullID] = prices
			aliases[alias] = append(aliases[alias], aliasCandidate{prices: prices, canonical: canonical})
		}
	}
	ambiguous := 0
	for alias, candidates := range aliases {
		if alias == "" {
			continue
		}
		if _, blocked := blockedAliases[alias]; blocked {
			continue
		}
		if prices, ok := safeAliasPrice(candidates); ok {
			doc.Models[alias] = prices
		} else {
			ambiguous++
		}
	}
	doc.Count = len(doc.Models)
	if doc.Count == 0 {
		return fmt.Errorf("no billable models found in %s", doc.Source)
	}

	encoded, errEncode := encode(doc)
	if errEncode != nil {
		return errEncode
	}
	if errWrite := os.WriteFile(output, encoded, 0o644); errWrite != nil {
		return fmt.Errorf("write %s: %w", output, errWrite)
	}
	fmt.Printf("genprices: wrote %d prices to %s (%d KiB; skipped %d malformed entries, %d unsupported price schemes, and %d ambiguous aliases)\n", doc.Count, output, len(encoded)/1024, malformed, unsupported, ambiguous)
	return nil
}

func normalizedID(id, fallback string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id != "" {
		return id
	}
	return strings.ToLower(strings.TrimSpace(fallback))
}

func modelSuffix(id string) string {
	if slash := strings.LastIndex(id, "/"); slash >= 0 {
		return id[slash+1:]
	}
	return id
}

// safeAliasPrice selects a price for a bare model ID without guessing between
// providers. Canonical (usually first-party) entries take precedence. If no
// canonical entry exists, every provider must publish the same price.
func safeAliasPrice(candidates []aliasCandidate) ([]*float64, bool) {
	if len(candidates) == 0 {
		return nil, false
	}
	canonical := make([]aliasCandidate, 0, 1)
	for _, candidate := range candidates {
		if candidate.canonical {
			canonical = append(canonical, candidate)
		}
	}
	if len(canonical) > 0 {
		candidates = canonical
	}
	first := candidates[0].prices
	for _, candidate := range candidates[1:] {
		if !samePrices(first, candidate.prices) {
			return nil, false
		}
	}
	return first, true
}

func samePrices(a, b []*float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] == nil || b[i] == nil {
			if a[i] != nil || b[i] != nil {
				return false
			}
			continue
		}
		if *a[i] != *b[i] {
			return false
		}
	}
	return true
}

func hasTextOutput(value modalities) bool {
	for _, output := range value.Output {
		if strings.EqualFold(strings.TrimSpace(output), "text") {
			return true
		}
	}
	return false
}

// hasFlatTokenPricing reports whether the plugin's input/output/cache price
// model can represent this models.dev record exactly. A separate reasoning or
// audio rate and context-dependent tiers would otherwise silently misprice
// usage, so those records remain unpriced until an administrator configures
// them explicitly.
func hasFlatTokenPricing(value *cost) bool {
	contextTier := strings.TrimSpace(string(value.ContextOver200K))
	if len(value.Tiers) > 0 || (contextTier != "" && contextTier != "null") {
		return false
	}
	return optionalPriceMatches(value.Reasoning, value.Output) &&
		optionalPriceMatches(value.InputAudio, value.Input) &&
		optionalPriceMatches(value.OutputAudio, value.Output)
}

func optionalPriceMatches(special, standard *float64) bool {
	return special == nil || (standard != nil && *special == *standard)
}

func load(source, input string) ([]byte, error) {
	if input != "" {
		raw, errRead := os.ReadFile(input)
		if errRead != nil {
			return nil, fmt.Errorf("read %s: %w", input, errRead)
		}
		return raw, nil
	}
	// A generator run is not a request path, so a timeout here is appropriate.
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, errGet := client.Get(source)
	if errGet != nil {
		return nil, fmt.Errorf("fetch %s: %w", source, errGet)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			fmt.Fprintf(os.Stderr, "genprices: close response body: %v\n", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: unexpected status %s", source, resp.Status)
	}
	raw, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("read %s: %w", source, errRead)
	}
	return raw, nil
}

// encode writes the document with one model per line and the keys sorted, so
// refreshing the catalog produces a reviewable diff instead of a reshuffle.
func encode(doc document) ([]byte, error) {
	names := make([]string, 0, len(doc.Models))
	for name := range doc.Models {
		names = append(names, name)
	}
	sort.Strings(names)

	var builder strings.Builder
	builder.WriteString("{\n")
	writeField(&builder, "source", doc.Source)
	writeField(&builder, "fetched_at", doc.FetchedAt)
	fmt.Fprintf(&builder, "  \"count\": %d,\n", doc.Count)
	builder.WriteString("  \"models\": {\n")
	for i, name := range names {
		encodedName, errName := json.Marshal(name)
		if errName != nil {
			return nil, fmt.Errorf("encode model name %q: %w", name, errName)
		}
		encodedPrices, errPrices := json.Marshal(doc.Models[name])
		if errPrices != nil {
			return nil, fmt.Errorf("encode prices for %q: %w", name, errPrices)
		}
		separator := ","
		if i == len(names)-1 {
			separator = ""
		}
		fmt.Fprintf(&builder, "    %s: %s%s\n", encodedName, encodedPrices, separator)
	}
	builder.WriteString("  }\n}\n")
	return []byte(builder.String()), nil
}

func writeField(builder *strings.Builder, name, value string) {
	encoded, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		encoded = []byte(`""`)
	}
	fmt.Fprintf(builder, "  %q: %s,\n", name, encoded)
}

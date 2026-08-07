package billing

import (
	"strings"
	"testing"
)

const unpricedModel = "cpa-key-billing-test-unpriced-model"

func TestEmbeddedCatalogIsUsable(t *testing.T) {
	info := BuiltinCatalog()
	rules := builtinCatalog().rules
	if info.Models < 500 || len(rules) != info.Models || info.FetchedAt == "" || !strings.Contains(info.Source, "models.dev") {
		t.Fatalf("catalog = %+v with %d rules", info, len(rules))
	}
	for _, model := range []string{"gpt-4o", "claude-sonnet-4-5-20250929", "gemini-2.0-flash"} {
		rule, known := CatalogDefault(model)
		if !known || rule.InputPer1M <= 0 || rule.OutputPer1M <= 0 {
			t.Fatalf("CatalogDefault(%q) = %+v, %v", model, rule, known)
		}
	}
	if _, known := CatalogDefault(unpricedModel); known {
		t.Fatalf("test sentinel %q unexpectedly has a catalog price", unpricedModel)
	}
}

func TestCatalogBareNameUsesCanonicalProviderPrice(t *testing.T) {
	bare, known := CatalogDefault("gpt-5.3-codex")
	canonical, canonicalKnown := CatalogDefault("openai/gpt-5.3-codex")
	if !known || !canonicalKnown || !samePrice(bare, canonical) {
		t.Fatalf("bare = %+v (%v), canonical = %+v (%v)", bare, known, canonical, canonicalKnown)
	}
}

func TestCatalogLookupUsesModelThenAlias(t *testing.T) {
	sample := builtinCatalog().rules[0]
	rule, matchedOn, ok := lookupBuiltin(strings.ToUpper(sample.Pattern), "")
	if !ok || rule.Pattern != sample.Pattern || matchedOn != "model" {
		t.Fatalf("lookup = %+v, %q, %v", rule, matchedOn, ok)
	}
	if _, matchedOn, ok = lookupBuiltin(unpricedModel, sample.Pattern); !ok || matchedOn != "alias" {
		t.Fatalf("alias lookup matched on %q, found=%v", matchedOn, ok)
	}
}

func TestPriceOverridesBeatTheCatalog(t *testing.T) {
	sample := builtinCatalog().rules[0]
	state := NewState()
	state.Prices = []PriceRule{{Pattern: "*", InputPer1M: 7}}
	price := state.ResolvePrice(sample.Pattern, "")
	if price.Source != PriceSourceOverride || price.InputPer1M != 7 {
		t.Fatalf("price = %+v, want the operator override", price)
	}
}

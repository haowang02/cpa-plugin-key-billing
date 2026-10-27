package billing

import (
	"fmt"
	"testing"
)

const unpricedModel = "cpa-key-billing-test-unpriced-model"

func TestReferencePricesCarriesSingleLongContextTier(t *testing.T) {
	rule, known := MatchReferencePrice("gpt-5.6-sol", fixtureReferencePrices(t))
	if !known || rule.LongContext == nil || rule.LongContext.ThresholdInputTokens != 272000 ||
		rule.LongContext.InputPer1M != 10 || rule.LongContext.OutputPer1M != 45 {
		t.Fatalf("tiered default = %+v, known=%v", rule, known)
	}
}

func TestParsedReferencePricesRejectsUnsupportedCosts(t *testing.T) {
	for _, cost := range []string{
		`{"input": 1}`,
		`{"input": 1, "output": 2, "cache_read": -1}`,
		`{"input": 1, "output": 2, "input_audio": 3}`,
		`{"input": 1, "output": 2, "tiers": [{"input": 3, "output": 4, "tier": {"type": "other", "size": 100}}]}`,
		`{"input": 1, "output": 2, "tiers": [{"input": 3, "output": 4, "tier": {"type": "context", "size": 0}}]}`,
		`{"input": 1, "output": 2, "tiers": [{"input": 3, "output": 4, "reasoning": 5, "tier": {"type": "context", "size": 100}}]}`,
		`{"input": 1, "output": 2, "tiers": [
            {"input": 3, "output": 4, "tier": {"type": "context", "size": 100}},
            {"input": 5, "output": 6, "tier": {"type": "context", "size": 200}}
        ]}`,
	} {
		raw := fmt.Sprintf(`{
            "models": {"maker/chat": {}},
            "providers": {
                "maker": {"models": {"chat": {"cost": %s}}},
                "reseller": {"models": {"chat": {"cost": {"input": 1, "output": 2}}}}
            }
        }`, cost)
		prices, err := parseModelsDevPrices([]byte(raw))
		if err != nil || len(prices) != 2 || !prices[0].IsCanonical || prices[0].PriceRates != nil {
			t.Fatalf("unsupported cost %s lost its canonical entry: %+v, %v", cost, prices, err)
		}
		if price, found := MatchReferencePrice("chat", prices); found {
			t.Fatalf("unsupported cost %s selected reseller rates: %+v", cost, price)
		}
	}
}

func fixtureReferencePrices(t *testing.T) []ReferencePrice {
	t.Helper()
	prices, err := parseModelsDevPrices(referencePricesJSON(t))
	if err != nil {
		t.Fatal(err)
	}
	return prices
}

func TestParsedReferencePricesPreservesProviderModelIdentity(t *testing.T) {
	raw := []byte(`{
        "models": {"maker/Family/Chat": {}, "maker/unsupported": {}},
        "providers": {
            "maker": {"id": "maker", "models": {
                "Family/Chat": {"id": "Family/Chat", "cost": {"input": 2, "output": 4}},
                "unsupported": {"id": "unsupported", "cost": {"input": 2, "output": 4, "reasoning": 9}},
                "priced": {"id": "priced", "cost": {"input": 0, "output": 0}},
                "unknown": {"id": "unknown"}
            }},
            "reseller": {"id": "reseller", "models": {
                "Family/Chat": {"id": "Family/Chat", "cost": {"input": 5, "output": 10}},
                "unsupported": {"id": "unsupported", "cost": {"input": 1, "output": 2}},
                "literal*": {"id": "literal*", "cost": {"input": 3, "output": 6}}
            }}
        }
    }`)
	prices, err := parseModelsDevPrices(raw)
	if err != nil || len(prices) != 7 {
		t.Fatalf("reference price rows=%+v err=%v", prices, err)
	}
	ids := map[string]bool{}
	for _, price := range prices {
		identity := price.ProviderID + "/" + price.ModelID
		if ids[identity] || price.ModelID == "Chat" || price.ModelID == "family/chat" {
			t.Fatalf("identity merged, rewritten or synthesized: %+v", price)
		}
		ids[identity] = true
	}
	for _, test := range []struct {
		model    string
		provider string
		found    bool
		input    float64
	}{
		{"Chat", "maker", true, 2},
		{"FAMILY/CHAT", "maker", true, 2},
		{"reseller/Family/Chat", "maker", true, 2},
		{"unsupported", "", false, 0},
		{"unknown", "", false, 0},
		{"priced", "maker", true, 0},
		{"literal-other", "", false, 0},
		{"literal*", "reseller", true, 3},
	} {
		price, found := MatchReferencePrice(test.model, prices)
		if found != test.found || (found && (price.ProviderID != test.provider || price.InputPer1M != test.input)) {
			t.Fatalf("lookup %q: %+v found=%t", test.model, price, found)
		}
	}
}

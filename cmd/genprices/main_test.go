package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunConvertsModelsDevCatalog(t *testing.T) {
	tempDir := t.TempDir()
	input := filepath.Join(tempDir, "catalog.json")
	output := filepath.Join(tempDir, "generated.json")
	raw := []byte(`{
  "models": {
    "example/model-one": {"id": "example/model-one"},
    "example/tiered": {"id": "example/tiered"}
  },
  "providers": {
    "Example": {
      "id": "Example",
      "models": {
        "model-one": {
          "id": "Model-One",
          "cost": {"input": 1.25, "output": 10, "cache_read": 0.125, "cache_write": 1.25}
        },
        "image-only": {
          "id": "image-only",
          "modalities": {"output": ["image"]},
          "cost": {"input": 2, "output": 12}
        },
        "unpriced": {
          "id": "unpriced"
        },
        "reasoning-priced": {
          "id": "reasoning-priced",
          "cost": {"input": 1, "output": 2, "reasoning": 8}
        },
        "audio-priced": {
          "id": "audio-priced",
          "cost": {"input": 1, "output": 2, "output_audio": 20}
        },
        "tiered": {
          "id": "tiered",
          "cost": {"input": 1, "output": 2, "tiers": [{"input": 4, "output": 8}]}
        },
        "broken": "not a model record"
      }
    },
    "Reseller": {
      "models": {
        "example/model-one": {
          "id": "example/model-one",
          "cost": {"input": 9, "output": 90}
        },
        "shared": {
          "id": "shared",
          "cost": {"input": 2, "output": 4}
        },
        "ambiguous": {
          "id": "ambiguous",
          "cost": {"input": 4, "output": 8}
        },
        "example/tiered": {
          "id": "example/tiered",
          "cost": {"input": 1, "output": 2}
        }
      }
    },
    "Other": {
      "models": {
        "shared": {
          "id": "shared",
          "cost": {"input": 2, "output": 4}
        },
        "ambiguous": {
          "id": "ambiguous",
          "cost": {"input": 5, "output": 8}
        }
      }
    },
    "Fallback": {
      "models": {
        "Fallback-Model": {
          "cost": {"output": 0}
        }
      }
    }
  }
}`)
	if errWrite := os.WriteFile(input, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	if errRun := run(defaultSource, input, output); errRun != nil {
		t.Fatalf("run() error = %v", errRun)
	}

	generated, errRead := os.ReadFile(output)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var got document
	if errUnmarshal := json.Unmarshal(generated, &got); errUnmarshal != nil {
		t.Fatalf("decode generated catalog: %v", errUnmarshal)
	}
	if got.Source != input || got.Count != 15 || len(got.Models) != 15 {
		t.Fatalf("document = source %q, count %d, models %+v", got.Source, got.Count, got.Models)
	}
	prices := got.Models["example/model-one"]
	if len(prices) != 4 || prices[0] == nil || *prices[0] != 1.25 || prices[1] == nil || *prices[1] != 10 || prices[2] == nil || *prices[2] != 0.125 || prices[3] == nil || *prices[3] != 1.25 {
		t.Fatalf("example/model-one prices = %v", prices)
	}
	fallback := got.Models["fallback/fallback-model"]
	if len(fallback) != 4 || fallback[0] != nil || fallback[1] == nil || *fallback[1] != 0 {
		t.Fatalf("fallback/fallback-model prices = %v", fallback)
	}
	if image := got.Models["image-only"]; len(image) != 4 || image[0] == nil || *image[0] != 2 || image[1] == nil || *image[1] != 12 {
		t.Fatalf("image-only bare alias prices = %v", image)
	}
	if got.Models["model-one"][0] == nil || *got.Models["model-one"][0] != 1.25 {
		t.Fatalf("canonical bare alias prices = %v", got.Models["model-one"])
	}
	if got.Models["shared"][0] == nil || *got.Models["shared"][0] != 2 {
		t.Fatalf("unanimous bare alias prices = %v", got.Models["shared"])
	}
	if _, included := got.Models["ambiguous"]; included {
		t.Fatal("ambiguous bare alias was included")
	}
	for _, unsupported := range []string{"reasoning-priced", "audio-priced"} {
		if _, included := got.Models["example/"+unsupported]; included {
			t.Fatalf("unsupported price scheme %q was included", unsupported)
		}
	}
	if tiered := got.Models["tiered"]; len(tiered) != 4 || tiered[0] == nil || *tiered[0] != 1 || tiered[1] == nil || *tiered[1] != 2 {
		t.Fatalf("tiered canonical model did not use its first-tier price: %v", tiered)
	}
	if _, included := got.Models["reseller/example/tiered"]; !included {
		t.Fatal("flat provider-specific price for tiered model was dropped")
	}
}

func TestSafeAliasPriceIgnoresFreeChannelWhenPaidPricesAgree(t *testing.T) {
	input, output := 1.75, 14.0
	zero := 0.0
	prices, ok := safeAliasPrice([]aliasCandidate{
		{prices: []*float64{&input, &output, nil, nil}},
		{prices: []*float64{&input, &output, nil, nil}},
		{prices: []*float64{&zero, &zero, nil, nil}},
	})
	if !ok || prices[0] == nil || *prices[0] != input || prices[1] == nil || *prices[1] != output {
		t.Fatalf("safeAliasPrice() = %v, %v", prices, ok)
	}
}

func TestDefaultSourceIsModelsDev(t *testing.T) {
	if defaultSource != "https://models.dev/catalog.json" {
		t.Fatalf("default source = %q", defaultSource)
	}
}

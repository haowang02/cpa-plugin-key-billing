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
          "modalities": {"output": ["TEXT"]},
          "cost": {"input": 1.25, "output": 10, "cache_read": 0.125, "cache_write": 1.25}
        },
        "image-only": {
          "id": "image-only",
          "modalities": {"output": ["image"]},
          "cost": {"input": 2, "output": 12}
        },
        "unpriced": {
          "id": "unpriced",
          "modalities": {"output": ["text"]}
        },
        "reasoning-priced": {
          "id": "reasoning-priced",
          "modalities": {"output": ["text"]},
          "cost": {"input": 1, "output": 2, "reasoning": 8}
        },
        "audio-priced": {
          "id": "audio-priced",
          "modalities": {"output": ["text", "audio"]},
          "cost": {"input": 1, "output": 2, "output_audio": 20}
        },
        "tiered": {
          "id": "tiered",
          "modalities": {"output": ["text"]},
          "cost": {"input": 1, "output": 2, "tiers": [{"input": 4, "output": 8}]}
        },
        "broken": "not a model record"
      }
    },
    "Reseller": {
      "models": {
        "example/model-one": {
          "id": "example/model-one",
          "modalities": {"output": ["text"]},
          "cost": {"input": 9, "output": 90}
        },
        "shared": {
          "id": "shared",
          "modalities": {"output": ["text"]},
          "cost": {"input": 2, "output": 4}
        },
        "ambiguous": {
          "id": "ambiguous",
          "modalities": {"output": ["text"]},
          "cost": {"input": 4, "output": 8}
        },
        "example/tiered": {
          "id": "example/tiered",
          "modalities": {"output": ["text"]},
          "cost": {"input": 1, "output": 2}
        }
      }
    },
    "Other": {
      "models": {
        "shared": {
          "id": "shared",
          "modalities": {"output": ["text"]},
          "cost": {"input": 2, "output": 4}
        },
        "ambiguous": {
          "id": "ambiguous",
          "modalities": {"output": ["text"]},
          "cost": {"input": 5, "output": 8}
        }
      }
    },
    "Fallback": {
      "models": {
        "Fallback-Model": {
          "modalities": {"output": ["text"]},
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
	if got.Source != input || got.Count != 11 || len(got.Models) != 11 {
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
	if _, included := got.Models["example/image-only"]; included {
		t.Fatal("image-only model was included")
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
	for _, unsupported := range []string{"reasoning-priced", "audio-priced", "tiered"} {
		if _, included := got.Models["example/"+unsupported]; included {
			t.Fatalf("unsupported price scheme %q was included", unsupported)
		}
	}
	if _, included := got.Models["tiered"]; included {
		t.Fatal("unsupported canonical model received a reseller bare alias")
	}
	if _, included := got.Models["reseller/example/tiered"]; !included {
		t.Fatal("flat provider-specific price for tiered model was dropped")
	}
}

func TestDefaultSourceIsModelsDev(t *testing.T) {
	if defaultSource != "https://models.dev/catalog.json" {
		t.Fatalf("default source = %q", defaultSource)
	}
}

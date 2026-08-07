package billing

import (
	"math"
	"testing"
)

const costEpsilon = 1e-12

func assertClose(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > costEpsilon {
		t.Fatalf("%s = %.12f, want %.12f", label, got, want)
	}
}

func floatPtr(v float64) *float64 { return &v }

// testPrice is used across the cost cases: 1.00 input, 2.00 output,
// 0.10 cache read, 1.25 cache write, all per 1M tokens.
var testPrice = Price{InputPer1M: 1, OutputPer1M: 2, CacheReadPer1M: 0.1, CacheWritePer1M: 1.25}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern string
		value   string
		want    bool
	}{
		{pattern: "gpt-5*", value: "gpt-5.5", want: true},
		{pattern: "gpt-5*", value: "gpt-4.1", want: false},
		// path.Match would fail this one because its '*' stops at '/'.
		{pattern: "*claude*", value: "anthropic/claude-sonnet-4", want: true},
		{pattern: "claude-?-opus", value: "claude-4-opus", want: true},
		{pattern: "claude-?-opus", value: "claude-45-opus", want: false},
		{pattern: "GPT-5*", value: "gpt-5.5", want: true},
	}
	for _, test := range tests {
		t.Run(test.pattern+"|"+test.value, func(t *testing.T) {
			if got := globMatch(test.pattern, test.value); got != test.want {
				t.Fatalf("globMatch(%q, %q) = %v, want %v", test.pattern, test.value, got, test.want)
			}
		})
	}
}

func TestResolvePricePrefersExactOverGlobAndModelOverAlias(t *testing.T) {
	state := NewState()
	state.Prices = []PriceRule{
		{Pattern: "gpt-*", InputPer1M: 9},
		{Pattern: "gpt-5.5", InputPer1M: 1},
		{Pattern: "my-alias", InputPer1M: 5},
	}

	price := state.ResolvePrice("gpt-5.5", "my-alias")
	if price.InputPer1M != 1 || price.MatchedOn != "model" {
		t.Fatalf("exact model rule did not win: %+v", price)
	}

	price = state.ResolvePrice("gpt-4.1", "my-alias")
	if price.InputPer1M != 5 || price.MatchedOn != "alias" {
		t.Fatalf("exact alias should beat a glob: %+v", price)
	}

	price = state.ResolvePrice("gpt-4.1", "unknown-alias")
	if price.InputPer1M != 9 || price.MatchedOn != "model-glob" {
		t.Fatalf("glob fallback did not apply: %+v", price)
	}
}

func TestResolvePriceReturnsNoneForUnknownModel(t *testing.T) {
	state := NewState()
	price := state.ResolvePrice("mystery-model", "")
	if price.Source != PriceSourceNone {
		t.Fatalf("Source = %q, want %q", price.Source, PriceSourceNone)
	}
	// An unpriced model must bill nothing at all.
	cost := ComputeCost(price, SemanticsSubset, TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	assertClose(t, "TotalUSD", cost.TotalUSD, 0)
}

func TestResolvePriceCacheFallsBackToInputWhenUnset(t *testing.T) {
	state := NewState()
	state.Prices = []PriceRule{{Pattern: "m", InputPer1M: 3, OutputPer1M: 6}}

	price := state.ResolvePrice("m", "")
	// Unset cache prices must not silently bill cache tokens at zero: a Claude
	// request can be almost entirely cache reads.
	if price.CacheReadPer1M != 3 || price.CacheWritePer1M != 3 {
		t.Fatalf("unset cache prices did not fall back to input: %+v", price)
	}
}

func TestResolvePriceExplicitZeroCacheMeansFree(t *testing.T) {
	state := NewState()
	state.Prices = []PriceRule{{
		Pattern:         "m",
		InputPer1M:      3,
		OutputPer1M:     6,
		CacheReadPer1M:  floatPtr(0),
		CacheWritePer1M: floatPtr(0),
	}}

	price := state.ResolvePrice("m", "")
	if price.CacheReadPer1M != 0 || price.CacheWritePer1M != 0 {
		t.Fatalf("explicit zero cache price was overridden: %+v", price)
	}
}

func TestComputeCostSubsetPeelsCacheOutOfInput(t *testing.T) {
	// OpenAI-style: cache is inside input, reasoning is inside output.
	usage := TokenUsage{
		InputTokens:         1000,
		CacheReadTokens:     400,
		CacheCreationTokens: 100,
		OutputTokens:        500,
		ReasoningTokens:     200,
	}
	cost := ComputeCost(testPrice, SemanticsSubset, usage)

	if cost.UncachedInputTokens != 500 || cost.CacheReadTokens != 400 || cost.CacheWriteTokens != 100 {
		t.Fatalf("input buckets = %+v", cost)
	}
	// Reasoning is already inside OutputTokens and must not be added again.
	if cost.BilledOutputTokens != 500 {
		t.Fatalf("BilledOutputTokens = %d, want 500", cost.BilledOutputTokens)
	}
	assertClose(t, "TotalUSD", cost.TotalUSD, 0.0005+0.00004+0.000125+0.001)
}

func TestComputeCostIndependentAddsCacheOnTopOfInput(t *testing.T) {
	// Anthropic-style: InputTokens excludes cache entirely.
	usage := TokenUsage{
		InputTokens:         1000,
		CacheReadTokens:     400,
		CacheCreationTokens: 100,
		OutputTokens:        500,
	}
	cost := ComputeCost(testPrice, SemanticsIndependent, usage)

	if cost.UncachedInputTokens != 1000 {
		t.Fatalf("UncachedInputTokens = %d, want the full 1000 (cache is reported separately)", cost.UncachedInputTokens)
	}
	if cost.CacheReadTokens != 400 || cost.CacheWriteTokens != 100 {
		t.Fatalf("cache buckets = %+v", cost)
	}
	assertClose(t, "TotalUSD", cost.TotalUSD, 0.001+0.00004+0.000125+0.001)
}

func TestComputeCostSeparateReasoningAddsReasoningToOutput(t *testing.T) {
	// Gemini-style: cache inside input, reasoning outside output.
	usage := TokenUsage{
		InputTokens:     1000,
		CacheReadTokens: 400,
		OutputTokens:    500,
		ReasoningTokens: 200,
	}
	cost := ComputeCost(testPrice, SemanticsSeparateReasoning, usage)

	if cost.UncachedInputTokens != 600 {
		t.Fatalf("UncachedInputTokens = %d, want 600", cost.UncachedInputTokens)
	}
	if cost.BilledOutputTokens != 700 {
		t.Fatalf("BilledOutputTokens = %d, want 700 (reasoning is charged on top)", cost.BilledOutputTokens)
	}
	assertClose(t, "TotalUSD", cost.TotalUSD, 0.0006+0.00004+0+0.0014)
}

func TestComputeCostFallsBackToAggregateCachedTokens(t *testing.T) {
	// Providers that report only the aggregate cached count must still get
	// their cache tokens priced at the cache rate.
	usage := TokenUsage{InputTokens: 1000, CachedTokens: 300, OutputTokens: 0}
	cost := ComputeCost(testPrice, SemanticsSubset, usage)
	if cost.CacheReadTokens != 300 || cost.UncachedInputTokens != 700 {
		t.Fatalf("cost = %+v, want 300 cache read and 700 uncached", cost)
	}
}

func TestComputeCostClampsCacheToReportedInput(t *testing.T) {
	// A provider reporting more cache than input must not inflate the bill.
	usage := TokenUsage{InputTokens: 100, CacheReadTokens: 400, CacheCreationTokens: 50}
	cost := ComputeCost(testPrice, SemanticsSubset, usage)

	if cost.CacheReadTokens != 100 || cost.CacheWriteTokens != 0 || cost.UncachedInputTokens != 0 {
		t.Fatalf("cost = %+v, want the cache buckets clamped to the 100 reported input tokens", cost)
	}
	total := cost.UncachedInputTokens + cost.CacheReadTokens + cost.CacheWriteTokens
	if total != 100 {
		t.Fatalf("billed input tokens = %d, want exactly the reported 100", total)
	}
}

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

func TestCustomPriceRejectsNonFiniteRates(t *testing.T) {
	infinite := math.Inf(1)
	for _, rule := range []CustomPrice{
		{ModelID: "m", PriceRates: PriceRates{InputPer1M: math.NaN()}},
		{ModelID: "m", PriceRates: PriceRates{OutputPer1M: infinite}},
		{ModelID: "m", PriceRates: PriceRates{CacheReadPer1M: &infinite}},
		{ModelID: "m", PriceRates: PriceRates{LongContext: &LongContextPrice{ThresholdInputTokens: 1, InputPer1M: infinite}}},
	} {
		if rule.Validate() == nil {
			t.Fatalf("invalid price accepted: %+v", rule)
		}
	}
}

func completeBreakdown(uncached, cacheRead, cacheWrite, output, reasoning int64) TokenBreakdown {
	return TokenBreakdown{
		Quality:     TokenAccountingComplete,
		TotalTokens: uncached + cacheRead + cacheWrite + output,
		Input: TokenInputBreakdown{
			TotalTokens:      uncached + cacheRead + cacheWrite,
			UncachedTokens:   uncached,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        output,
			NonReasoningTokens: output - reasoning,
			ReasoningTokens:    reasoning,
		},
	}
}

func TestCanonicalBreakdownValidation(t *testing.T) {
	valid := completeBreakdown(500, 400, 100, 500, 200)
	if !valid.Valid() || !valid.Billable() {
		t.Fatalf("valid breakdown rejected: %+v", valid)
	}
	invalid := valid
	invalid.Input.TotalTokens++
	if invalid.Valid() || invalid.Billable() {
		t.Fatalf("invalid breakdown accepted: %+v", invalid)
	}
	unclassified := TokenBreakdown{
		Quality:            TokenAccountingUnclassified,
		TotalTokens:        10,
		UnclassifiedTokens: 10,
	}
	if !unclassified.Valid() || unclassified.Billable() {
		t.Fatalf("unclassified breakdown state is wrong: %+v", unclassified)
	}
}

func TestComputeCostUsesCanonicalBucketsExactlyOnce(t *testing.T) {
	price := Price{InputPer1M: 1, OutputPer1M: 2, CacheReadPer1M: 0.1, CacheWritePer1M: 1.25}
	cost := ComputeCost(price, completeBreakdown(500, 400, 100, 500, 200))
	if cost.UncachedInputTokens != 500 || cost.CacheReadTokens != 400 || cost.CacheWriteTokens != 100 || cost.BilledOutputTokens != 500 {
		t.Fatalf("cost buckets = %+v", cost)
	}
	if cost.Tiered || cost.LongContext || cost.ThresholdInputTokens != 0 ||
		cost.AppliedInputPer1M != 1 || cost.AppliedOutputPer1M != 2 ||
		cost.AppliedCacheReadPer1M != 0.1 || cost.AppliedCacheWritePer1M != 1.25 {
		t.Fatalf("non-tiered applied pricing = %+v", cost)
	}
	assertClose(t, "Total", cost.Total, 0.0005+0.00004+0.000125+0.001)
}

func TestComputeCostAppliesLongContextRatesToWholeRequest(t *testing.T) {
	price := Price{
		InputPer1M: 5, OutputPer1M: 30, CacheReadPer1M: 0.5, CacheWritePer1M: 6.25,
		LongContext: &ResolvedLongContextPrice{
			ThresholdInputTokens: 272000,
			InputPer1M:           10, OutputPer1M: 45, CacheReadPer1M: 1, CacheWritePer1M: 12.5,
		},
	}
	short := ComputeCost(price, completeBreakdown(200000, 72000, 0, 20000, 0))
	if short.LongContext || !short.Tiered {
		t.Fatalf("threshold-equal request selected wrong tier: %+v", short)
	}
	assertClose(t, "short total", short.Total, 1+0.036+0.6)

	long := ComputeCost(price, completeBreakdown(228001, 72000, 0, 20000, 0))
	if !long.LongContext || long.ThresholdInputTokens != 272000 {
		t.Fatalf("long request selected wrong tier: %+v", long)
	}
	// The entire 300001 input and all output use long-context prices, not only
	// the single input token above the threshold.
	assertClose(t, "long total", long.Total, 2.28001+0.072+0.9)
	if long.AppliedInputPer1M != 10 || long.AppliedOutputPer1M != 45 {
		t.Fatalf("applied prices = %+v", long)
	}
}

func TestComputeCostRefusesInvalidOrUnclassifiedBreakdown(t *testing.T) {
	price := Price{InputPer1M: 1, OutputPer1M: 2, CacheReadPer1M: 0.1, CacheWritePer1M: 1.25}
	invalid := completeBreakdown(10, 0, 0, 5, 0)
	invalid.TotalTokens++
	if cost := ComputeCost(price, invalid); cost != (Cost{}) {
		t.Fatalf("invalid breakdown cost = %+v", cost)
	}
	unclassified := TokenBreakdown{Quality: TokenAccountingUnclassified, TotalTokens: 10, UnclassifiedTokens: 10}
	if cost := ComputeCost(price, unclassified); cost != (Cost{}) {
		t.Fatalf("unclassified breakdown cost = %+v", cost)
	}
}

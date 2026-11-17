package billing

import (
	"math"
	"reflect"
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
		{ModelID: "m", PriceRates: PriceRates{ImageInputPer1M: floatPtr(math.NaN())}},
		{ModelID: "m", PriceRates: PriceRates{ImageCacheReadPer1M: floatPtr(-1)}},
		{ModelID: "m", PriceRates: PriceRates{ImageOutputPer1M: &infinite}},
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
	for _, detail := range []TokenDetail{
		{CacheWrite5mTokens: int64Ptr(100), CacheWrite1hTokens: int64Ptr(1)},
		{ImageInputTokens: int64Ptr(501)},
		{ImageCacheReadTokens: int64Ptr(-1)},
		{ImageOutputTokens: int64Ptr(301)},
	} {
		invalid := valid
		invalid.Detail = detail
		if invalid.Valid() || invalid.Billable() {
			t.Fatalf("invalid subsets accepted: %+v", detail)
		}
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

func int64Ptr(value int64) *int64 { return &value }

func TestImageTokenPricing(t *testing.T) {
	price := ResolveBuiltinPrice("gpt-image-1.5")
	for _, test := range []struct {
		name      string
		breakdown TokenBreakdown
		detail    TokenDetail
		plain     bool
		amount    float64
	}{
		{
			name: "parent rates", breakdown: completeBreakdown(80, 0, 0, 1056, 0), plain: true,
			detail: TokenDetail{ImageInputTokens: int64Ptr(50), ImageCacheReadTokens: int64Ptr(0), ImageOutputTokens: int64Ptr(1056)},
			amount: (80*5 + 1056*10) / 1e6,
		},
		{
			name: "image generation", breakdown: completeBreakdown(80, 0, 0, 1056, 0),
			detail: TokenDetail{ImageInputTokens: int64Ptr(50), ImageCacheReadTokens: int64Ptr(0), ImageOutputTokens: int64Ptr(1056)},
			amount: (30*5 + 50*8 + 1056*32) / 1e6,
		},
		{
			name: "cached images and reasoning", breakdown: completeBreakdown(80, 40, 0, 10, 2),
			detail: TokenDetail{ImageInputTokens: int64Ptr(40), ImageCacheReadTokens: int64Ptr(30), ImageOutputTokens: int64Ptr(8)},
			amount: (40*5 + 40*8 + 10*1.25 + 30*2 + 2*10 + 8*32) / 1e6,
		},
		{
			name: "reported zero", breakdown: completeBreakdown(80, 0, 0, 10, 0),
			detail: TokenDetail{ImageInputTokens: int64Ptr(0), ImageCacheReadTokens: int64Ptr(0), ImageOutputTokens: int64Ptr(0)},
			amount: (80*5 + 10*10) / 1e6,
		},
		{name: "unreported", breakdown: completeBreakdown(80, 40, 0, 10, 0), amount: (80*5 + 40*1.25 + 10*10) / 1e6},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, b, want := price, test.breakdown, test.detail
			b.Detail = test.detail
			if test.plain {
				p.ImageInputPer1M, p.ImageCacheReadPer1M, p.ImageOutputPer1M = nil, nil, nil
				want = TokenDetail{}
			}
			cost := ComputeCost(p, b)
			if !reflect.DeepEqual(cost.ImageInputTokens, want.ImageInputTokens) ||
				!reflect.DeepEqual(cost.ImageCacheReadTokens, want.ImageCacheReadTokens) ||
				!reflect.DeepEqual(cost.ImageOutputTokens, want.ImageOutputTokens) {
				t.Fatalf("image counts = %+v", cost)
			}
			assertClose(t, "total", cost.Total, test.amount)
			if cost.UncachedInputTokens != b.Input.UncachedTokens || cost.CacheReadTokens != b.Input.CacheReadTokens || cost.BilledOutputTokens != b.Output.TotalTokens {
				t.Fatal("parent tokens changed")
			}
		})
	}
}

func TestTokenDetailRatesAfterTierSelection(t *testing.T) {
	rates := PriceRates{
		InputPer1M: 1, OutputPer1M: 2, CacheWritePer1M: floatPtr(1.25), ImageOutputPer1M: floatPtr(32),
		LongContext: &LongContextPrice{
			ThresholdInputTokens: 100, InputPer1M: 4, OutputPer1M: 8, CacheReadPer1M: floatPtr(.5), CacheWritePer1M: floatPtr(5),
		},
	}
	b := completeBreakdown(80, 40, 20, 10, 2)
	b.Detail = TokenDetail{
		ImageInputTokens: int64Ptr(40), ImageCacheReadTokens: int64Ptr(30), ImageOutputTokens: int64Ptr(8),
		CacheWrite5mTokens: int64Ptr(5), CacheWrite1hTokens: int64Ptr(15),
	}
	cost := ComputeCost(rates.resolve(PriceSourceCustom, USD), b)
	if !cost.LongContext || cost.AppliedImageInputPer1M != 4 || cost.AppliedImageCacheReadPer1M != .5 || cost.AppliedImageOutputPer1M != 32 || cost.AppliedCacheWrite1hPer1M != 8 {
		t.Fatalf("tier rates = %+v", cost)
	}
	assertClose(t, "cache write", cost.CacheWrite, (5*5+15*8)/1e6)
	assertClose(t, "total", cost.Total, (80*4+40*.5+5*5+15*8+2*8+8*32)/1e6)
	rates.ImageInputPer1M = floatPtr(9)
	rates.ImageCacheReadPer1M = floatPtr(3)
	explicit := ComputeCost(rates.resolve(PriceSourceCustom, USD), b)
	if explicit.AppliedImageInputPer1M != 9 || explicit.AppliedImageCacheReadPer1M != 3 {
		t.Fatal("explicit image rates used tier", explicit)
	}
	rates.ImageOutputPer1M = nil
	inheritedOutput := ComputeCost(rates.resolve(PriceSourceCustom, USD), b)
	if inheritedOutput.AppliedImageOutputPer1M != 8 {
		t.Fatal("output did not inherit tier", inheritedOutput)
	}
	short := b
	short.Input.UncachedTokens = 30
	short.Input.TotalTokens = 90
	short.TotalTokens = 100
	short.Detail.ImageInputTokens = int64Ptr(0)
	standard := ComputeCost(rates.resolve(PriceSourceCustom, USD), short)
	if standard.LongContext || standard.AppliedCacheWrite1hPer1M != 2 {
		t.Fatal("standard TTL rate", standard)
	}
}

func TestCacheWriteTTLReportedPresenceAndUnpriced(t *testing.T) {
	for _, detail := range []TokenDetail{
		{CacheWrite5mTokens: int64Ptr(20)}, {CacheWrite1hTokens: int64Ptr(20)},
		{CacheWrite5mTokens: int64Ptr(0), CacheWrite1hTokens: int64Ptr(0)},
		{CacheWrite5mTokens: int64Ptr(5), CacheWrite1hTokens: int64Ptr(10)},
	} {
		b := completeBreakdown(10, 0, 20, 2, 0)
		b.Detail = detail
		for _, p := range []Price{{Source: PriceSourceNone}, {CacheWritePer1M: 1.25}} {
			cost := ComputeCost(p, b)
			if !reflect.DeepEqual(cost.CacheWrite5mTokens, detail.CacheWrite5mTokens) || !reflect.DeepEqual(cost.CacheWrite1hTokens, detail.CacheWrite1hTokens) {
				t.Fatalf("lost TTL presence: %+v", cost)
			}
			hour := tokenCount(detail.CacheWrite1hTokens)
			assertClose(t, "TTL amount", cost.CacheWrite, (float64(20-hour)*p.CacheWritePer1M+float64(hour)*p.CacheWritePer1M*1.6)/1e6)
		}
	}
}

package billing

import (
	"math"
)

type TokenAccountingQuality string

const (
	TokenAccountingComplete     TokenAccountingQuality = "complete"
	TokenAccountingInconsistent TokenAccountingQuality = "inconsistent"
	TokenAccountingUnclassified TokenAccountingQuality = "unclassified"
)

type TokenInputBreakdown struct {
	TotalTokens      int64
	UncachedTokens   int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

type TokenOutputBreakdown struct {
	TotalTokens        int64
	NonReasoningTokens int64
	ReasoningTokens    int64
}

// Provider usage is normalized into these non-overlapping buckets before pricing.
type TokenBreakdown struct {
	Quality            TokenAccountingQuality
	TotalTokens        int64
	Input              TokenInputBreakdown
	Output             TokenOutputBreakdown
	UnclassifiedTokens int64
}

func (b TokenBreakdown) Valid() bool {
	switch b.Quality {
	case TokenAccountingComplete, TokenAccountingInconsistent, TokenAccountingUnclassified:
	default:
		return false
	}
	if b.TotalTokens < 0 || b.UnclassifiedTokens < 0 ||
		b.Input.TotalTokens < 0 || b.Input.UncachedTokens < 0 || b.Input.CacheReadTokens < 0 || b.Input.CacheWriteTokens < 0 ||
		b.Output.TotalTokens < 0 || b.Output.NonReasoningTokens < 0 || b.Output.ReasoningTokens < 0 {
		return false
	}
	if b.Input.TotalTokens != b.Input.UncachedTokens+b.Input.CacheReadTokens+b.Input.CacheWriteTokens ||
		b.Output.TotalTokens != b.Output.NonReasoningTokens+b.Output.ReasoningTokens ||
		b.TotalTokens != b.Input.TotalTokens+b.Output.TotalTokens+b.UnclassifiedTokens {
		return false
	}
	return b.Quality != TokenAccountingComplete || b.UnclassifiedTokens == 0
}

func (b TokenBreakdown) Billable() bool {
	return b.Valid() && b.Quality == TokenAccountingComplete
}

// PriceSource records where the applied price came from.
type PriceSource string

const (
	PriceSourceReference PriceSource = "reference"
	PriceSourceNone      PriceSource = "none"
	PriceSourceCustom    PriceSource = "custom"
	PriceSourceBuiltin   PriceSource = "builtin"
)

// PriceRates describes token prices per million tokens in the currency of the
// price that holds them. Nil cache rates inherit the input rate; configured
// cache rates are used as supplied.
type PriceRates struct {
	InputPer1M      float64           `json:"input_per_1m"`
	OutputPer1M     float64           `json:"output_per_1m"`
	CacheReadPer1M  *float64          `json:"cache_read_per_1m,omitempty"`
	CacheWritePer1M *float64          `json:"cache_write_per_1m,omitempty"`
	LongContext     *LongContextPrice `json:"long_context,omitempty"`
}

// Rates are per 1,000,000 tokens in Currency.
type Price struct {
	Currency        Currency                  `json:"currency"`
	InputPer1M      float64                   `json:"input_per_1m"`
	OutputPer1M     float64                   `json:"output_per_1m"`
	CacheReadPer1M  float64                   `json:"cache_read_per_1m"`
	CacheWritePer1M float64                   `json:"cache_write_per_1m"`
	Source          PriceSource               `json:"source"`
	LongContext     *ResolvedLongContextPrice `json:"long_context,omitempty"`
}

type ResolvedLongContextPrice struct {
	ThresholdInputTokens int64   `json:"threshold_input_tokens"`
	InputPer1M           float64 `json:"input_per_1m"`
	OutputPer1M          float64 `json:"output_per_1m"`
	CacheReadPer1M       float64 `json:"cache_read_per_1m"`
	CacheWritePer1M      float64 `json:"cache_write_per_1m"`
}

func (r PriceRates) resolve(source PriceSource, currency Currency) Price {
	price := Price{
		Currency:        currency,
		InputPer1M:      r.InputPer1M,
		OutputPer1M:     r.OutputPer1M,
		CacheReadPer1M:  r.InputPer1M,
		CacheWritePer1M: r.InputPer1M,
		Source:          source,
	}
	if r.CacheReadPer1M != nil {
		price.CacheReadPer1M = *r.CacheReadPer1M
	}
	if r.CacheWritePer1M != nil {
		price.CacheWritePer1M = *r.CacheWritePer1M
	}
	if tier := r.LongContext; tier != nil {
		resolved := &ResolvedLongContextPrice{
			ThresholdInputTokens: tier.ThresholdInputTokens,
			InputPer1M:           tier.InputPer1M,
			OutputPer1M:          tier.OutputPer1M,
			CacheReadPer1M:       tier.InputPer1M,
			CacheWritePer1M:      tier.InputPer1M,
		}
		if tier.CacheReadPer1M != nil {
			resolved.CacheReadPer1M = *tier.CacheReadPer1M
		}
		if tier.CacheWritePer1M != nil {
			resolved.CacheWritePer1M = *tier.CacheWritePer1M
		}
		price.LongContext = resolved
	}
	return price
}

const CodexFastModeMultiplier = 2.5

// Cost amounts and applied rates are in the currency of the price used.
type Cost struct {
	// Multiplier is the applied billing multiplier. Zero/omitted means 1x.
	// Applied rates and amounts already include it; token counts do not.
	Multiplier float64 `json:"multiplier,omitempty"`

	Currency      Currency `json:"currency"`
	Total         float64  `json:"total"`
	UncachedInput float64  `json:"uncached_input"`
	CacheRead     float64  `json:"cache_read"`
	CacheWrite    float64  `json:"cache_write"`
	Output        float64  `json:"output"`

	UncachedInputTokens int64 `json:"uncached_input_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheWriteTokens    int64 `json:"cache_write_tokens"`
	BilledOutputTokens  int64 `json:"billed_output_tokens"`

	Tiered                 bool    `json:"tiered,omitempty"`
	LongContext            bool    `json:"long_context,omitempty"`
	ThresholdInputTokens   int64   `json:"threshold_input_tokens,omitempty"`
	AppliedInputPer1M      float64 `json:"applied_input_per_1m,omitempty"`
	AppliedOutputPer1M     float64 `json:"applied_output_per_1m,omitempty"`
	AppliedCacheReadPer1M  float64 `json:"applied_cache_read_per_1m,omitempty"`
	AppliedCacheWritePer1M float64 `json:"applied_cache_write_per_1m,omitempty"`
}

// ComputeCost prices one already-normalized usage record. Invalid or
// unclassified records are deliberately not guessed and therefore cost zero.
func ComputeCost(price Price, breakdown TokenBreakdown) Cost {
	if !breakdown.Billable() {
		return Cost{Currency: price.Currency}
	}
	inputPrice := price.InputPer1M
	outputPrice := price.OutputPer1M
	cacheReadPrice := price.CacheReadPer1M
	cacheWritePrice := price.CacheWritePer1M
	longContext := false
	threshold := int64(0)
	if tier := price.LongContext; tier != nil {
		threshold = tier.ThresholdInputTokens
		if breakdown.Input.TotalTokens > threshold {
			longContext = true
			inputPrice = tier.InputPer1M
			outputPrice = tier.OutputPer1M
			cacheReadPrice = tier.CacheReadPer1M
			cacheWritePrice = tier.CacheWritePer1M
		}
	}
	cost := Cost{
		Currency:               price.Currency,
		UncachedInput:          perMillion(breakdown.Input.UncachedTokens, inputPrice),
		CacheRead:              perMillion(breakdown.Input.CacheReadTokens, cacheReadPrice),
		CacheWrite:             perMillion(breakdown.Input.CacheWriteTokens, cacheWritePrice),
		Output:                 perMillion(breakdown.Output.TotalTokens, outputPrice),
		UncachedInputTokens:    breakdown.Input.UncachedTokens,
		CacheReadTokens:        breakdown.Input.CacheReadTokens,
		CacheWriteTokens:       breakdown.Input.CacheWriteTokens,
		BilledOutputTokens:     breakdown.Output.TotalTokens,
		Tiered:                 price.LongContext != nil,
		LongContext:            longContext,
		ThresholdInputTokens:   threshold,
		AppliedInputPer1M:      inputPrice,
		AppliedOutputPer1M:     outputPrice,
		AppliedCacheReadPer1M:  cacheReadPrice,
		AppliedCacheWritePer1M: cacheWritePrice,
	}
	cost.Total = cost.UncachedInput + cost.CacheRead + cost.CacheWrite + cost.Output
	return cost
}

func (c *Cost) multiply(multiplier float64) {
	c.Multiplier = multiplier
	c.UncachedInput *= multiplier
	c.CacheRead *= multiplier
	c.CacheWrite *= multiplier
	c.Output *= multiplier
	c.Total = c.UncachedInput + c.CacheRead + c.CacheWrite + c.Output
	c.AppliedInputPer1M *= multiplier
	c.AppliedOutputPer1M *= multiplier
	c.AppliedCacheReadPer1M *= multiplier
	c.AppliedCacheWritePer1M *= multiplier
}

func perMillion(tokens int64, pricePer1M float64) float64 {
	if tokens <= 0 || pricePer1M == 0 {
		return 0
	}
	return float64(tokens) / 1_000_000 * pricePer1M
}

func samePriceRates(a, b PriceRates) bool {
	return a.InputPer1M == b.InputPer1M &&
		a.OutputPer1M == b.OutputPer1M &&
		sameOptionalPrice(a.CacheReadPer1M, b.CacheReadPer1M) &&
		sameOptionalPrice(a.CacheWritePer1M, b.CacheWritePer1M) &&
		sameLongContextPrice(a.LongContext, b.LongContext)
}

func sameLongContextPrice(a, b *LongContextPrice) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.ThresholdInputTokens == b.ThresholdInputTokens &&
		a.InputPer1M == b.InputPer1M && a.OutputPer1M == b.OutputPer1M &&
		sameOptionalPrice(a.CacheReadPer1M, b.CacheReadPer1M) &&
		sameOptionalPrice(a.CacheWritePer1M, b.CacheWritePer1M)
}

func sameOptionalPrice(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (r PriceRates) validate(modelID string) error {
	if invalidAmount(r.InputPer1M) || invalidAmount(r.OutputPer1M) {
		return invalidf("Model %q: token rates must be finite non-negative numbers", modelID)
	}
	if r.CacheReadPer1M != nil && invalidAmount(*r.CacheReadPer1M) {
		return invalidf("Model %q: cache read rate must be a finite non-negative number", modelID)
	}
	if r.CacheWritePer1M != nil && invalidAmount(*r.CacheWritePer1M) {
		return invalidf("Model %q: cache write rate must be a finite non-negative number", modelID)
	}
	if tier := r.LongContext; tier != nil {
		if tier.ThresholdInputTokens <= 0 {
			return invalidf("Model %q: long-context threshold must be greater than zero", modelID)
		}
		if invalidAmount(tier.InputPer1M) || invalidAmount(tier.OutputPer1M) {
			return invalidf("Model %q: long-context token rates must be finite non-negative numbers", modelID)
		}
		if tier.CacheReadPer1M != nil && invalidAmount(*tier.CacheReadPer1M) {
			return invalidf("Model %q: long-context cache read rate must be a finite non-negative number", modelID)
		}
		if tier.CacheWritePer1M != nil && invalidAmount(*tier.CacheWritePer1M) {
			return invalidf("Model %q: long-context cache write rate must be a finite non-negative number", modelID)
		}
	}
	return nil
}

func invalidAmount(value float64) bool {
	return value < 0 || math.IsNaN(value) || math.IsInf(value, 0)
}

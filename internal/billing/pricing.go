package billing

import "math"

type PriceSource string

const (
	PriceSourceReference PriceSource = "reference"
	PriceSourceNone      PriceSource = "none"
	PriceSourceCustom    PriceSource = "custom"
	PriceSourceBuiltin   PriceSource = "builtin"
)

// Rates are per million tokens in the price's currency; nil cache rates inherit the input rate.
type PriceRates struct {
	InputPer1M          float64           `json:"input_per_1m"`
	OutputPer1M         float64           `json:"output_per_1m"`
	CacheReadPer1M      *float64          `json:"cache_read_per_1m,omitempty"`
	CacheWritePer1M     *float64          `json:"cache_write_per_1m,omitempty"`
	ImageInputPer1M     *float64          `json:"image_input_per_1m,omitempty"`
	ImageCacheReadPer1M *float64          `json:"image_cache_read_per_1m,omitempty"`
	ImageOutputPer1M    *float64          `json:"image_output_per_1m,omitempty"`
	LongContext         *LongContextPrice `json:"long_context,omitempty"`
}

// Rates are per 1,000,000 tokens in Currency.
type Price struct {
	Currency            Currency                  `json:"currency"`
	InputPer1M          float64                   `json:"input_per_1m"`
	OutputPer1M         float64                   `json:"output_per_1m"`
	CacheReadPer1M      float64                   `json:"cache_read_per_1m"`
	CacheWritePer1M     float64                   `json:"cache_write_per_1m"`
	ImageInputPer1M     *float64                  `json:"image_input_per_1m,omitempty"`
	ImageCacheReadPer1M *float64                  `json:"image_cache_read_per_1m,omitempty"`
	ImageOutputPer1M    *float64                  `json:"image_output_per_1m,omitempty"`
	Source              PriceSource               `json:"source"`
	LongContext         *ResolvedLongContextPrice `json:"long_context,omitempty"`
}

type ResolvedLongContextPrice struct {
	ThresholdInputTokens int64   `json:"threshold_input_tokens"`
	InputPer1M           float64 `json:"input_per_1m"`
	OutputPer1M          float64 `json:"output_per_1m"`
	CacheReadPer1M       float64 `json:"cache_read_per_1m"`
	CacheWritePer1M      float64 `json:"cache_write_per_1m"`
}

const (
	CodexFastModeMultiplier    = 2.5
	CacheWrite1hRateMultiplier = 1.6
)

// Parent amounts and token counts include their details; rates use the price's currency.
type Cost struct {
	// Multiplier is the applied billing multiplier. Zero/omitted means 1x.
	// Applied rates and amounts already include it; token counts do not.
	Multiplier float64 `json:"multiplier,omitempty"`

	Currency       Currency `json:"currency"`
	Total          float64  `json:"total"`
	UncachedInput  float64  `json:"uncached_input"`
	CacheRead      float64  `json:"cache_read"`
	CacheWrite     float64  `json:"cache_write"`
	Output         float64  `json:"output"`
	CacheWrite1h   float64  `json:"cache_write_1h"`
	ImageInput     float64  `json:"image_input"`
	ImageCacheRead float64  `json:"image_cache_read"`
	ImageOutput    float64  `json:"image_output"`

	UncachedInputTokens  int64  `json:"uncached_input_tokens"`
	CacheReadTokens      int64  `json:"cache_read_tokens"`
	CacheWriteTokens     int64  `json:"cache_write_tokens"`
	BilledOutputTokens   int64  `json:"billed_output_tokens"`
	CacheWrite5mTokens   *int64 `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens   *int64 `json:"cache_write_1h_tokens"`
	ImageInputTokens     *int64 `json:"image_input_tokens"`
	ImageCacheReadTokens *int64 `json:"image_cache_read_tokens"`
	ImageOutputTokens    *int64 `json:"image_output_tokens"`

	Tiered                     bool    `json:"tiered,omitempty"`
	LongContext                bool    `json:"long_context,omitempty"`
	ThresholdInputTokens       int64   `json:"threshold_input_tokens,omitempty"`
	AppliedInputPer1M          float64 `json:"applied_input_per_1m,omitempty"`
	AppliedOutputPer1M         float64 `json:"applied_output_per_1m,omitempty"`
	AppliedCacheReadPer1M      float64 `json:"applied_cache_read_per_1m,omitempty"`
	AppliedCacheWritePer1M     float64 `json:"applied_cache_write_per_1m,omitempty"`
	AppliedCacheWrite1hPer1M   float64 `json:"applied_cache_write_1h_per_1m"`
	AppliedImageInputPer1M     float64 `json:"applied_image_input_per_1m"`
	AppliedImageCacheReadPer1M float64 `json:"applied_image_cache_read_per_1m"`
	AppliedImageOutputPer1M    float64 `json:"applied_image_output_per_1m"`
}

// Invalid or unclassified records are never guessed and cost zero.
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
	cost.CacheWrite5mTokens, cost.CacheWrite1hTokens = breakdown.Detail.CacheWrite5mTokens, breakdown.Detail.CacheWrite1hTokens
	if cost.CacheWrite1hTokens != nil {
		cost.AppliedCacheWrite1hPer1M = cacheWritePrice * CacheWrite1hRateMultiplier
	}
	if price.hasImageRates() {
		cost.ImageInputTokens, cost.ImageCacheReadTokens = breakdown.Detail.ImageInputTokens, breakdown.Detail.ImageCacheReadTokens
		cost.ImageOutputTokens = breakdown.Detail.ImageOutputTokens
		if cost.ImageInputTokens != nil {
			cost.AppliedImageInputPer1M = inheritedRate(price.ImageInputPer1M, inputPrice)
		}
		if cost.ImageCacheReadTokens != nil {
			cost.AppliedImageCacheReadPer1M = inheritedRate(price.ImageCacheReadPer1M, cacheReadPrice)
		}
		if cost.ImageOutputTokens != nil {
			cost.AppliedImageOutputPer1M = inheritedRate(price.ImageOutputPer1M, outputPrice)
		}
	}
	imageInput, imageRead, imageOutput := tokenCount(cost.ImageInputTokens), tokenCount(cost.ImageCacheReadTokens), tokenCount(cost.ImageOutputTokens)
	hour := tokenCount(cost.CacheWrite1hTokens)
	cost.ImageInput = perMillion(imageInput, cost.AppliedImageInputPer1M)
	cost.ImageCacheRead = perMillion(imageRead, cost.AppliedImageCacheReadPer1M)
	cost.ImageOutput = perMillion(imageOutput, cost.AppliedImageOutputPer1M)
	cost.CacheWrite1h = perMillion(hour, cost.AppliedCacheWrite1hPer1M)
	cost.UncachedInput = perMillion(cost.UncachedInputTokens-imageInput, inputPrice) + cost.ImageInput
	cost.CacheRead = perMillion(cost.CacheReadTokens-imageRead, cacheReadPrice) + cost.ImageCacheRead
	cost.CacheWrite = perMillion(cost.CacheWriteTokens-hour, cacheWritePrice) + cost.CacheWrite1h
	cost.Output = perMillion(cost.BilledOutputTokens-imageOutput, outputPrice) + cost.ImageOutput
	cost.Total = cost.UncachedInput + cost.CacheRead + cost.CacheWrite + cost.Output
	return cost
}

func (c *Cost) multiply(multiplier float64) {
	c.Multiplier = multiplier
	c.UncachedInput *= multiplier
	c.CacheRead *= multiplier
	c.CacheWrite *= multiplier
	c.Output *= multiplier
	c.CacheWrite1h *= multiplier
	c.ImageInput *= multiplier
	c.ImageCacheRead *= multiplier
	c.ImageOutput *= multiplier
	c.Total = c.UncachedInput + c.CacheRead + c.CacheWrite + c.Output
	c.AppliedInputPer1M *= multiplier
	c.AppliedOutputPer1M *= multiplier
	c.AppliedCacheReadPer1M *= multiplier
	c.AppliedCacheWritePer1M *= multiplier
	c.AppliedCacheWrite1hPer1M *= multiplier
	c.AppliedImageInputPer1M *= multiplier
	c.AppliedImageCacheReadPer1M *= multiplier
	c.AppliedImageOutputPer1M *= multiplier
}

func perMillion(tokens int64, pricePer1M float64) float64 {
	if tokens <= 0 || pricePer1M == 0 {
		return 0
	}
	return float64(tokens) / 1_000_000 * pricePer1M
}

func (r PriceRates) resolve(source PriceSource, currency Currency) Price {
	price := Price{
		Currency:            currency,
		InputPer1M:          r.InputPer1M,
		OutputPer1M:         r.OutputPer1M,
		CacheReadPer1M:      inheritedRate(r.CacheReadPer1M, r.InputPer1M),
		CacheWritePer1M:     inheritedRate(r.CacheWritePer1M, r.InputPer1M),
		ImageInputPer1M:     r.ImageInputPer1M,
		ImageCacheReadPer1M: r.ImageCacheReadPer1M,
		ImageOutputPer1M:    r.ImageOutputPer1M,
		Source:              source,
	}
	if tier := r.LongContext; tier != nil {
		resolved := &ResolvedLongContextPrice{
			ThresholdInputTokens: tier.ThresholdInputTokens,
			InputPer1M:           tier.InputPer1M,
			OutputPer1M:          tier.OutputPer1M,
			CacheReadPer1M:       inheritedRate(tier.CacheReadPer1M, tier.InputPer1M),
			CacheWritePer1M:      inheritedRate(tier.CacheWritePer1M, tier.InputPer1M),
		}
		price.LongContext = resolved
	}
	return price
}

func samePriceRates(a, b PriceRates) bool {
	return a.InputPer1M == b.InputPer1M &&
		a.OutputPer1M == b.OutputPer1M &&
		sameOptionalPrice(a.CacheReadPer1M, b.CacheReadPer1M) &&
		sameOptionalPrice(a.CacheWritePer1M, b.CacheWritePer1M) &&
		sameOptionalPrice(a.ImageInputPer1M, b.ImageInputPer1M) &&
		sameOptionalPrice(a.ImageCacheReadPer1M, b.ImageCacheReadPer1M) &&
		sameOptionalPrice(a.ImageOutputPer1M, b.ImageOutputPer1M) &&
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
	for _, rate := range []*float64{r.ImageInputPer1M, r.ImageCacheReadPer1M, r.ImageOutputPer1M} {
		if rate != nil && invalidAmount(*rate) {
			return invalidf("Model %q: token rates must be finite non-negative numbers", modelID)
		}
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

func (p Price) hasImageRates() bool {
	return p.ImageInputPer1M != nil || p.ImageCacheReadPer1M != nil || p.ImageOutputPer1M != nil
}

func inheritedRate(rate *float64, fallback float64) float64 {
	if rate != nil {
		return *rate
	}
	return fallback
}

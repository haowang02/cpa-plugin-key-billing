package billing

import "strings"

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

// Measured reports whether any upstream usage was observed. The zero value
// means none ever was, which is the normal shape of a canceled request: nothing
// to price, and nothing wrong with it either.
func (b TokenBreakdown) Measured() bool {
	return b.Quality != ""
}

// PriceSource distinguishes an explicit zero price from an unresolved price.
type PriceSource string

const (
	PriceSourceOverride PriceSource = "override"
	PriceSourceBuiltin  PriceSource = "builtin"
	PriceSourceNone     PriceSource = "none"
	PriceSourceCustom   PriceSource = "custom"
)

// Rates are USD per 1,000,000 tokens.
type Price struct {
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

func (r PriceRule) resolve(source PriceSource) Price {
	price := Price{
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

// ResolvePrice finds the price for one usage record. Route-specific prices win
// over upstream-model fallbacks; built-in prices use the opposite order because
// models.dev normally knows the upstream model rather than local route names.
func (s *State) ResolvePrice(upstreamModel, billingModel string) Price {
	baseUpstreamModel := modelWithoutSuffix(upstreamModel)
	if rule, ok := matchPriceRule(s.Prices, billingModel, baseUpstreamModel); ok {
		return rule.resolve(PriceSourceOverride)
	}
	if rule, ok := lookupBuiltin(baseUpstreamModel, billingModel); ok {
		return rule.resolve(PriceSourceBuiltin)
	}
	return Price{Source: PriceSourceNone}
}

// ResolveBillingModel returns the stable client-visible model used for pricing
// and aggregation. A trailing CPA thinking suffix is a request option unless an
// exact model row declares it as part of a configured model ID.
func (s *State) ResolveBillingModel(upstreamModel, routeModel string) string {
	upstreamModel = modelWithoutSuffix(upstreamModel)
	routeModel = strings.TrimSpace(routeModel)
	if routeModel == "" {
		return upstreamModel
	}

	base := modelWithoutSuffix(routeModel)
	if strings.EqualFold(base, "auto") {
		return upstreamModel
	}
	if model, ok := exactPriceModel(s.Prices, routeModel); ok {
		return model
	}
	if model, ok := exactPriceModel(s.Prices, base); ok {
		return model
	}
	if strings.EqualFold(base, upstreamModel) {
		return upstreamModel
	}
	return base
}

func modelWithoutSuffix(model string) string {
	model = strings.TrimSpace(model)
	if open := strings.LastIndex(model, "("); open >= 0 && strings.HasSuffix(model, ")") {
		return strings.TrimSpace(model[:open])
	}
	return model
}

func exactPriceModel(rules []PriceRule, model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	for _, rule := range rules {
		pattern := strings.TrimSpace(rule.Pattern)
		if !isGlob(pattern) && strings.EqualFold(pattern, model) {
			return pattern, true
		}
	}
	return "", false
}

func matchPriceRule(rules []PriceRule, billingModel, upstreamModel string) (PriceRule, bool) {
	billingModel = strings.TrimSpace(billingModel)
	upstreamModel = strings.TrimSpace(upstreamModel)

	for _, candidate := range []string{billingModel, upstreamModel} {
		if candidate == "" {
			continue
		}
		for _, rule := range rules {
			if !isGlob(rule.Pattern) && strings.EqualFold(strings.TrimSpace(rule.Pattern), candidate) {
				return rule, true
			}
		}
	}
	for _, candidate := range []string{billingModel, upstreamModel} {
		if candidate == "" {
			continue
		}
		for _, rule := range rules {
			if isGlob(rule.Pattern) && globMatch(strings.TrimSpace(rule.Pattern), candidate) {
				return rule, true
			}
		}
	}
	return PriceRule{}, false
}

func isGlob(pattern string) bool {
	return strings.ContainsAny(pattern, "*?")
}

// path.Match is not usable here: its '*' stops at '/', which would break
// patterns like "*claude*" against OpenRouter-style names such as
// "anthropic/claude-sonnet-4".
func globMatch(pattern, value string) bool {
	pattern = strings.ToLower(pattern)
	value = strings.ToLower(value)

	var (
		patternIndex, valueIndex int
		starIndex                = -1
		matchIndex               int
	)
	for valueIndex < len(value) {
		switch {
		case patternIndex < len(pattern) && (pattern[patternIndex] == '?' || pattern[patternIndex] == value[valueIndex]):
			patternIndex++
			valueIndex++
		case patternIndex < len(pattern) && pattern[patternIndex] == '*':
			starIndex = patternIndex
			matchIndex = valueIndex
			patternIndex++
		case starIndex >= 0:
			patternIndex = starIndex + 1
			matchIndex++
			valueIndex = matchIndex
		default:
			return false
		}
	}
	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pattern)
}

type Cost struct {
	TotalUSD         float64 `json:"total_usd"`
	UncachedInputUSD float64 `json:"uncached_input_usd"`
	CacheReadUSD     float64 `json:"cache_read_usd"`
	CacheWriteUSD    float64 `json:"cache_write_usd"`
	OutputUSD        float64 `json:"output_usd"`

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
		return Cost{}
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
		UncachedInputUSD:       perMillion(breakdown.Input.UncachedTokens, inputPrice),
		CacheReadUSD:           perMillion(breakdown.Input.CacheReadTokens, cacheReadPrice),
		CacheWriteUSD:          perMillion(breakdown.Input.CacheWriteTokens, cacheWritePrice),
		OutputUSD:              perMillion(breakdown.Output.TotalTokens, outputPrice),
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
	cost.TotalUSD = cost.UncachedInputUSD + cost.CacheReadUSD + cost.CacheWriteUSD + cost.OutputUSD
	return cost
}

func perMillion(tokens int64, pricePer1M float64) float64 {
	if tokens <= 0 || pricePer1M == 0 {
		return 0
	}
	return float64(tokens) / 1_000_000 * pricePer1M
}

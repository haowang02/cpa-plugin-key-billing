package billing

import "strings"

const TokenAccountingSchemaVersion = 2

type TokenAccountingQuality string

const (
	TokenAccountingComplete     TokenAccountingQuality = "complete"
	TokenAccountingInconsistent TokenAccountingQuality = "inconsistent"
	TokenAccountingUnclassified TokenAccountingQuality = "unclassified"
)

type TokenInputBreakdown struct {
	TotalTokens      int64 `json:"total_tokens"`
	UncachedTokens   int64 `json:"uncached_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

type TokenOutputBreakdown struct {
	TotalTokens        int64 `json:"total_tokens"`
	NonReasoningTokens int64 `json:"non_reasoning_tokens"`
	ReasoningTokens    int64 `json:"reasoning_tokens"`
}

// TokenBreakdown is the canonical, non-overlapping accounting contract emitted
// by CLIProxyAPI before any downstream response translation occurs.
type TokenBreakdown struct {
	SchemaVersion      int                    `json:"schema_version"`
	Quality            TokenAccountingQuality `json:"quality"`
	TotalTokens        int64                  `json:"total_tokens"`
	Input              TokenInputBreakdown    `json:"input"`
	Output             TokenOutputBreakdown   `json:"output"`
	UnclassifiedTokens int64                  `json:"unclassified_tokens"`
}

func (b TokenBreakdown) Valid() bool {
	if b.SchemaVersion != TokenAccountingSchemaVersion {
		return false
	}
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

// PriceSource records where a resolved price came from, for display and for
// distinguishing "priced at zero" from "not priced".
type PriceSource string

const (
	PriceSourceOverride PriceSource = "override"
	PriceSourceBuiltin  PriceSource = "builtin"
	PriceSourceNone     PriceSource = "none"
	PriceSourceCustom   PriceSource = "custom"
)

// Price is a resolved, fully specified price in USD per 1,000,000 tokens.
type Price struct {
	InputPer1M      float64                   `json:"input_per_1m"`
	OutputPer1M     float64                   `json:"output_per_1m"`
	CacheReadPer1M  float64                   `json:"cache_read_per_1m"`
	CacheWritePer1M float64                   `json:"cache_write_per_1m"`
	Source          PriceSource               `json:"source"`
	Pattern         string                    `json:"pattern,omitempty"`
	MatchedOn       string                    `json:"matched_on,omitempty"`
	LongContext     *ResolvedLongContextPrice `json:"long_context,omitempty"`
}

type ResolvedLongContextPrice struct {
	ThresholdInputTokens int64   `json:"threshold_input_tokens"`
	InputPer1M           float64 `json:"input_per_1m"`
	OutputPer1M          float64 `json:"output_per_1m"`
	CacheReadPer1M       float64 `json:"cache_read_per_1m"`
	CacheWritePer1M      float64 `json:"cache_write_per_1m"`
}

// resolve fills in the cache-price fallbacks and records provenance.
func (r PriceRule) resolve(source PriceSource, matchedOn string) Price {
	price := Price{
		InputPer1M:      r.InputPer1M,
		OutputPer1M:     r.OutputPer1M,
		CacheReadPer1M:  r.InputPer1M,
		CacheWritePer1M: r.InputPer1M,
		Source:          source,
		Pattern:         r.Pattern,
		MatchedOn:       matchedOn,
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

// ResolvePrice finds the price for one usage record.
//
// Administrator overrides are consulted in full before the reference catalog, so
// an override always wins; an override that lost to a more specific reference
// entry would be the kind of surprise that costs money. Within each table the
// order is exact model, exact alias, glob model, glob alias.
func (s *State) ResolvePrice(model, alias string) Price {
	if rule, matchedOn, ok := matchPriceRule(s.Prices, model, alias); ok {
		return rule.resolve(PriceSourceOverride, matchedOn)
	}
	if rule, matchedOn, ok := lookupBuiltin(model, alias); ok {
		return rule.resolve(PriceSourceBuiltin, matchedOn)
	}
	return Price{Source: PriceSourceNone}
}

func matchPriceRule(rules []PriceRule, model, alias string) (PriceRule, string, bool) {
	model = strings.TrimSpace(model)
	alias = strings.TrimSpace(alias)

	for _, candidate := range []struct {
		value string
		label string
	}{{model, "model"}, {alias, "alias"}} {
		if candidate.value == "" {
			continue
		}
		for _, rule := range rules {
			if !isGlob(rule.Pattern) && strings.EqualFold(strings.TrimSpace(rule.Pattern), candidate.value) {
				return rule, candidate.label, true
			}
		}
	}
	for _, candidate := range []struct {
		value string
		label string
	}{{model, "model-glob"}, {alias, "alias-glob"}} {
		if candidate.value == "" {
			continue
		}
		for _, rule := range rules {
			if isGlob(rule.Pattern) && globMatch(strings.TrimSpace(rule.Pattern), candidate.value) {
				return rule, candidate.label, true
			}
		}
	}
	return PriceRule{}, "", false
}

func isGlob(pattern string) bool {
	return strings.ContainsAny(pattern, "*?")
}

// globMatch is a case-insensitive '*' and '?' matcher.
//
// path.Match is not usable here: its '*' stops at '/', which would break
// patterns like "*claude*" against OpenRouter-style names such as
// "anthropic/claude-sonnet-4".
func globMatch(pattern, value string) bool {
	pattern = strings.ToLower(pattern)
	value = strings.ToLower(value)

	// Two-pointer scan with backtracking to the last '*'.
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

	// Billed token counts after semantics normalization and clamping.
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

// ComputeCost prices one already-normalized canonical usage record. Invalid or
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

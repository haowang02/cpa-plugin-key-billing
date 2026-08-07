package billing

import "strings"

// Semantics describes how a response payload's token counters overlap. It is
// detected from the usage block rather than the protocol label because CPA may
// translate protocols before the plugin sees the response.
type Semantics string

const (
	// Cache and reasoning tokens are included in the input/output totals.
	SemanticsSubset Semantics = "subset"
	// Cache and reasoning tokens are reported separately from input/output.
	SemanticsIndependent Semantics = "independent"
	// Cache is included in input, while reasoning is separate from output.
	SemanticsSeparateReasoning Semantics = "separate_reasoning"
)

// TokenUsage mirrors the token counters CLIProxyAPI delivers with a usage
// record. It is declared here rather than imported so the billing domain has no
// dependency on the RPC layer.
type TokenUsage struct {
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	// TotalTokens is carried for fidelity with what the provider reported. No
	// price is applied to it: the bill is built from the buckets above, which
	// is the only way it stays correct across the layouts that disagree about
	// what the total includes.
	TotalTokens int64
}

// PriceSource records where a resolved price came from, for display and for
// distinguishing "priced at zero" from "not priced".
type PriceSource string

const (
	// PriceSourceOverride is a row from the stored price table, reported when
	// resolving what one request costs.
	PriceSourceOverride PriceSource = "override"
	// PriceSourceBuiltin is the shipped public price catalog.
	PriceSourceBuiltin PriceSource = "builtin"
	// PriceSourceNone means no row matched; the model bills at zero.
	PriceSourceNone PriceSource = "none"
	// PriceSourceCustom marks a stored row an administrator has edited away
	// from its catalog default. It is reported when listing the price table,
	// where every model has a row and the question is where its number came
	// from rather than which rule won.
	PriceSourceCustom PriceSource = "custom"
)

// Price is a resolved, fully specified price in USD per 1,000,000 tokens.
type Price struct {
	InputPer1M      float64     `json:"input_per_1m"`
	OutputPer1M     float64     `json:"output_per_1m"`
	CacheReadPer1M  float64     `json:"cache_read_per_1m"`
	CacheWritePer1M float64     `json:"cache_write_per_1m"`
	Source          PriceSource `json:"source"`
	Pattern         string      `json:"pattern,omitempty"`
	MatchedOn       string      `json:"matched_on,omitempty"`
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
	return price
}

// ResolvePrice finds the price for one usage record.
//
// Administrator overrides are consulted in full before the built-in catalog, so
// an override always wins; an override that lost to a more specific built-in
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

// Cost is the priced breakdown of one usage record.
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
}

// ComputeCost prices one usage record.
//
// The token buckets are first normalized according to the semantics detected
// from the payload the counters were read out of:
//
//   - independent: InputTokens excludes cache, so all four buckets are summed.
//   - separate_reasoning: cache is inside InputTokens and reasoning is outside
//     OutputTokens.
//   - subset (and any unrecognized value): cache is inside InputTokens and
//     reasoning is inside OutputTokens.
//
// For the two "cache inside input" cases the cache buckets are clamped so the
// billed input tokens add up to exactly InputTokens, which keeps a provider
// reporting inconsistent counters from inflating the bill.
func ComputeCost(price Price, semantics Semantics, usage TokenUsage) Cost {
	usage = clampUsage(usage)

	cacheRead := usage.CacheReadTokens
	if cacheRead == 0 {
		// Some providers only populate the aggregate CachedTokens field.
		cacheRead = usage.CachedTokens
	}
	cacheWrite := usage.CacheCreationTokens

	var uncachedInput, billedOutput int64
	switch semantics {
	case SemanticsIndependent:
		uncachedInput = usage.InputTokens
		billedOutput = usage.OutputTokens + usage.ReasoningTokens
	case SemanticsSeparateReasoning:
		cacheRead, cacheWrite = clampCacheToInput(usage.InputTokens, cacheRead, cacheWrite)
		uncachedInput = usage.InputTokens - cacheRead - cacheWrite
		billedOutput = usage.OutputTokens + usage.ReasoningTokens
	default: // SemanticsSubset and anything unrecognized.
		cacheRead, cacheWrite = clampCacheToInput(usage.InputTokens, cacheRead, cacheWrite)
		uncachedInput = usage.InputTokens - cacheRead - cacheWrite
		billedOutput = usage.OutputTokens
	}

	cost := Cost{
		UncachedInputUSD:    perMillion(uncachedInput, price.InputPer1M),
		CacheReadUSD:        perMillion(cacheRead, price.CacheReadPer1M),
		CacheWriteUSD:       perMillion(cacheWrite, price.CacheWritePer1M),
		OutputUSD:           perMillion(billedOutput, price.OutputPer1M),
		UncachedInputTokens: uncachedInput,
		CacheReadTokens:     cacheRead,
		CacheWriteTokens:    cacheWrite,
		BilledOutputTokens:  billedOutput,
	}
	cost.TotalUSD = cost.UncachedInputUSD + cost.CacheReadUSD + cost.CacheWriteUSD + cost.OutputUSD
	return cost
}

// clampCacheToInput caps the cache buckets so they never exceed the reported
// input total, preferring to keep cache reads intact over cache writes.
func clampCacheToInput(input, cacheRead, cacheWrite int64) (int64, int64) {
	if cacheRead > input {
		cacheRead = input
	}
	if remaining := input - cacheRead; cacheWrite > remaining {
		cacheWrite = remaining
	}
	return cacheRead, cacheWrite
}

func clampUsage(usage TokenUsage) TokenUsage {
	usage.InputTokens = nonNegative(usage.InputTokens)
	usage.OutputTokens = nonNegative(usage.OutputTokens)
	usage.ReasoningTokens = nonNegative(usage.ReasoningTokens)
	usage.CachedTokens = nonNegative(usage.CachedTokens)
	usage.CacheReadTokens = nonNegative(usage.CacheReadTokens)
	usage.CacheCreationTokens = nonNegative(usage.CacheCreationTokens)
	usage.TotalTokens = nonNegative(usage.TotalTokens)
	return usage
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func perMillion(tokens int64, pricePer1M float64) float64 {
	if tokens <= 0 || pricePer1M == 0 {
		return 0
	}
	return float64(tokens) / 1_000_000 * pricePer1M
}

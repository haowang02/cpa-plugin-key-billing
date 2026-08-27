package plugin

import (
	"math"
	"strings"

	"cpa-key-billing/internal/billing"
)

type tokenSemantics uint8

const (
	tokenSemanticsUnknown tokenSemantics = iota
	tokenSemanticsSubset
	tokenSemanticsIndependent
	tokenSemanticsSeparateReasoning
)

func usageHasTokens(detail UsageDetail) bool {
	return detail.InputTokens != 0 ||
		detail.OutputTokens != 0 ||
		detail.ReasoningTokens != 0 ||
		detail.CachedTokens != 0 ||
		detail.CacheReadTokens != 0 ||
		detail.CacheCreationTokens != 0 ||
		detail.TotalTokens != 0
}

// usageBreakdown reconstructs the provider-specific accounting semantics
// CLIProxyAPI applies before flattening its internal usage.Detail for plugins.
// A zero detail means the provider reported no usage, not a measured zero, so
// it deliberately keeps the zero-value quality used by the billing log.
func usageBreakdown(record UsageRecord) billing.TokenBreakdown {
	detail := record.Detail
	if !usageHasTokens(detail) {
		return billing.TokenBreakdown{}
	}
	semantics := usageSemantics(record.Provider, record.ExecutorType)
	input := detail.InputTokens
	output := detail.OutputTokens
	reasoning := detail.ReasoningTokens
	cacheRead := detail.CacheReadTokens
	cacheWrite := detail.CacheCreationTokens
	total := detail.TotalTokens
	if total > 0 && input == 0 && output == 0 && reasoning == 0 && cacheRead == 0 && cacheWrite == 0 &&
		detail.CachedTokens == 0 && (semantics == tokenSemanticsSubset || semantics == tokenSemanticsUnknown) {
		return unclassifiedBreakdown(total)
	}
	if total == 0 && input == 0 && output == 0 {
		lowerBound, okLowerBound := unclassifiedLowerBound(input, output, reasoning, cacheRead, cacheWrite, detail.CachedTokens)
		if !okLowerBound {
			return inconsistentBreakdown(total, 0)
		}
		if lowerBound > 0 && (semantics == tokenSemanticsUnknown || semantics == tokenSemanticsSubset ||
			(semantics == tokenSemanticsSeparateReasoning && (cacheRead > 0 || cacheWrite > 0 || detail.CachedTokens > 0))) {
			return unclassifiedBreakdown(lowerBound)
		}
	}
	// OpenAI-style parsers retain raw child buckets even when their parent total
	// is absent, but the canonical host breakdown leaves those children in the
	// unclassified remainder. Presence bits are not exposed through UsageRecord;
	// under subset semantics, a non-zero child of a zero parent is the equivalent
	// signal.
	if semantics == tokenSemanticsSubset {
		if input == 0 {
			cacheRead = 0
			cacheWrite = 0
		}
		if output == 0 {
			reasoning = 0
		}
	}

	switch semantics {
	case tokenSemanticsSubset:
		return subsetBreakdown(input, cacheRead, cacheWrite, output, reasoning, total)
	case tokenSemanticsIndependent:
		if reasoning < 0 || reasoning > output {
			return inconsistentBreakdown(total, 0)
		}
		return independentBreakdown(input, cacheRead, cacheWrite, output-reasoning, reasoning, total)
	case tokenSemanticsSeparateReasoning:
		return separateReasoningBreakdown(input, cacheRead, cacheWrite, output, reasoning, total)
	default:
		// Unknown child buckets may overlap their parents, so use only the
		// top-level input and output counts.
		return subsetBreakdown(input, 0, 0, output, 0, total)
	}
}

// Keep this marker order byte-for-byte equivalent to CLIProxyAPI's current
// sdk/cliproxy/usage classifier. OpenAI-compatible executors win before any
// provider substring classification.
func usageSemantics(provider, executorType string) tokenSemantics {
	normalizedProvider := strings.ToLower(strings.TrimSpace(provider))
	normalizedExecutor := strings.ToLower(strings.TrimSpace(executorType))
	value := strings.TrimSpace(normalizedProvider + " " + normalizedExecutor)
	if value == "" || value == "unknown" || value == "unknown unknown" {
		return tokenSemanticsUnknown
	}
	if normalizedExecutor == "openaicompatexecutor" || normalizedProvider == "openai-compatibility" ||
		strings.HasPrefix(normalizedProvider, "openai-compatible-") {
		return tokenSemanticsSubset
	}
	if strings.Contains(value, "claude") || strings.Contains(value, "anthropic") {
		return tokenSemanticsIndependent
	}
	for _, marker := range []string{"gemini", "aistudio", "antigravity", "vertex", "interaction"} {
		if strings.Contains(value, marker) {
			return tokenSemanticsSeparateReasoning
		}
	}
	for _, marker := range []string{"openai", "codex", "xai", "grok", "kimi", "qwen", "deepseek", "openrouter"} {
		if strings.Contains(value, marker) {
			return tokenSemanticsSubset
		}
	}
	return tokenSemanticsUnknown
}

func subsetBreakdown(input, cacheRead, cacheWrite, output, reasoning, total int64) billing.TokenBreakdown {
	expected, okExpected := checkedTokenSum(input, output)
	cache, okCache := checkedTokenSum(cacheRead, cacheWrite)
	if !okExpected || !okCache || cache > input || reasoning < 0 || reasoning > output {
		return inconsistentBreakdown(total, expected)
	}
	if total < 0 || total > 0 && total < expected {
		return inconsistentBreakdown(total, expected)
	}
	breakdown := completeBreakdown(input-cache, cacheRead, cacheWrite, output-reasoning, reasoning, expected)
	if total > expected {
		breakdown.Quality = billing.TokenAccountingUnclassified
		breakdown.TotalTokens = total
		breakdown.UnclassifiedTokens = total - expected
	}
	return breakdown
}

func independentBreakdown(input, cacheRead, cacheWrite, output, reasoning, total int64) billing.TokenBreakdown {
	inputTotal, okInput := checkedTokenSum(input, cacheRead, cacheWrite)
	outputTotal, okOutput := checkedTokenSum(output, reasoning)
	expected, okExpected := checkedTokenSum(inputTotal, outputTotal)
	if !okInput || !okOutput || !okExpected {
		return inconsistentBreakdown(total, expected)
	}
	resolved, okTotal := resolveTotal(total, expected)
	if !okTotal {
		return inconsistentBreakdown(total, expected)
	}
	return completeBreakdown(input, cacheRead, cacheWrite, output, reasoning, resolved)
}

func separateReasoningBreakdown(input, cacheRead, cacheWrite, output, reasoning, total int64) billing.TokenBreakdown {
	cache, okCache := checkedTokenSum(cacheRead, cacheWrite)
	if input < 0 || !okCache || cache > input {
		return inconsistentBreakdown(total, 0)
	}
	outputTotal, okOutput := checkedTokenSum(output, reasoning)
	expected, okExpected := checkedTokenSum(input, outputTotal)
	if !okOutput || !okExpected {
		return inconsistentBreakdown(total, expected)
	}
	resolved, okTotal := resolveTotal(total, expected)
	if !okTotal {
		return inconsistentBreakdown(total, expected)
	}
	return completeBreakdown(input-cache, cacheRead, cacheWrite, output, reasoning, resolved)
}

func completeBreakdown(input, cacheRead, cacheWrite, output, reasoning, total int64) billing.TokenBreakdown {
	return billing.TokenBreakdown{
		Quality:     billing.TokenAccountingComplete,
		TotalTokens: total,
		Input: billing.TokenInputBreakdown{
			TotalTokens:      input + cacheRead + cacheWrite,
			UncachedTokens:   input,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		},
		Output: billing.TokenOutputBreakdown{
			TotalTokens:        output + reasoning,
			NonReasoningTokens: output,
			ReasoningTokens:    reasoning,
		},
	}
}

func unclassifiedBreakdown(total int64) billing.TokenBreakdown {
	if total < 0 {
		return inconsistentBreakdown(total, 0)
	}
	if total == 0 {
		return billing.TokenBreakdown{Quality: billing.TokenAccountingComplete}
	}
	return billing.TokenBreakdown{
		Quality:            billing.TokenAccountingUnclassified,
		TotalTokens:        total,
		UnclassifiedTokens: total,
	}
}

func inconsistentBreakdown(total, computedTotal int64) billing.TokenBreakdown {
	if total <= 0 {
		total = computedTotal
	}
	if total < 0 {
		total = 0
	}
	return billing.TokenBreakdown{
		Quality:            billing.TokenAccountingInconsistent,
		TotalTokens:        total,
		UnclassifiedTokens: total,
	}
}

func resolveTotal(total, expected int64) (int64, bool) {
	if total < 0 || expected < 0 {
		return 0, false
	}
	if total == 0 {
		return expected, true
	}
	return total, total == expected
}

func checkedTokenSum(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value < 0 || total > math.MaxInt64-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

func unclassifiedLowerBound(input, output, reasoning, cacheRead, cacheWrite, cached int64) (int64, bool) {
	cache, okCache := checkedTokenSum(cacheRead, cacheWrite)
	if !okCache || input < 0 || output < 0 || reasoning < 0 || cached < 0 {
		return 0, false
	}
	if cache > input {
		input = cache
	}
	if cached > input {
		input = cached
	}
	if reasoning > output {
		output = reasoning
	}
	return checkedTokenSum(input, output)
}

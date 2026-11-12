package plugin

import (
	"math"
	"net/url"
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
// it deliberately keeps the zero-value quality used by request events.
//
// Plugin executors and other unknown executors carry no semantics. Their usage
// is still classified when every semantics the counts do not contradict yields
// the same breakdown, because CLIProxyAPI recorded one of them.
func usageBreakdown(record UsageRecord) billing.TokenBreakdown {
	detail := record.Detail
	if !usageHasTokens(detail) {
		return billing.TokenBreakdown{}
	}
	if semantics := usageSemantics(record.Provider, record.ExecutorType); semantics != tokenSemanticsUnknown {
		return semanticBreakdown(detail, semantics)
	}
	var agreed *billing.TokenBreakdown
	for _, semantics := range []tokenSemantics{tokenSemanticsSubset, tokenSemanticsIndependent, tokenSemanticsSeparateReasoning} {
		breakdown := semanticBreakdown(detail, semantics)
		if breakdown.Quality == billing.TokenAccountingInconsistent {
			continue
		}
		if agreed != nil && *agreed != breakdown {
			return unclassifiedUsage(detail)
		}
		agreed = &breakdown
	}
	if agreed == nil || agreed.Quality != billing.TokenAccountingComplete {
		return unclassifiedUsage(detail)
	}
	return *agreed
}

func semanticBreakdown(detail UsageDetail, semantics tokenSemantics) billing.TokenBreakdown {
	input := detail.InputTokens
	output := detail.OutputTokens
	reasoning := detail.ReasoningTokens
	cacheRead := detail.CacheReadTokens
	cacheWrite := detail.CacheCreationTokens
	total := detail.TotalTokens
	if total > 0 && input == 0 && output == 0 && reasoning == 0 && cacheRead == 0 && cacheWrite == 0 &&
		detail.CachedTokens == 0 && semantics == tokenSemanticsSubset {
		return unclassifiedBreakdown(total)
	}
	if total == 0 && input == 0 && output == 0 {
		lowerBound, okLowerBound := unclassifiedLowerBound(input, output, reasoning, cacheRead, cacheWrite, detail.CachedTokens)
		if !okLowerBound {
			return inconsistentBreakdown(total, 0)
		}
		if lowerBound > 0 && (semantics == tokenSemanticsSubset ||
			(semantics == tokenSemanticsSeparateReasoning && (cacheRead > 0 || cacheWrite > 0 || detail.CachedTokens > 0))) {
			return unclassifiedBreakdown(lowerBound)
		}
	}
	switch semantics {
	case tokenSemanticsIndependent:
		return providerIndependentBreakdown(input, cacheRead, cacheWrite, output, reasoning, total)
	case tokenSemanticsSeparateReasoning:
		return separateReasoningBreakdown(input, cacheRead, cacheWrite, output, reasoning, total)
	}
	// OpenAI-style parsers retain raw child buckets even when their parent total
	// is absent, but the canonical host breakdown leaves those children in the
	// unclassified remainder. Presence bits are not exposed through UsageRecord;
	// under subset semantics, a non-zero child of a zero parent is the equivalent
	// signal.
	if input == 0 {
		cacheRead = 0
		cacheWrite = 0
	}
	if output == 0 {
		reasoning = 0
	}
	return subsetBreakdown(input, cacheRead, cacheWrite, output, reasoning, total)
}

// unclassifiedUsage keeps the reported total, or the sum of the known parts
// when the provider reported none.
func unclassifiedUsage(detail UsageDetail) billing.TokenBreakdown {
	lowerBound, okLowerBound := unclassifiedLowerBound(detail.InputTokens, detail.OutputTokens, detail.ReasoningTokens,
		detail.CacheReadTokens, detail.CacheCreationTokens, detail.CachedTokens)
	if !okLowerBound {
		return inconsistentBreakdown(detail.TotalTokens, 0)
	}
	total := detail.TotalTokens
	if total == 0 {
		total = lowerBound
	}
	return unclassifiedBreakdown(total)
}

// Native executors can publish a canonical breakdown that the RPC contract
// flattens. Match their known parser semantics before the host's legacy
// provider classifier.
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
	switch normalizedExecutor {
	case "metaexecutor": // ParseCodexUsage: reasoning and cache are subsets.
		return tokenSemanticsSubset
	case "devinexecutor": // ParseInteractionsUsage: reasoning is separate.
		return tokenSemanticsSeparateReasoning
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
		if input > 0 && output > 0 {
			return inconsistentBreakdown(total, expected)
		}
		breakdown.Quality = billing.TokenAccountingUnclassified
		breakdown.TotalTokens = total
		breakdown.UnclassifiedTokens = total - expected
	}
	return breakdown
}

func providerIndependentBreakdown(input, cacheRead, cacheWrite, output, reasoning, total int64) billing.TokenBreakdown {
	inputTotal, okInput := checkedTokenSum(input, cacheRead, cacheWrite)
	separateTotal, okSeparate := checkedTokenSum(inputTotal, output, reasoning)
	if !okInput || !okSeparate {
		return inconsistentBreakdown(total, 0)
	}
	if total == separateTotal {
		return independentBreakdown(input, cacheRead, cacheWrite, output, reasoning, total)
	}
	if reasoning < 0 || reasoning > output {
		return inconsistentBreakdown(total, 0)
	}
	return independentBreakdown(input, cacheRead, cacheWrite, output-reasoning, reasoning, total)
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

func (record UsageRecord) executionMetadata() billing.ExecutionMetadata {
	metadata := billing.ExecutionMetadata{
		RequestID: strings.TrimSpace(record.RequestID), TraceID: strings.TrimSpace(record.TraceID),
		SessionID: strings.TrimSpace(record.SessionID), ParentSessionID: strings.TrimSpace(record.ParentSessionID),
		AccessProvider: strings.TrimSpace(record.AccessProvider), IsNativeKey: &record.IsNativeKey,
	}
	if id := strings.TrimSpace(record.AuthID); id != "" {
		metadata.CredentialRef = billing.CredentialFingerprint(id)
	}
	// An upstream URL may contain credentials in any URL component. Store only
	// its HTTP origin, never user info, query parameters, fragments or path tokens.
	if parsed, err := url.Parse(strings.TrimSpace(record.BaseURL)); err == nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
		metadata.BaseURL = parsed.Scheme + "://" + parsed.Host
	}
	return metadata
}

// usageAccount names the upstream credential of a usage record. The host
// reports the caller's own key for a credential without an account; an auth
// file is then named by its title, and other credentials stay unnamed.
func (a *App) usageAccount(record UsageRecord) string {
	if account := strings.TrimSpace(record.Source); account != "" && account != strings.TrimSpace(record.APIKey) {
		switch strings.ToLower(strings.TrimSpace(record.AuthType)) {
		case "apikey":
			return billing.PreviewKey(account)
		case "oauth":
			return account
		}
	}
	index := strings.TrimSpace(record.AuthIndex)
	if index == "" || a.hostCaller == nil {
		return ""
	}
	file, err := a.hostAuthRuntime(index)
	if err != nil || credentialSourceFromHost(file) != billing.CredentialSourceAuthFiles {
		return ""
	}
	return authFileTitle(file)
}

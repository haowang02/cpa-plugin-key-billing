package plugin

import (
	"bytes"
	"encoding/json"
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

type tokenValue struct {
	value int64
	set   bool
}

type upstreamUsage struct {
	semantics  tokenSemantics
	input      tokenValue
	output     tokenValue
	reasoning  tokenValue
	cacheRead  tokenValue
	cacheWrite tokenValue
	total      tokenValue
}

type upstreamResponseFrame struct {
	responseID string
	usage      upstreamUsage
}

func (u *upstreamUsage) merge(next upstreamUsage) {
	if !next.hasUsage() {
		return
	}
	if next.semantics != tokenSemanticsUnknown || u.semantics == tokenSemanticsUnknown {
		u.semantics = next.semantics
	}
	mergeTokenValue(&u.input, next.input)
	mergeTokenValue(&u.output, next.output)
	mergeTokenValue(&u.reasoning, next.reasoning)
	mergeTokenValue(&u.cacheRead, next.cacheRead)
	mergeTokenValue(&u.cacheWrite, next.cacheWrite)
	mergeTokenValue(&u.total, next.total)
}

func (u upstreamUsage) hasUsage() bool {
	return u.input.set || u.output.set || u.reasoning.set || u.cacheRead.set || u.cacheWrite.set || u.total.set
}

func mergeTokenValue(current *tokenValue, next tokenValue) {
	if next.set {
		*current = next
	}
}

func parseUpstreamResponse(format string, body []byte) []upstreamResponseFrame {
	objects := responseObjects(body)
	if len(objects) == 0 {
		return nil
	}
	frames := make([]upstreamResponseFrame, 0, len(objects))
	for _, object := range objects {
		frames = append(frames, upstreamResponseFrame{
			responseID: responseID(object),
			usage:      parseUsageObject(format, object),
		})
	}
	return frames
}

func responseObjects(body []byte) []map[string]any {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}
	if object, ok := decodeJSONObject(trimmed); ok {
		return []map[string]any{object}
	}
	var objects []map[string]any
	for _, line := range bytes.Split(trimmed, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if object, ok := decodeJSONObject(payload); ok {
			objects = append(objects, object)
		}
	}
	return objects
}

func decodeJSONObject(raw []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if errDecode := decoder.Decode(&object); errDecode != nil || object == nil {
		return nil, false
	}
	return object, true
}

func responseID(object map[string]any) string {
	for _, path := range [][]string{
		{"response", "id"},
		{"message", "id"},
		{"interaction", "id"},
		{"response", "responseId"},
		{"responseId"},
		{"traceId"},
		{"id"},
	} {
		if value, ok := objectPath(object, path...); ok {
			if id, okString := value.(string); okString && strings.TrimSpace(id) != "" {
				return strings.TrimSpace(id)
			}
		}
	}
	return ""
}

func parseUsageObject(format string, object map[string]any) upstreamUsage {
	semantics := usageSemantics(format)
	switch semantics {
	case tokenSemanticsIndependent:
		return parseAnthropicUsage(object)
	case tokenSemanticsSeparateReasoning:
		if strings.Contains(strings.ToLower(format), "interaction") {
			return parseInteractionsUsage(object)
		}
		return parseGeminiUsage(object)
	case tokenSemanticsSubset:
		return parseOpenAIUsage(object, semantics)
	default:
		// Preserve the authoritative total, but unknown token semantics remain
		// unclassified and therefore cost zero.
		return parseOpenAIUsage(object, semantics)
	}
}

func usageSemantics(format string) tokenSemantics {
	format = strings.ToLower(strings.TrimSpace(format))
	if strings.Contains(format, "claude") || strings.Contains(format, "anthropic") {
		return tokenSemanticsIndependent
	}
	for _, marker := range []string{"gemini", "aistudio", "antigravity", "vertex", "interaction"} {
		if strings.Contains(format, marker) {
			return tokenSemanticsSeparateReasoning
		}
	}
	for _, marker := range []string{"openai", "codex", "xai", "grok", "kimi", "qwen", "deepseek", "openrouter"} {
		if strings.Contains(format, marker) {
			return tokenSemanticsSubset
		}
	}
	return tokenSemanticsUnknown
}

func parseOpenAIUsage(object map[string]any, semantics tokenSemantics) upstreamUsage {
	node := objectMapAt(object, "response", "usage")
	if node == nil {
		node = objectMapAt(object, "usage")
	}
	if node == nil {
		return upstreamUsage{}
	}
	usage := upstreamUsage{semantics: semantics}
	usage.input = firstToken(node, []string{"prompt_tokens"}, []string{"input_tokens"})
	usage.output = firstToken(node, []string{"completion_tokens"}, []string{"output_tokens"})
	usage.total = tokenAt(node, "total_tokens")
	usage.cacheRead = firstToken(node,
		[]string{"prompt_tokens_details", "cached_tokens"},
		[]string{"input_tokens_details", "cached_tokens"},
	)
	usage.cacheWrite = firstToken(node,
		[]string{"input_tokens_details", "cache_creation_tokens"},
		[]string{"input_tokens_details", "cache_write_tokens"},
		[]string{"prompt_tokens_details", "cache_creation_tokens"},
		[]string{"prompt_tokens_details", "cache_write_tokens"},
	)
	usage.reasoning = firstToken(node,
		[]string{"completion_tokens_details", "reasoning_tokens"},
		[]string{"output_tokens_details", "reasoning_tokens"},
	)
	return usage
}

func parseAnthropicUsage(object map[string]any) upstreamUsage {
	node := objectMapAt(object, "usage")
	if node == nil {
		node = objectMapAt(object, "message", "usage")
	}
	if node == nil {
		return upstreamUsage{}
	}
	usage := upstreamUsage{semantics: tokenSemanticsIndependent}
	usage.input = tokenAt(node, "input_tokens")
	usage.output = tokenAt(node, "output_tokens")
	usage.cacheRead = tokenAt(node, "cache_read_input_tokens")
	usage.cacheWrite = tokenAt(node, "cache_creation_input_tokens")
	usage.reasoning = firstToken(node,
		[]string{"output_tokens_details", "thinking_tokens"},
		[]string{"output_tokens_details", "reasoning_tokens"},
		[]string{"thinking_tokens"},
	)
	return usage
}

func parseGeminiUsage(object map[string]any) upstreamUsage {
	node := firstObjectMap(object,
		[]string{"response", "usageMetadata"},
		[]string{"usageMetadata"},
		[]string{"usage_metadata"},
	)
	if node == nil {
		return upstreamUsage{}
	}
	usage := upstreamUsage{semantics: tokenSemanticsSeparateReasoning}
	prompt := tokenAt(node, "promptTokenCount")
	tool := firstToken(node, []string{"toolUsePromptTokenCount"}, []string{"tool_use_prompt_token_count"})
	if prompt.set || tool.set {
		usage.input.set = true
		var ok bool
		usage.input.value, ok = checkedTokenSum(prompt.value, tool.value)
		if !ok {
			usage.input.value = -1
		}
	}
	usage.output = tokenAt(node, "candidatesTokenCount")
	usage.reasoning = tokenAt(node, "thoughtsTokenCount")
	usage.cacheRead = tokenAt(node, "cachedContentTokenCount")
	usage.total = tokenAt(node, "totalTokenCount")
	return usage
}

func parseInteractionsUsage(object map[string]any) upstreamUsage {
	node := firstObjectMap(object,
		[]string{"usage"},
		[]string{"total_usage"},
		[]string{"metadata", "total_usage"},
		[]string{"metadata", "usage"},
		[]string{"usageMetadata"},
		[]string{"usage_metadata"},
		[]string{"interaction", "usage"},
		[]string{"interaction", "total_usage"},
		[]string{"interaction", "metadata", "total_usage"},
	)
	if node == nil {
		return upstreamUsage{}
	}
	if _, promptExists := node["promptTokenCount"]; promptExists {
		return parseGeminiUsage(map[string]any{"usageMetadata": node})
	}
	if _, candidatesExists := node["candidatesTokenCount"]; candidatesExists {
		return parseGeminiUsage(map[string]any{"usageMetadata": node})
	}
	usage := upstreamUsage{semantics: tokenSemanticsSeparateReasoning}
	input := firstToken(node, []string{"input_tokens"}, []string{"prompt_tokens"}, []string{"total_input_tokens"})
	tool := firstToken(node, []string{"tool_use_tokens"}, []string{"total_tool_use_tokens"}, []string{"toolUseTokens"}, []string{"totalToolUseTokens"})
	if input.set || tool.set {
		usage.input.set = true
		var ok bool
		usage.input.value, ok = checkedTokenSum(input.value, tool.value)
		if !ok {
			usage.input.value = -1
		}
	}
	usage.output = firstToken(node, []string{"output_tokens"}, []string{"completion_tokens"}, []string{"total_output_tokens"})
	usage.reasoning = firstToken(node, []string{"reasoning_tokens"}, []string{"thoughtsTokenCount"}, []string{"total_thought_tokens"})
	usage.total = firstToken(node, []string{"total_tokens"}, []string{"totalTokenCount"})
	usage.cacheRead = firstToken(node,
		[]string{"cache_read_tokens"}, []string{"cacheReadTokens"},
		[]string{"cached_tokens"}, []string{"cachedContentTokenCount"}, []string{"total_cached_tokens"},
	)
	usage.cacheWrite = firstToken(node,
		[]string{"cache_creation_tokens"}, []string{"cacheCreationTokens"},
		[]string{"cache_write_tokens"}, []string{"cacheWriteTokens"},
	)
	return usage
}

func (u upstreamUsage) breakdown() billing.TokenBreakdown {
	input, output := u.input.value, u.output.value
	cacheRead, cacheWrite := u.cacheRead.value, u.cacheWrite.value
	reasoning, total := u.reasoning.value, u.total.value
	switch u.semantics {
	case tokenSemanticsSubset:
		if u.input.set && u.output.set {
			return subsetBreakdown(input, cacheRead, cacheWrite, output, reasoning, total)
		}
		if !u.input.set {
			cacheRead, cacheWrite = 0, 0
		}
		if !u.output.set {
			reasoning = 0
		}
		return partialSubsetBreakdown(input, cacheRead, cacheWrite, output, reasoning, total)
	case tokenSemanticsIndependent:
		nonReasoning := output
		if reasoning > 0 && reasoning <= output {
			nonReasoning = output - reasoning
		} else if reasoning > output {
			nonReasoning = 0
		}
		rawTotal, _ := checkedTokenSum(input, cacheRead, cacheWrite, output)
		return independentBreakdown(input, cacheRead, cacheWrite, nonReasoning, reasoning, rawTotal)
	case tokenSemanticsSeparateReasoning:
		if total == 0 {
			total, _ = checkedTokenSum(input, output, reasoning)
		}
		return separateReasoningBreakdown(input, cacheRead, cacheWrite, output, reasoning, total)
	default:
		if total == 0 {
			total = unclassifiedLowerBound(input, output, reasoning, cacheRead, cacheWrite)
		}
		return unclassifiedBreakdown(total)
	}
}

func subsetBreakdown(input, cacheRead, cacheWrite, output, reasoning, total int64) billing.TokenBreakdown {
	expected, okExpected := checkedTokenSum(input, output)
	cache, okCache := checkedTokenSum(cacheRead, cacheWrite)
	if !okExpected || !okCache || cache > input || reasoning < 0 || reasoning > output {
		return inconsistentBreakdown(total, expected)
	}
	resolved, okTotal := resolveTotal(total, expected)
	if !okTotal {
		return inconsistentBreakdown(total, expected)
	}
	return completeBreakdown(input-cache, cacheRead, cacheWrite, output-reasoning, reasoning, resolved)
}

func partialSubsetBreakdown(input, cacheRead, cacheWrite, output, reasoning, total int64) billing.TokenBreakdown {
	cache, okCache := checkedTokenSum(cacheRead, cacheWrite)
	expected, okExpected := checkedTokenSum(input, output)
	if !okCache || !okExpected || cache > input || reasoning < 0 || reasoning > output || total < 0 {
		return inconsistentBreakdown(total, expected)
	}
	if total == 0 {
		total = expected
	}
	if total < expected {
		return inconsistentBreakdown(total, expected)
	}
	unclassified := total - expected
	quality := billing.TokenAccountingComplete
	if unclassified > 0 {
		quality = billing.TokenAccountingUnclassified
	}
	return billing.TokenBreakdown{
		Quality:     quality,
		TotalTokens: total,
		Input: billing.TokenInputBreakdown{
			TotalTokens: input, UncachedTokens: input - cache, CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite,
		},
		Output: billing.TokenOutputBreakdown{
			TotalTokens: output, NonReasoningTokens: output - reasoning, ReasoningTokens: reasoning,
		},
		UnclassifiedTokens: unclassified,
	}
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
			TotalTokens: input + cacheRead + cacheWrite, UncachedTokens: input, CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite,
		},
		Output: billing.TokenOutputBreakdown{
			TotalTokens: output + reasoning, NonReasoningTokens: output, ReasoningTokens: reasoning,
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

func unclassifiedLowerBound(input, output, reasoning, cacheRead, cacheWrite int64) int64 {
	cache, ok := checkedTokenSum(cacheRead, cacheWrite)
	if !ok || input < 0 || output < 0 || reasoning < 0 {
		return 0
	}
	if cache > input {
		input = cache
	}
	if reasoning > output {
		output = reasoning
	}
	total, _ := checkedTokenSum(input, output)
	return total
}

func tokenAt(root map[string]any, path ...string) tokenValue {
	value, exists := objectPath(root, path...)
	if !exists || value == nil {
		return tokenValue{}
	}
	switch number := value.(type) {
	case json.Number:
		parsed, errParse := number.Int64()
		return tokenValue{value: parsed, set: errParse == nil}
	case float64:
		if math.Trunc(number) != number || number > math.MaxInt64 || number < math.MinInt64 {
			return tokenValue{}
		}
		return tokenValue{value: int64(number), set: true}
	case int64:
		return tokenValue{value: number, set: true}
	case int:
		return tokenValue{value: int64(number), set: true}
	default:
		return tokenValue{}
	}
}

func firstToken(root map[string]any, paths ...[]string) tokenValue {
	for _, path := range paths {
		if value := tokenAt(root, path...); value.set {
			return value
		}
	}
	return tokenValue{}
}

func objectMapAt(root map[string]any, path ...string) map[string]any {
	value, ok := objectPath(root, path...)
	if !ok {
		return nil
	}
	object, _ := value.(map[string]any)
	return object
}

func firstObjectMap(root map[string]any, paths ...[]string) map[string]any {
	for _, path := range paths {
		if object := objectMapAt(root, path...); object != nil {
			return object
		}
	}
	return nil
}

func objectPath(root map[string]any, path ...string) (any, bool) {
	var current any = root
	for _, part := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

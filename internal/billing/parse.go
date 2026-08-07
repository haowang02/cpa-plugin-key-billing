package billing

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ParseUsage extracts token counters from a response payload, together with the
// way those counters nest.
//
// It accepts both a complete JSON document (non-streaming responses) and a
// Server-Sent Events fragment (one or more streaming chunks). Across SSE frames
// the counters are merged with MergeMax, because providers report usage
// cumulatively and in pieces: Anthropic sends input counts in message_start and
// output counts in a later message_delta, while OpenAI and Gemini resend a
// growing total.
//
// The semantics come from the keys of the usage object itself. Nothing here
// consults the protocol the body was labelled with, and that is the point: CPA
// translates a response into the client's protocol before the interceptors see
// it, but a payload can still arrive carrying the upstream's counter layout
// under the downstream protocol's name. Reading an OpenAI Responses block as
// Anthropic is not a cosmetic error — its input_tokens includes the cached
// tokens, so charging it as Anthropic's cache-exclusive input bills the cache
// at the full input price and drops the discount, tripling a cache-heavy
// request.
//
// The returned semantics are empty when the block carried no key that names a
// layout. That is not a failure: such a block reports neither cache nor
// reasoning, and every layout bills it identically, so the caller is free to
// keep whatever an earlier, self-identifying block established.
//
// It reports ok=false when the payload carries no usage block at all, which is
// the common case for the content chunks of a stream.
func ParseUsage(body []byte) (TokenUsage, Semantics, bool) {
	if len(body) == 0 {
		return TokenUsage{}, "", false
	}
	// Cheap rejection first. Most streaming chunks are content deltas with no
	// usage block, and this substring scan keeps them off the JSON decoder,
	// which would otherwise run on every token of every stream. One marker
	// covers every layout, since Gemini's "usageMetadata" starts with it.
	if !bytes.Contains(body, usageKeyMarker) {
		return TokenUsage{}, "", false
	}

	if trimmed := bytes.TrimLeft(body, " \t\r\n"); len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed) {
		var payload map[string]any
		if errUnmarshal := json.Unmarshal(trimmed, &payload); errUnmarshal == nil {
			reading, ok := usageFromObject(payload)
			return reading.usage, reading.semantics, ok
		}
	}
	return parseSSEUsage(body)
}

var usageKeyMarker = []byte("usage")

// parseSSEUsage scans the `data:` lines of an SSE fragment.
func parseSSEUsage(body []byte) (TokenUsage, Semantics, bool) {
	var (
		merged    TokenUsage
		semantics Semantics
		found     bool
	)
	for _, rawLine := range bytes.Split(body, []byte("\n")) {
		line := bytes.TrimSpace(rawLine)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if !bytes.Contains(payload, usageKeyMarker) {
			continue
		}
		var decoded map[string]any
		if errUnmarshal := json.Unmarshal(payload, &decoded); errUnmarshal != nil {
			continue
		}
		reading, ok := usageFromObject(decoded)
		if !ok {
			continue
		}
		merged.MergeMax(reading.usage)
		semantics = mergeSemantics(semantics, reading.semantics)
		found = true
	}
	return merged, semantics, found
}

// wrapperKeys are the one-level nestings that carry a usage block: Anthropic's
// message_start wraps it under "message", the OpenAI Responses stream wraps it
// under "response", and the Interactions stream wraps it under "interaction".
var wrapperKeys = []string{"message", "response", "interaction"}

// usageReading is one usage block's counters plus the layout its keys named.
// An empty semantics means the block named no layout.
type usageReading struct {
	usage     TokenUsage
	semantics Semantics
}

func usageFromObject(payload map[string]any) (usageReading, bool) {
	if reading, ok := usageAtLevel(payload); ok {
		return reading, true
	}
	for _, key := range wrapperKeys {
		nested, ok := payload[key].(map[string]any)
		if !ok {
			continue
		}
		if reading, okNested := usageAtLevel(nested); okNested {
			return reading, true
		}
	}
	return usageReading{}, false
}

// usageAtLevel reads one usage object, choosing the reader from the keys it
// carries. Each layout uses keys the others never do, so the block identifies
// itself and no protocol label is needed to interpret it.
func usageAtLevel(payload map[string]any) (usageReading, bool) {
	if node, ok := payload["usageMetadata"].(map[string]any); ok {
		return usageReading{geminiUsage(node), SemanticsSeparateReasoning}, true
	}
	node, ok := payload["usage"].(map[string]any)
	if !ok {
		return usageReading{}, false
	}
	switch {
	case hasAnyKey(node, "cache_read_input_tokens", "cache_creation_input_tokens"):
		// Anthropic's own spelling of the cache buckets, which it reports
		// alongside a cache-exclusive input_tokens.
		return usageReading{claudeUsage(node), SemanticsIndependent}, true
	case hasAnyKey(node, "prompt_tokens", "completion_tokens",
		"prompt_tokens_details", "completion_tokens_details",
		"input_tokens_details", "output_tokens_details"):
		// OpenAI chat completions and Responses, where the cache and reasoning
		// counts are subsets reported under *_details.
		return usageReading{openAIUsage(node), SemanticsSubset}, true
	default:
		// Counters sitting directly on the object, which is what Anthropic, the
		// Responses API, and the Interactions envelope all look like once the
		// keys above are absent. The layout has to come out of the numbers.
		usage, semantics := flatUsage(node)
		return usageReading{usage, semantics}, true
	}
}

// flatUsage reads a usage block whose counters sit directly on the object,
// under either the plain or the "total_" spelling.
//
// That covers CLIProxyAPI's Interactions envelope, which is the reason this has
// to work at all: it passes the upstream's numbers through under one set of
// names regardless of which provider produced them. Anthropic arrives with a
// cache-exclusive input count, Gemini with a cache-inclusive one and reasoning
// outside the output count, the Responses API with both nested inside. The key
// names are identical in all three cases, so the nesting is derived from the
// counters themselves:
//
//   - Cache is always reported. Whether it sits inside the input count is the
//     one thing the arithmetic can prove outright.
//   - Reasoning is reported only when the total proves it sits outside the
//     output count. Left at zero it is billed as part of the output, which is
//     where every layout that does not separate it already counts it.
//
// A block that proves neither claims no layout, so a self-identifying block
// elsewhere in the same request decides. Failing that the caller's default
// treats cache as inside the input, which under-charges a cache-heavy
// Anthropic request by at most its input count rather than over-charging a
// Responses one without bound.
func flatUsage(node map[string]any) (TokenUsage, Semantics) {
	cached := firstNumber(node, "cached_tokens", "total_cached_tokens")
	usage := TokenUsage{
		InputTokens:     firstNumber(node, "input_tokens", "total_input_tokens"),
		OutputTokens:    firstNumber(node, "output_tokens", "total_output_tokens"),
		CachedTokens:    cached,
		CacheReadTokens: cached,
		TotalTokens:     firstNumber(node, "total_tokens"),
	}
	reasoning := firstNumber(node, "reasoning_tokens", "total_thought_tokens")

	switch {
	case cached > usage.InputTokens:
		// A cache count cannot be a subset of an input count it exceeds, so
		// these are reported independently, the way Anthropic does it.
		// Reasoning stays at zero: the layouts that report cache separately
		// still count reasoning inside the output.
		return usage, SemanticsIndependent
	case reasoning > 0 && usage.TotalTokens > 0 &&
		usage.TotalTokens == usage.InputTokens+usage.OutputTokens+reasoning:
		// The total only balances once reasoning is added on top of the output,
		// so it sits outside it. This is Gemini's layout reaching us through the
		// Interactions envelope, where thoughts can dwarf the visible output.
		usage.ReasoningTokens = reasoning
		return usage, SemanticsSeparateReasoning
	default:
		return usage, ""
	}
}

func hasAnyKey(node map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, exists := node[key]; exists {
			return true
		}
	}
	return false
}

// mergeSemantics resolves the layout when two identified frames disagree.
//
// MergeMax keeps the largest value of every counter, so once a frame has been
// seen whose input total already includes the cache buckets, the merged input
// total includes them too and has to be billed that way. Reading that number as
// cache-exclusive would charge the cached tokens twice — once at the full input
// price and once at the cache price — so a cache-inside-input layout always
// wins over Anthropic's.
func mergeSemantics(current, next Semantics) Semantics {
	switch {
	case next == "":
		return current
	case current == "" || current == SemanticsIndependent:
		return next
	case next == SemanticsIndependent:
		return current
	default:
		return next
	}
}

// openAIUsage reads the OpenAI chat-completions and Responses shapes. Cache and
// reasoning counts are subsets of the input and output totals respectively.
//
// The cache-creation field has three spellings in practice: CLIProxyAPI's own
// translators emit "cached_creation_tokens", while upstreams use
// "cache_creation_tokens" or "cache_write_tokens".
func openAIUsage(node map[string]any) TokenUsage {
	usage := TokenUsage{
		InputTokens:  firstNumber(node, "prompt_tokens", "input_tokens"),
		OutputTokens: firstNumber(node, "completion_tokens", "output_tokens"),
		TotalTokens:  firstNumber(node, "total_tokens"),
	}
	for _, detailKey := range []string{"prompt_tokens_details", "input_tokens_details"} {
		detail, ok := node[detailKey].(map[string]any)
		if !ok {
			continue
		}
		if value := firstNumber(detail, "cached_tokens"); value > 0 {
			usage.CachedTokens = value
			usage.CacheReadTokens = value
		}
		if value := firstNumber(detail, "cached_creation_tokens", "cache_creation_tokens", "cache_write_tokens"); value > 0 {
			usage.CacheCreationTokens = value
		}
	}
	for _, detailKey := range []string{"completion_tokens_details", "output_tokens_details"} {
		detail, ok := node[detailKey].(map[string]any)
		if !ok {
			continue
		}
		if value := firstNumber(detail, "reasoning_tokens"); value > 0 {
			usage.ReasoningTokens = value
		}
	}
	return usage
}

// claudeUsage reads the Anthropic shape, where input_tokens excludes both cache
// buckets.
func claudeUsage(node map[string]any) TokenUsage {
	cacheRead := firstNumber(node, "cache_read_input_tokens")
	cacheCreation := firstNumber(node, "cache_creation_input_tokens")
	return TokenUsage{
		InputTokens:         firstNumber(node, "input_tokens"),
		OutputTokens:        firstNumber(node, "output_tokens"),
		CachedTokens:        cacheRead,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: cacheCreation,
	}
}

// geminiUsage reads the Gemini shape, where cached content is inside the prompt
// count but thoughts are reported outside the candidate count.
func geminiUsage(node map[string]any) TokenUsage {
	cached := firstNumber(node, "cachedContentTokenCount")
	return TokenUsage{
		InputTokens:     firstNumber(node, "promptTokenCount"),
		OutputTokens:    firstNumber(node, "candidatesTokenCount"),
		ReasoningTokens: firstNumber(node, "thoughtsTokenCount"),
		CachedTokens:    cached,
		CacheReadTokens: cached,
		TotalTokens:     firstNumber(node, "totalTokenCount"),
	}
}

// firstNumber returns the first key present as a number. JSON decoding yields
// float64, but string-encoded counts appear in some provider payloads.
func firstNumber(node map[string]any, keys ...string) int64 {
	for _, key := range keys {
		raw, exists := node[key]
		if !exists {
			continue
		}
		switch value := raw.(type) {
		case float64:
			if value > 0 {
				return int64(value)
			}
			return 0
		case json.Number:
			if parsed, errParse := value.Int64(); errParse == nil {
				return parsed
			}
		case string:
			var parsed int64
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				continue
			}
			valid := true
			for _, char := range trimmed {
				if char < '0' || char > '9' {
					valid = false
					break
				}
				parsed = parsed*10 + int64(char-'0')
			}
			if valid {
				return parsed
			}
		}
	}
	return 0
}

// MergeMax folds another observation of the same request's usage into t by
// taking the larger value of each counter.
//
// The input total is the exception, because two observations can disagree about
// what it means. An observation that names a cache bucket has divided the input
// into cached and fresh tokens; one that names none has either nothing to
// divide or has not been told. Taking the larger of the two then measures a
// whole input against a remainder, and whichever wins, the cached tokens are
// charged twice: once at the full input rate inside the total, once at the
// cache rate beside it.
//
// This is not hypothetical. Serving a Claude client from a non-Claude upstream,
// CLIProxyAPI writes its own tokenizer's count of the whole prompt into
// message_start, since nothing upstream reports usage until the stream ends and
// a client whose context meter reads zero looks broken. That number arrives
// bare, ahead of the real counters, and on a Codex upstream with a warm cache it
// is several times the fresh input the request is actually charged for.
//
// So an observation that splits the input decides the input, and only
// observations of the same kind compete on size.
func (t *TokenUsage) MergeMax(other TokenUsage) {
	switch {
	case t.reportsCacheSplit() == other.reportsCacheSplit():
		// Both divide the input or neither does, so the two numbers mean the
		// same thing and the larger one wins, as every other counter does.
		t.InputTokens = max(t.InputTokens, other.InputTokens)
	case other.reportsCacheSplit():
		t.InputTokens = other.InputTokens
	default:
		// What is already merged divides the input and the new observation does
		// not, so the merged total stands however large the new one is.
	}
	t.OutputTokens = max(t.OutputTokens, other.OutputTokens)
	t.ReasoningTokens = max(t.ReasoningTokens, other.ReasoningTokens)
	t.CachedTokens = max(t.CachedTokens, other.CachedTokens)
	t.CacheReadTokens = max(t.CacheReadTokens, other.CacheReadTokens)
	t.CacheCreationTokens = max(t.CacheCreationTokens, other.CacheCreationTokens)
	t.TotalTokens = max(t.TotalTokens, other.TotalTokens)
}

// reportsCacheSplit reports whether this observation says how much of the input
// came from cache. A request that used no cache says nothing either way, which
// is harmless: with no cache to charge separately, nothing can be charged twice
// and the merge falls back to taking the larger input.
func (t TokenUsage) reportsCacheSplit() bool {
	return t.CachedTokens > 0 || t.CacheReadTokens > 0 || t.CacheCreationTokens > 0
}

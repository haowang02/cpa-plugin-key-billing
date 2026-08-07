package billing

import "testing"

func TestParseUsageOpenAIChatCompletion(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl-1",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "hi"}}],
		"usage": {
			"prompt_tokens": 1000,
			"completion_tokens": 500,
			"total_tokens": 1500,
			"prompt_tokens_details": {"cached_tokens": 400},
			"completion_tokens_details": {"reasoning_tokens": 200}
		}
	}`)
	usage, _, ok := ParseUsage(body)
	if !ok {
		t.Fatal("ParseUsage reported no usage")
	}
	want := TokenUsage{
		InputTokens: 1000, OutputTokens: 500, ReasoningTokens: 200,
		CachedTokens: 400, CacheReadTokens: 400, TotalTokens: 1500,
	}
	if usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
}

// TestParseUsageOpenAICacheCreationSpellings covers the three names this field
// appears under: CLIProxyAPI's own translators emit "cached_creation_tokens"
// when converting Anthropic usage, while upstreams use the other two.
func TestParseUsageOpenAICacheCreationSpellings(t *testing.T) {
	for _, field := range []string{"cached_creation_tokens", "cache_creation_tokens", "cache_write_tokens"} {
		t.Run(field, func(t *testing.T) {
			body := []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":400,"` + field + `":100}}}`)
			usage, _, ok := ParseUsage(body)
			if !ok {
				t.Fatal("ParseUsage reported no usage")
			}
			if usage.CacheCreationTokens != 100 {
				t.Fatalf("CacheCreationTokens = %d, want 100 (field %s)", usage.CacheCreationTokens, field)
			}
		})
	}
}

func TestParseUsageOpenAIResponsesShape(t *testing.T) {
	body := []byte(`{
		"usage": {
			"input_tokens": 800,
			"output_tokens": 300,
			"total_tokens": 1100,
			"input_tokens_details": {"cached_tokens": 200},
			"output_tokens_details": {"reasoning_tokens": 120}
		}
	}`)
	usage, _, ok := ParseUsage(body)
	if !ok {
		t.Fatal("ParseUsage reported no usage")
	}
	if usage.InputTokens != 800 || usage.OutputTokens != 300 ||
		usage.CacheReadTokens != 200 || usage.ReasoningTokens != 120 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestParseUsageClaude(t *testing.T) {
	body := []byte(`{
		"type": "message",
		"usage": {
			"input_tokens": 1000,
			"output_tokens": 500,
			"cache_read_input_tokens": 400,
			"cache_creation_input_tokens": 100
		}
	}`)
	usage, _, ok := ParseUsage(body)
	if !ok {
		t.Fatal("ParseUsage reported no usage")
	}
	want := TokenUsage{
		InputTokens: 1000, OutputTokens: 500,
		CachedTokens: 400, CacheReadTokens: 400, CacheCreationTokens: 100,
	}
	if usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
}

func TestParseUsageGemini(t *testing.T) {
	body := []byte(`{
		"candidates": [],
		"usageMetadata": {
			"promptTokenCount": 1000,
			"candidatesTokenCount": 500,
			"cachedContentTokenCount": 400,
			"thoughtsTokenCount": 200,
			"totalTokenCount": 1700
		}
	}`)
	usage, _, ok := ParseUsage(body)
	if !ok {
		t.Fatal("ParseUsage reported no usage")
	}
	want := TokenUsage{
		InputTokens: 1000, OutputTokens: 500, ReasoningTokens: 200,
		CachedTokens: 400, CacheReadTokens: 400, TotalTokens: 1700,
	}
	if usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
}

// TestParseUsageClaudeStreamMergesAcrossFrames is the case a single-frame
// parser gets wrong: Anthropic reports input counts in message_start and the
// final output count in a later message_delta.
func TestParseUsageClaudeStreamMergesAcrossFrames(t *testing.T) {
	body := []byte(`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":400,"cache_creation_input_tokens":100}}}

event: content_block_delta
data: {"type":"content_block_delta","delta":{"text":"hello"}}

event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":500}}

event: message_stop
data: {"type":"message_stop"}
`)
	usage, _, ok := ParseUsage(body)
	if !ok {
		t.Fatal("ParseUsage reported no usage")
	}
	if usage.InputTokens != 1000 || usage.CacheReadTokens != 400 || usage.CacheCreationTokens != 100 {
		t.Fatalf("input side lost across frames: %+v", usage)
	}
	if usage.OutputTokens != 500 {
		t.Fatalf("OutputTokens = %d, want the later 500 to win", usage.OutputTokens)
	}
}

func TestParseUsageOpenAIStream(t *testing.T) {
	body := []byte(`data: {"choices":[{"delta":{"content":"hi"}}]}

data: {"choices":[],"usage":{"prompt_tokens":700,"completion_tokens":250,"total_tokens":950,"prompt_tokens_details":{"cached_tokens":100}}}

data: [DONE]
`)
	usage, _, ok := ParseUsage(body)
	if !ok {
		t.Fatal("ParseUsage reported no usage")
	}
	if usage.InputTokens != 700 || usage.OutputTokens != 250 || usage.CacheReadTokens != 100 {
		t.Fatalf("usage = %+v", usage)
	}
}

// TestParseUsageOpenAIResponsesStreamNesting covers the Responses stream, where
// the usage block sits under a "response" wrapper.
func TestParseUsageOpenAIResponsesStreamNesting(t *testing.T) {
	body := []byte(`event: response.completed
data: {"type":"response.completed","response":{"usage":{"input_tokens":600,"output_tokens":200,"output_tokens_details":{"reasoning_tokens":50}}}}
`)
	usage, _, ok := ParseUsage(body)
	if !ok {
		t.Fatal("ParseUsage reported no usage")
	}
	if usage.InputTokens != 600 || usage.OutputTokens != 200 || usage.ReasoningTokens != 50 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestParseUsageGeminiStreamTakesTheLargestFrame(t *testing.T) {
	body := []byte(`data: {"usageMetadata":{"promptTokenCount":500,"candidatesTokenCount":10}}

data: {"usageMetadata":{"promptTokenCount":500,"candidatesTokenCount":220,"thoughtsTokenCount":80}}
`)
	usage, _, ok := ParseUsage(body)
	if !ok {
		t.Fatal("ParseUsage reported no usage")
	}
	if usage.InputTokens != 500 || usage.OutputTokens != 220 || usage.ReasoningTokens != 80 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestParseUsageRejectsPayloadsWithoutUsage(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		body     string
	}{
		{name: "empty", protocol: "openai", body: ""},
		{name: "content chunk", protocol: "openai", body: `data: {"choices":[{"delta":{"content":"hi"}}]}`},
		{name: "claude text delta", protocol: "claude", body: `data: {"type":"content_block_delta","delta":{"text":"hi"}}`},
		{name: "gemini candidate only", protocol: "gemini", body: `data: {"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`},
		{name: "garbage", protocol: "openai", body: "not json at all"},
		{name: "done marker", protocol: "openai", body: "data: [DONE]"},
		{name: "usage word but no block", protocol: "openai", body: `{"note":"usage is not here"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, ok := ParseUsage([]byte(test.body)); ok {
				t.Fatal("ParseUsage reported usage where there is none")
			}
		})
	}
}

// TestParseUsageIdentifiesTheLayoutFromTheKeys is the rule that keeps a
// mislabelled payload from being mispriced. ParseUsage takes no protocol at
// all, so each of these is read purely from the keys it carries — the bodies
// here are the ones that used to be misread when the label decided.
func TestParseUsageIdentifiesTheLayoutFromTheKeys(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		semantics Semantics
		input     int64
		cacheRead int64
	}{{
		name:      "openai chat completions",
		body:      `{"usage":{"prompt_tokens":1000,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":800}}}`,
		semantics: SemanticsSubset,
		input:     1000,
		cacheRead: 800,
	}, {
		// The shape that caused the overcharge: read as Anthropic, its
		// cache-inclusive input_tokens is billed entirely at the input rate.
		name:      "openai responses",
		body:      `{"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":800},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":5}}}`,
		semantics: SemanticsSubset,
		input:     1000,
		cacheRead: 800,
	}, {
		name:      "anthropic",
		body:      `{"usage":{"input_tokens":200,"cache_read_input_tokens":800,"output_tokens":20}}`,
		semantics: SemanticsIndependent,
		input:     200,
		cacheRead: 800,
	}, {
		name:      "gemini",
		body:      `{"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":20,"cachedContentTokenCount":800}}`,
		semantics: SemanticsSeparateReasoning,
		input:     1000,
		cacheRead: 800,
	}, {
		// Neither cache nor reasoning is reported, so no layout is claimed and
		// a self-identifying frame elsewhere in the request gets to decide.
		name:      "bare input and output claim no layout",
		body:      `{"usage":{"input_tokens":1000,"output_tokens":20}}`,
		semantics: "",
		input:     1000,
		cacheRead: 0,
	}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usage, semantics, ok := ParseUsage([]byte(test.body))
			if !ok {
				t.Fatal("ParseUsage reported no usage")
			}
			if semantics != test.semantics {
				t.Fatalf("Semantics = %q, want %q", semantics, test.semantics)
			}
			if usage.InputTokens != test.input || usage.CacheReadTokens != test.cacheRead {
				t.Fatalf("usage = %+v, want input %d cache read %d", usage, test.input, test.cacheRead)
			}
		})
	}
}

// TestParseUsagePrefersGeminiOverAStrayUsageKey keeps real Gemini traffic on the
// Gemini reading even if a payload happens to carry both spellings.
func TestParseUsagePrefersGeminiOverAStrayUsageKey(t *testing.T) {
	body := `{"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":20},"usage":{"prompt_tokens":7}}`
	usage, semantics, ok := ParseUsage([]byte(body))
	if !ok || semantics != SemanticsSeparateReasoning || usage.InputTokens != 1000 {
		t.Fatalf("usage = %+v, semantics = %q, ok = %v", usage, semantics, ok)
	}
}

// TestMergeSemanticsPrefersCacheInsideInput protects the merge across frames.
// MergeMax keeps the largest input total, so once a cache-inclusive frame has
// been seen, the merged total is cache-inclusive and must be billed that way.
func TestMergeSemanticsPrefersCacheInsideInput(t *testing.T) {
	if got := mergeSemantics(SemanticsIndependent, SemanticsSubset); got != SemanticsSubset {
		t.Fatalf("MergeSemantics = %q, want %q", got, SemanticsSubset)
	}
	if got := mergeSemantics(SemanticsSubset, SemanticsIndependent); got != SemanticsSubset {
		t.Fatalf("MergeSemantics = %q, want the cache-inclusive reading to win", got)
	}
	if got := mergeSemantics("", SemanticsIndependent); got != SemanticsIndependent {
		t.Fatalf("MergeSemantics = %q, want the first observation to be taken", got)
	}
	if got := mergeSemantics(SemanticsIndependent, ""); got != SemanticsIndependent {
		t.Fatalf("MergeSemantics = %q, want an empty observation ignored", got)
	}
}

func TestParseUsageAcceptsStringCounts(t *testing.T) {
	usage, _, ok := ParseUsage([]byte(`{"usageMetadata":{"promptTokenCount":"1200","candidatesTokenCount":"340"}}`))
	if !ok {
		t.Fatal("ParseUsage reported no usage")
	}
	if usage.InputTokens != 1200 || usage.OutputTokens != 340 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestMergeMaxTakesTheLargerOfEachCounter(t *testing.T) {
	base := TokenUsage{InputTokens: 100, OutputTokens: 5, CacheReadTokens: 40}
	base.MergeMax(TokenUsage{InputTokens: 0, OutputTokens: 500, CacheCreationTokens: 10})
	want := TokenUsage{InputTokens: 100, OutputTokens: 500, CacheReadTokens: 40, CacheCreationTokens: 10}
	if base != want {
		t.Fatalf("merged = %+v, want %+v", base, want)
	}
}

// TestMergeMaxLetsTheCacheSplitDecideTheInput covers the one counter that is not
// merged by size. An observation that reports no cache bucket is reporting the
// whole input; one that reports a cache bucket is reporting the rest of it after
// the cache is taken out. Comparing them by size charges the cached tokens
// twice, which is what a Claude client on a Codex upstream was billed, because
// CLIProxyAPI fills message_start in with a locally tokenized count of the whole
// prompt before the upstream reports anything.
func TestMergeMaxLetsTheCacheSplitDecideTheInput(t *testing.T) {
	// The proxy's estimate of the whole prompt, ahead of the real counters.
	estimated := TokenUsage{InputTokens: 23500}
	// What the upstream reported, in Anthropic's cache-exclusive layout.
	reported := TokenUsage{InputTokens: 2772, OutputTokens: 500, CachedTokens: 20000, CacheReadTokens: 20000}

	merged := estimated
	merged.MergeMax(reported)
	if merged.InputTokens != 2772 || merged.CacheReadTokens != 20000 {
		t.Fatalf("merged = %+v, want the reported split to decide the input", merged)
	}

	// The same holds when the bare observation arrives second, as a stream that
	// repeats a header frame would deliver it.
	merged = reported
	merged.MergeMax(estimated)
	if merged.InputTokens != 2772 || merged.CacheReadTokens != 20000 {
		t.Fatalf("merged = %+v, want the bare observation ignored", merged)
	}

	// With no cache anywhere there is nothing to charge twice, so size decides.
	merged = TokenUsage{InputTokens: 1200}
	merged.MergeMax(TokenUsage{InputTokens: 1500})
	if merged.InputTokens != 1500 {
		t.Fatalf("InputTokens = %d, want 1500", merged.InputTokens)
	}
}

// TestParseUsageReadsTheInteractionsEnvelope covers the shape CLIProxyAPI's
// Interactions translators emit. The counters sit under an "interaction"
// wrapper and are spelled "total_*", so before this was handled these responses
// parsed as no usage at all and billed nothing.
//
// The same names carry a different layout depending on which upstream produced
// them, which is why each case asserts the layout derived from the numbers.
func TestParseUsageReadsTheInteractionsEnvelope(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantUsage     TokenUsage
		wantSemantics Semantics
	}{
		{
			// Verbatim from CLIProxyAPI's own translator test. Gemini upstream:
			// the total only balances as 123+36+240, so thoughts sit outside the
			// output count and must be billed on top of it.
			name: "gemini upstream reports thoughts outside the output",
			body: `data: {"interaction":{"id":"interaction_1","status":"completed",` +
				`"usage":{"total_tokens":399,"total_input_tokens":123,"total_cached_tokens":5,` +
				`"total_output_tokens":36,"total_thought_tokens":240},"object":"interaction"},` +
				`"event_type":"interaction.completed"}`,
			wantUsage: TokenUsage{
				InputTokens: 123, OutputTokens: 36, ReasoningTokens: 240,
				CachedTokens: 5, CacheReadTokens: 5, TotalTokens: 399,
			},
			wantSemantics: SemanticsSeparateReasoning,
		},
		{
			// Responses upstream: the total is input+output, so reasoning is
			// already inside the output count. Reporting it would bill it twice.
			name: "responses upstream reports reasoning inside the output",
			body: `{"interaction":{"usage":{"total_tokens":1100,"total_input_tokens":1000,` +
				`"total_cached_tokens":800,"total_output_tokens":100,"total_thought_tokens":40}}}`,
			wantUsage: TokenUsage{
				InputTokens: 1000, OutputTokens: 100,
				CachedTokens: 800, CacheReadTokens: 800, TotalTokens: 1100,
			},
			wantSemantics: "",
		},
		{
			// Anthropic upstream: a cache count larger than the input count
			// proves the two are reported independently.
			name: "anthropic upstream reports cache outside the input",
			body: `{"interaction":{"usage":{"total_tokens":1100,"total_input_tokens":1000,` +
				`"total_cached_tokens":5000,"total_output_tokens":100}}}`,
			wantUsage: TokenUsage{
				InputTokens: 1000, OutputTokens: 100,
				CachedTokens: 5000, CacheReadTokens: 5000, TotalTokens: 1100,
			},
			wantSemantics: SemanticsIndependent,
		},
		{
			// The non-streaming envelope puts usage at the top level and uses
			// the plain spellings.
			name: "non-streaming envelope with plain spellings",
			body: `{"id":"interaction_1","object":"interaction","status":"completed",` +
				`"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`,
			wantUsage:     TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
			wantSemantics: "",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			usage, semantics, ok := ParseUsage([]byte(testCase.body))
			if !ok {
				t.Fatal("ParseUsage reported no usage")
			}
			if usage != testCase.wantUsage {
				t.Fatalf("usage = %+v, want %+v", usage, testCase.wantUsage)
			}
			if semantics != testCase.wantSemantics {
				t.Fatalf("semantics = %q, want %q", semantics, testCase.wantSemantics)
			}
		})
	}
}

// TestInteractionsGeminiUsageBillsThoughtsOnTop pins the money consequence of
// the case above: thoughts dwarf the visible output in a reasoning model, so
// dropping them from the bill is not a rounding error.
func TestInteractionsGeminiUsageBillsThoughtsOnTop(t *testing.T) {
	body := []byte(`{"interaction":{"usage":{"total_tokens":399,"total_input_tokens":123,` +
		`"total_cached_tokens":5,"total_output_tokens":36,"total_thought_tokens":240}}}`)
	usage, semantics, ok := ParseUsage(body)
	if !ok {
		t.Fatal("ParseUsage reported no usage")
	}
	price := Price{InputPer1M: 1, OutputPer1M: 10, CacheReadPer1M: 0.1, CacheWritePer1M: 1}
	cost := ComputeCost(price, semantics, usage)

	// 118 uncached input, 5 cache reads, and 36+240 output tokens.
	if cost.BilledOutputTokens != 276 {
		t.Fatalf("billed output = %d, want 276", cost.BilledOutputTokens)
	}
	if cost.UncachedInputTokens != 118 {
		t.Fatalf("uncached input = %d, want 118", cost.UncachedInputTokens)
	}
	want := 118*1e-6 + 5*0.1e-6 + 276*10e-6
	if diff := cost.TotalUSD - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("total = %.12f, want %.12f", cost.TotalUSD, want)
	}
}

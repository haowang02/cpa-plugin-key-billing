package plugin

import (
	"testing"

	"cpa-key-billing/internal/billing"
)

func TestUpstreamUsageNormalizesGeminiAndInteractions(t *testing.T) {
	tests := []struct {
		name   string
		format string
		body   string
		want   billing.TokenBreakdown
	}{
		{
			name:   "gemini",
			format: "gemini",
			body:   `{"usageMetadata":{"promptTokenCount":10,"toolUsePromptTokenCount":5,"cachedContentTokenCount":4,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":20}}`,
			want: billing.TokenBreakdown{
				SchemaVersion: billing.TokenAccountingSchemaVersion, Quality: billing.TokenAccountingComplete, TotalTokens: 20,
				Input:  billing.TokenInputBreakdown{TotalTokens: 15, UncachedTokens: 11, CacheReadTokens: 4},
				Output: billing.TokenOutputBreakdown{TotalTokens: 5, NonReasoningTokens: 2, ReasoningTokens: 3},
			},
		},
		{
			name:   "interactions",
			format: "interactions",
			body:   `{"interaction":{"id":"int-1","usage":{"total_input_tokens":2,"total_tool_use_tokens":4,"total_output_tokens":6,"total_thought_tokens":3,"total_tokens":15}}}`,
			want: billing.TokenBreakdown{
				SchemaVersion: billing.TokenAccountingSchemaVersion, Quality: billing.TokenAccountingComplete, TotalTokens: 15,
				Input:  billing.TokenInputBreakdown{TotalTokens: 6, UncachedTokens: 6},
				Output: billing.TokenOutputBreakdown{TotalTokens: 9, NonReasoningTokens: 6, ReasoningTokens: 3},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frames := parseUpstreamResponse(test.format, []byte(test.body))
			if len(frames) != 1 || !frames[0].usage.hasUsage() {
				t.Fatalf("frames = %+v", frames)
			}
			if got := frames[0].usage.breakdown(); got != test.want {
				t.Fatalf("breakdown = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestUnknownUpstreamUsageIsNeverBillable(t *testing.T) {
	frames := parseUpstreamResponse("future-format", []byte(`{"id":"future-1","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}`))
	if len(frames) != 1 {
		t.Fatalf("frames = %+v", frames)
	}
	breakdown := frames[0].usage.breakdown()
	if breakdown.Quality != billing.TokenAccountingUnclassified || breakdown.Billable() {
		t.Fatalf("breakdown = %+v, want unclassified non-billable usage", breakdown)
	}
}

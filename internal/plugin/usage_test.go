package plugin

import (
	"testing"

	"cpa-key-billing/internal/billing"
)

func TestUsageBreakdownMatchesCurrentHostSemantics(t *testing.T) {
	tests := []struct {
		name   string
		record UsageRecord
		want   billing.TokenBreakdown
	}{
		{
			name: "openai subset",
			record: UsageRecord{Provider: "codex", ExecutorType: "CodexExecutor", Detail: UsageDetail{
				InputTokens: 1000, OutputTokens: 500, ReasoningTokens: 200,
				CacheReadTokens: 400, CacheCreationTokens: 100, TotalTokens: 1500,
			}},
			want: billing.TokenBreakdown{
				Quality: billing.TokenAccountingComplete, TotalTokens: 1500,
				Input:  billing.TokenInputBreakdown{TotalTokens: 1000, UncachedTokens: 500, CacheReadTokens: 400, CacheWriteTokens: 100},
				Output: billing.TokenOutputBreakdown{TotalTokens: 500, NonReasoningTokens: 300, ReasoningTokens: 200},
			},
		},
		{
			name: "openai compatible executor takes precedence",
			record: UsageRecord{Provider: "anthropic", ExecutorType: "OpenAICompatExecutor", Detail: UsageDetail{
				InputTokens: 100, OutputTokens: 30, ReasoningTokens: 12,
				CacheReadTokens: 40, CacheCreationTokens: 10, TotalTokens: 130,
			}},
			want: billing.TokenBreakdown{
				Quality: billing.TokenAccountingComplete, TotalTokens: 130,
				Input:  billing.TokenInputBreakdown{TotalTokens: 100, UncachedTokens: 50, CacheReadTokens: 40, CacheWriteTokens: 10},
				Output: billing.TokenOutputBreakdown{TotalTokens: 30, NonReasoningTokens: 18, ReasoningTokens: 12},
			},
		},
		{
			name: "claude independent cache",
			record: UsageRecord{Provider: "claude", ExecutorType: "ClaudeExecutor", Detail: UsageDetail{
				InputTokens: 500, OutputTokens: 500, ReasoningTokens: 200,
				CacheReadTokens: 400, CacheCreationTokens: 100, TotalTokens: 1500,
			}},
			want: billing.TokenBreakdown{
				Quality: billing.TokenAccountingComplete, TotalTokens: 1500,
				Input:  billing.TokenInputBreakdown{TotalTokens: 1000, UncachedTokens: 500, CacheReadTokens: 400, CacheWriteTokens: 100},
				Output: billing.TokenOutputBreakdown{TotalTokens: 500, NonReasoningTokens: 300, ReasoningTokens: 200},
			},
		},
		{
			name: "direct anthropic record with separate reasoning",
			record: UsageRecord{Provider: "anthropic", Detail: UsageDetail{
				InputTokens: 100, OutputTokens: 30, ReasoningTokens: 12,
				CacheReadTokens: 40, CacheCreationTokens: 10, TotalTokens: 192,
			}},
			want: billing.TokenBreakdown{
				Quality: billing.TokenAccountingComplete, TotalTokens: 192,
				Input:  billing.TokenInputBreakdown{TotalTokens: 150, UncachedTokens: 100, CacheReadTokens: 40, CacheWriteTokens: 10},
				Output: billing.TokenOutputBreakdown{TotalTokens: 42, NonReasoningTokens: 30, ReasoningTokens: 12},
			},
		},
		{
			name: "gemini separate reasoning",
			record: UsageRecord{Provider: "gemini", ExecutorType: "GeminiExecutor", Detail: UsageDetail{
				InputTokens: 15, OutputTokens: 2, ReasoningTokens: 3,
				CacheReadTokens: 4, TotalTokens: 20,
			}},
			want: billing.TokenBreakdown{
				Quality: billing.TokenAccountingComplete, TotalTokens: 20,
				Input:  billing.TokenInputBreakdown{TotalTokens: 15, UncachedTokens: 11, CacheReadTokens: 4},
				Output: billing.TokenOutputBreakdown{TotalTokens: 5, NonReasoningTokens: 2, ReasoningTokens: 3},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := usageBreakdown(test.record); got != test.want {
				t.Fatalf("breakdown = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestUsageBreakdownHandlesUnknownOrContradictoryRecords(t *testing.T) {
	unknown := usageBreakdown(UsageRecord{Provider: "future-provider", Detail: UsageDetail{
		InputTokens: 100, OutputTokens: 20, TotalTokens: 120,
	}})
	if unknown.Quality != billing.TokenAccountingUnclassified || unknown.UnclassifiedTokens != 120 || unknown.Billable() {
		t.Fatalf("unknown breakdown = %+v", unknown)
	}
	unknownRemainder := usageBreakdown(UsageRecord{Provider: "future-provider", Detail: UsageDetail{
		InputTokens: 100, OutputTokens: 20, TotalTokens: 125,
	}})
	if unknownRemainder.Quality != billing.TokenAccountingUnclassified || unknownRemainder.UnclassifiedTokens != 125 ||
		unknownRemainder.Billable() {
		t.Fatalf("unknown breakdown with remainder = %+v", unknownRemainder)
	}

	contradictory := usageBreakdown(UsageRecord{Provider: "openai", Detail: UsageDetail{
		InputTokens: 10, OutputTokens: 5, ReasoningTokens: 8, TotalTokens: 15,
	}})
	if contradictory.Quality != billing.TokenAccountingInconsistent || contradictory.Billable() {
		t.Fatalf("contradictory breakdown = %+v", contradictory)
	}
	contradictoryTotal := usageBreakdown(UsageRecord{Provider: "openai", Detail: UsageDetail{
		InputTokens: 10, OutputTokens: 5, TotalTokens: 20,
	}})
	if contradictoryTotal.Quality != billing.TokenAccountingInconsistent || contradictoryTotal.Billable() {
		t.Fatalf("contradictory total breakdown = %+v", contradictoryTotal)
	}

	if zero := usageBreakdown(UsageRecord{Provider: "openai"}); zero != (billing.TokenBreakdown{}) {
		t.Fatalf("zero breakdown = %+v, want an unmeasured record", zero)
	}
}

func TestUsageBreakdownPreservesPartialOpenAIUsage(t *testing.T) {
	totalOnly := usageBreakdown(UsageRecord{Provider: "openai", Detail: UsageDetail{TotalTokens: 120}})
	if totalOnly.Quality != billing.TokenAccountingUnclassified || totalOnly.TotalTokens != 120 || totalOnly.UnclassifiedTokens != 120 {
		t.Fatalf("total-only breakdown = %+v", totalOnly)
	}

	partial := usageBreakdown(UsageRecord{Provider: "openai", Detail: UsageDetail{OutputTokens: 20, TotalTokens: 120}})
	if partial.Quality != billing.TokenAccountingUnclassified || partial.Output.TotalTokens != 20 ||
		partial.UnclassifiedTokens != 100 || partial.Billable() {
		t.Fatalf("partial breakdown = %+v", partial)
	}

	missingParents := usageBreakdown(UsageRecord{Provider: "openai", Detail: UsageDetail{
		CacheReadTokens: 30, ReasoningTokens: 10, TotalTokens: 120,
	}})
	if missingParents.Quality != billing.TokenAccountingUnclassified || missingParents.TotalTokens != 120 ||
		missingParents.UnclassifiedTokens != 120 {
		t.Fatalf("missing-parent breakdown = %+v", missingParents)
	}

	missingInputWithoutTotal := usageBreakdown(UsageRecord{Provider: "openai", Detail: UsageDetail{
		OutputTokens: 20, CacheReadTokens: 30,
	}})
	if missingInputWithoutTotal.Quality != billing.TokenAccountingComplete ||
		missingInputWithoutTotal.Output.NonReasoningTokens != 20 || !missingInputWithoutTotal.Billable() {
		t.Fatalf("missing-input breakdown without total = %+v", missingInputWithoutTotal)
	}
}

func TestUsageSemanticsCoverBuiltinExecutors(t *testing.T) {
	tests := []struct {
		provider string
		executor string
		want     tokenSemantics
	}{
		{"codex", "CodexExecutor", tokenSemanticsSubset},
		{"claude", "ClaudeExecutor", tokenSemanticsIndependent},
		{"kimi", "KimiExecutor", tokenSemanticsSubset},
		{"xai", "XAIExecutor", tokenSemanticsSubset},
		{"gemini", "GeminiExecutor", tokenSemanticsSeparateReasoning},
		{"gemini-interactions", "GeminiExecutor", tokenSemanticsSeparateReasoning},
		{"vertex", "GeminiVertexExecutor", tokenSemanticsSeparateReasoning},
		{"aistudio", "AIStudioExecutor", tokenSemanticsSeparateReasoning},
		{"antigravity", "AntigravityExecutor", tokenSemanticsSeparateReasoning},
		{"custom", "OpenAICompatExecutor", tokenSemanticsSubset},
	}
	for _, test := range tests {
		if got := usageSemantics(test.provider, test.executor); got != test.want {
			t.Errorf("%s/%s semantics = %d, want %d", test.provider, test.executor, got, test.want)
		}
	}
}

func TestUsageBreakdownMatchesCurrentHostForAuxiliaryOnlyRecords(t *testing.T) {
	tests := []struct {
		name   string
		record UsageRecord
		want   billing.TokenBreakdown
	}{
		{
			name:   "openai cached only is unclassified",
			record: UsageRecord{Provider: "openai", Detail: UsageDetail{CachedTokens: 13}},
			want: billing.TokenBreakdown{
				Quality: billing.TokenAccountingUnclassified, TotalTokens: 13, UnclassifiedTokens: 13,
			},
		},
		{
			name:   "openai reasoning only is unclassified",
			record: UsageRecord{Provider: "openai", Detail: UsageDetail{ReasoningTokens: 12}},
			want: billing.TokenBreakdown{
				Quality: billing.TokenAccountingUnclassified, TotalTokens: 12, UnclassifiedTokens: 12,
			},
		},
		{
			name:   "gemini reasoning only is complete",
			record: UsageRecord{Provider: "gemini", Detail: UsageDetail{ReasoningTokens: 12}},
			want: billing.TokenBreakdown{
				Quality: billing.TokenAccountingComplete, TotalTokens: 12,
				Output: billing.TokenOutputBreakdown{TotalTokens: 12, ReasoningTokens: 12},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := usageBreakdown(test.record); got != test.want {
				t.Fatalf("breakdown = %+v, want %+v", got, test.want)
			}
		})
	}
}

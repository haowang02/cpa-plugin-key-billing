package plugin

import (
	"encoding/json"
	"reflect"
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
		{
			name: "Meta Responses accounting",
			record: UsageRecord{Provider: "meta", ExecutorType: "MetaExecutor", Detail: UsageDetail{
				InputTokens: 100, CacheReadTokens: 40, OutputTokens: 50, ReasoningTokens: 20, TotalTokens: 150,
			}},
			want: billing.TokenBreakdown{
				Quality: billing.TokenAccountingComplete, TotalTokens: 150,
				Input:  billing.TokenInputBreakdown{TotalTokens: 100, UncachedTokens: 60, CacheReadTokens: 40},
				Output: billing.TokenOutputBreakdown{TotalTokens: 50, NonReasoningTokens: 30, ReasoningTokens: 20},
			},
		},
		{
			name: "Devin Interactions accounting",
			record: UsageRecord{Provider: "devin", ExecutorType: "DevinExecutor", Detail: UsageDetail{
				InputTokens: 100, CacheReadTokens: 40, OutputTokens: 50, ReasoningTokens: 20, TotalTokens: 170,
			}},
			want: billing.TokenBreakdown{
				Quality: billing.TokenAccountingComplete, TotalTokens: 170,
				Input:  billing.TokenInputBreakdown{TotalTokens: 100, UncachedTokens: 60, CacheReadTokens: 40},
				Output: billing.TokenOutputBreakdown{TotalTokens: 70, NonReasoningTokens: 50, ReasoningTokens: 20},
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
	// Every semantics agrees when no count depends on it, as with plain plugin executor usage.
	agreed := usageBreakdown(UsageRecord{Provider: "freetier", ExecutorType: "executorAdapter", Detail: UsageDetail{
		InputTokens: 100, OutputTokens: 20, TotalTokens: 120,
	}})
	if agreed.Quality != billing.TokenAccountingComplete || agreed.Input.UncachedTokens != 100 ||
		agreed.Output.NonReasoningTokens != 20 || !agreed.Billable() {
		t.Fatalf("agreed breakdown = %+v", agreed)
	}
	// Without a total, reasoning may be part of the output or separate from it.
	ambiguous := usageBreakdown(UsageRecord{Provider: "future-provider", Detail: UsageDetail{
		InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5,
	}})
	if ambiguous.Quality != billing.TokenAccountingUnclassified || ambiguous.Billable() {
		t.Fatalf("ambiguous breakdown = %+v", ambiguous)
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

func TestUsageBreakdownOptionalDetails(t *testing.T) {
	count := func(value int64) *int64 { return &value }
	for _, test := range []struct {
		name         string
		json         string
		want         billing.TokenDetail
		inconsistent bool
	}{
		{name: "absent", json: `{}`},
		{name: "null", json: `{"CacheCreation5mTokens":null,"CacheCreation1hTokens":null,"ImageInputTokens":null,"CachedImageTokens":null,"CachedTextTokens":null,"ImageOutputTokens":null}`},
		{
			name: "zero", json: `{
				"CacheCreation5mTokens":0,"CacheCreation1hTokens":0,
				"ImageInputTokens":0,"CachedImageTokens":0,"ImageOutputTokens":0
			}`,
			want: billing.TokenDetail{CacheWrite5mTokens: count(0), CacheWrite1hTokens: count(0), ImageInputTokens: count(0), ImageCacheReadTokens: count(0), ImageOutputTokens: count(0)},
		},
		{
			name: "image and TTL subsets", json: `{
				"CacheCreation5mTokens":5,"CacheCreation1hTokens":15,
				"ImageInputTokens":70,"CachedImageTokens":30,"ImageOutputTokens":8
			}`,
			want: billing.TokenDetail{CacheWrite5mTokens: count(5), CacheWrite1hTokens: count(15), ImageInputTokens: count(40), ImageCacheReadTokens: count(30), ImageOutputTokens: count(8)},
		},
		{
			name: "known text cache", json: `{"ImageInputTokens":50,"CachedTextTokens":40}`,
			want: billing.TokenDetail{ImageInputTokens: count(50), ImageCacheReadTokens: count(0)},
		},
		{
			name: "unknown image cache", json: `{"ImageInputTokens":50,"ImageOutputTokens":8}`,
			want: billing.TokenDetail{ImageOutputTokens: count(8)},
		},
		{
			name: "partial TTL", json: `{"CacheCreation5mTokens":5,"CacheCreation1hTokens":10}`,
			want: billing.TokenDetail{CacheWrite5mTokens: count(5), CacheWrite1hTokens: count(10)},
		},
		{name: "negative details", json: `{"CacheCreation5mTokens":-1,"ImageInputTokens":-1,"CachedImageTokens":0,"ImageOutputTokens":-1}`},
		{name: "overlapping subsets", json: `{"CacheCreation5mTokens":11,"CacheCreation1hTokens":10,"ImageInputTokens":20,"CachedImageTokens":21,"ImageOutputTokens":9}`},
		{name: "exceeds parents", json: `{"ImageInputTokens":81,"CachedImageTokens":0}`},
		{name: "cached image exceeds parent", json: `{"ImageInputTokens":50,"CachedImageTokens":41}`},
		{name: "overflowing TTL", json: `{"CacheCreation5mTokens":9223372036854775807,"CacheCreation1hTokens":9223372036854775807}`},
		{name: "inconsistent parent", inconsistent: true, json: `{"TotalTokens":1,"ImageOutputTokens":8}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			detail := UsageDetail{InputTokens: 140, CacheReadTokens: 40, CacheCreationTokens: 20, OutputTokens: 10, ReasoningTokens: 2}
			if err := json.Unmarshal([]byte(test.json), &detail); err != nil {
				t.Fatal(err)
			}
			got := usageBreakdown(UsageRecord{Provider: "codex", Detail: detail})
			quality := billing.TokenAccountingComplete
			if test.inconsistent {
				quality = billing.TokenAccountingInconsistent
			}
			if got.Quality != quality || !got.Valid() || !reflect.DeepEqual(got.Detail, test.want) {
				t.Fatalf("breakdown = %+v, want quality %s and detail %+v", got, quality, test.want)
			}
		})
	}
}

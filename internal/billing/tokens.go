package billing

type TokenAccountingQuality string

const (
	TokenAccountingComplete     TokenAccountingQuality = "complete"
	TokenAccountingInconsistent TokenAccountingQuality = "inconsistent"
	TokenAccountingUnclassified TokenAccountingQuality = "unclassified"
)

type TokenInputBreakdown struct {
	TotalTokens      int64
	UncachedTokens   int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

type TokenOutputBreakdown struct {
	TotalTokens        int64
	NonReasoningTokens int64
	ReasoningTokens    int64
}

// ImageInputTokens excludes cached images; nil counters mean unreported detail.
type TokenDetail struct {
	CacheWrite5mTokens   *int64
	CacheWrite1hTokens   *int64
	ImageInputTokens     *int64
	ImageCacheReadTokens *int64
	ImageOutputTokens    *int64
}

// Provider usage is normalized into these non-overlapping buckets before pricing.
type TokenBreakdown struct {
	Quality            TokenAccountingQuality
	TotalTokens        int64
	Input              TokenInputBreakdown
	Output             TokenOutputBreakdown
	UnclassifiedTokens int64
	Detail             TokenDetail
}

func (b TokenBreakdown) Valid() bool {
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
	d := b.Detail
	return (b.Quality != TokenAccountingComplete || b.UnclassifiedTokens == 0) &&
		validTokenCount(d.CacheWrite5mTokens, b.Input.CacheWriteTokens) &&
		validTokenCount(d.CacheWrite1hTokens, b.Input.CacheWriteTokens-tokenCount(d.CacheWrite5mTokens)) &&
		validTokenCount(d.ImageInputTokens, b.Input.UncachedTokens) &&
		validTokenCount(d.ImageCacheReadTokens, b.Input.CacheReadTokens) &&
		validTokenCount(d.ImageOutputTokens, b.Output.NonReasoningTokens)
}

func (b TokenBreakdown) Billable() bool {
	return b.Valid() && b.Quality == TokenAccountingComplete
}

func tokenCount(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func validTokenCount(count *int64, total int64) bool {
	return count == nil || *count >= 0 && *count <= total
}

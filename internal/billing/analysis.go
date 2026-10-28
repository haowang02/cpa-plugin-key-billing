package billing

import "time"

type AnalysisTrendPoint struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}

type AnalysisTrends struct {
	Requests            []AnalysisTrendPoint `json:"requests"`
	TotalTokens         []AnalysisTrendPoint `json:"total_tokens"`
	UncachedInputTokens []AnalysisTrendPoint `json:"input_tokens"`
	OutputTokens        []AnalysisTrendPoint `json:"output_tokens"`
	CacheReadTokens     []AnalysisTrendPoint `json:"cache_read_tokens"`
	CacheWriteTokens    []AnalysisTrendPoint `json:"cache_write_tokens"`
	CacheRate           []AnalysisTrendPoint `json:"cache_rate"`
	TotalCost           []AnalysisTrendPoint `json:"total_cost"`
}

// Costs and ChargedIn are as in CredentialUsageStats.
type AnalysisComposition struct {
	Key              string   `json:"key"`
	Label            string   `json:"label"`
	Preview          string   `json:"preview,omitempty"`
	TotalTokens      int64    `json:"total_tokens"`
	InputTokens      int64    `json:"input_tokens"`
	OutputTokens     int64    `json:"output_tokens"`
	CacheReadTokens  int64    `json:"cache_read_tokens"`
	CacheWriteTokens int64    `json:"cache_write_tokens"`
	Requests         int64    `json:"requests"`
	CostUSD          float64  `json:"cost_usd"`
	ChargedIn        Currency `json:"charged_in,omitempty"`
	Percent          float64  `json:"percent"`
	// Filter names a source row in usage queries without revealing it.
	Filter string `json:"filter,omitempty"`
}

type UsageDistribution struct {
	APIKeys []AnalysisComposition `json:"api_keys"`
	Models  []AnalysisComposition `json:"models"`
	Sources []AnalysisComposition `json:"sources"`
}

// Costs and ChargedIn are as in CredentialUsageStats; the cost trend shares them.
type AnalysisCostSummary struct {
	TotalUSD      float64  `json:"total_usd"`
	InputUSD      float64  `json:"input_usd"`
	CacheReadUSD  float64  `json:"cache_read_usd"`
	CacheWriteUSD float64  `json:"cache_write_usd"`
	OutputUSD     float64  `json:"output_usd"`
	ChargedIn     Currency `json:"charged_in,omitempty"`
}

type AnalysisSummary struct {
	Requests         int64               `json:"requests"`
	Succeeded        int64               `json:"succeeded"`
	Failed           int64               `json:"failed"`
	SuccessRate      float64             `json:"success_rate"`
	TotalTokens      int64               `json:"total_tokens"`
	InputTokens      int64               `json:"input_tokens"`
	OutputTokens     int64               `json:"output_tokens"`
	CacheReadTokens  int64               `json:"cache_read_tokens"`
	CacheWriteTokens int64               `json:"cache_write_tokens"`
	CacheRate        float64             `json:"cache_rate"`
	Cost             AnalysisCostSummary `json:"cost"`
}

type AnalysisView struct {
	Summary           AnalysisSummary   `json:"summary"`
	Trends            AnalysisTrends    `json:"trends"`
	UsageDistribution UsageDistribution `json:"usage_distribution"`
}

// SpeedStats summarizes TPS as the request list measures it: output tokens of
// successful, fully accounted requests over total latency. Interpolated
// quantiles are zero without samples.
type SpeedStats struct {
	Samples int     `json:"samples"`
	P10     float64 `json:"p10"`
	P25     float64 `json:"p25"`
	P50     float64 `json:"p50"`
	P75     float64 `json:"p75"`
	P90     float64 `json:"p90"`
}

type SpeedTrendPoint struct {
	Time time.Time `json:"time"`
	SpeedStats
}

type AnalysisSpeedView struct {
	Summary SpeedStats        `json:"summary"`
	Trend   []SpeedTrendPoint `json:"trend"`
}

func (s *Store) Analysis(query RequestEventQuery) (AnalysisView, error) {
	view, err := readAnalysis(s, query, func(repo Repository, query RequestEventQuery, since time.Time) (AnalysisView, error) {
		return repo.Analysis(query, s.cfg.CNYPerUSD, since)
	})
	if view.UsageDistribution.APIKeys == nil {
		view.UsageDistribution.APIKeys = []AnalysisComposition{}
	}
	if view.UsageDistribution.Models == nil {
		view.UsageDistribution.Models = []AnalysisComposition{}
	}
	if view.UsageDistribution.Sources == nil {
		view.UsageDistribution.Sources = []AnalysisComposition{}
	}
	return view, err
}

func (s *Store) AnalysisSpeed(query RequestEventQuery) (AnalysisSpeedView, error) {
	view, err := readAnalysis(s, query, Repository.AnalysisSpeed)
	if view.Trend == nil {
		view.Trend = []SpeedTrendPoint{}
	}
	return view, err
}

func readAnalysis[T any](s *Store, query RequestEventQuery, read func(Repository, RequestEventQuery, time.Time) (T, error)) (T, error) {
	now := s.Now()
	from, to, err := usageTimeRange(query.From, query.To, now)
	if err != nil {
		var zero T
		return zero, err
	}
	query.From, query.To = from, to
	return withRepository(s, func(repo Repository) (T, error) {
		return read(repo, query, now.Add(-RequestEventRetention))
	})
}

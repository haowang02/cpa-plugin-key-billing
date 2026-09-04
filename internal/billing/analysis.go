package billing

import "time"

type AnalysisComposition struct {
	Key           string  `json:"key"`
	Label         string  `json:"label"`
	TotalTokens   int64   `json:"total_tokens"`
	Requests      int64   `json:"requests"`
	CostUSD       float64 `json:"cost_usd"`
	CostAvailable bool    `json:"cost_available"`
	Percent       float64 `json:"percent"`
}

type UsageDistribution struct {
	APIKeys []AnalysisComposition `json:"api_keys"`
	Models  []AnalysisComposition `json:"models"`
	Sources []AnalysisComposition `json:"sources"`
}

type AnalysisCostPart struct {
	USD float64 `json:"usd"`
}

type AnalysisCostSummary struct {
	Available  bool             `json:"available"`
	TotalUSD   float64          `json:"total_usd"`
	Input      AnalysisCostPart `json:"input"`
	CacheRead  AnalysisCostPart `json:"cache_read"`
	CacheWrite AnalysisCostPart `json:"cache_write"`
	Output     AnalysisCostPart `json:"output"`
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
	UsageDistribution UsageDistribution `json:"usage_distribution"`
}

func (s *Store) Analysis(query RequestEventQuery) (AnalysisView, error) {
	now := s.Now()
	from, to := effectiveAnalysisRange(query, now)
	if !from.Before(to) {
		return AnalysisView{}, invalidf("分析范围不在当前请求事件保留期内")
	}
	query.From, query.To = from, to
	view, err := withRepository(s, func(repo Repository) (AnalysisView, error) {
		return repo.Analysis(query, now.Add(-RequestEventRetention))
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

func effectiveAnalysisRange(query RequestEventQuery, now time.Time) (time.Time, time.Time) {
	to := query.To
	if to.IsZero() || to.After(now) {
		to = now
	}
	from := query.From
	if from.IsZero() {
		from = to.Add(-30 * 24 * time.Hour)
	}
	cutoff := now.Add(-RequestEventRetention)
	if from.Before(cutoff) {
		from = cutoff
	}
	return from, to
}

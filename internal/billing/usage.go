package billing

import "time"

// Costs sum in USD at the configured rate. ChargedIn is the one currency of every summed cost, empty when
// they mix or cost nothing.
type CredentialUsageStats struct {
	Requests        int64    `json:"requests"`
	Failed          int64    `json:"failed"`
	InputTokens     int64    `json:"input_tokens"`
	CacheReadTokens int64    `json:"cache_read_tokens"`
	OutputTokens    int64    `json:"output_tokens"`
	CostUSD         float64  `json:"cost_usd"`
	ChargedIn       Currency `json:"charged_in,omitempty"`
}

// Most tokens first; InputTokens include cached ones.
type ModelUsage struct {
	Model            string   `json:"model"`
	Requests         int64    `json:"requests"`
	InputTokens      int64    `json:"input_tokens"`
	OutputTokens     int64    `json:"output_tokens"`
	CacheReadTokens  int64    `json:"cache_read_tokens"`
	CacheWriteTokens int64    `json:"cache_write_tokens"`
	CostUSD          float64  `json:"cost_usd"`
	ChargedIn        Currency `json:"charged_in,omitempty"`
}

type UsageTotal struct {
	Tokens    int64    `json:"tokens"`
	CostUSD   float64  `json:"cost_usd"`
	ChargedIn Currency `json:"charged_in,omitempty"`
}

// UsageQuery selects requests from From until To matching every set field; a list matches any of its values.
type UsageQuery struct {
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	AuthIndex string    `json:"auth_index,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	ModelMatch
	KeyScopes   []string `json:"-"`
	Models      []string `json:"-"`
	Credentials []string `json:"-"`
}

// An empty Include matches every model; matching ignores case.
type ModelMatch struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

func (s *Store) CredentialUsageStats(authIndexes []string, scope string, from, to time.Time) (map[string]CredentialUsageStats, error) {
	return withRepository(s, func(repo Repository) (map[string]CredentialUsageStats, error) {
		from, to, err := s.cfg.usageTimeRange(from, to, s.Now())
		if err != nil {
			return nil, err
		}
		return repo.CredentialUsageStats(authIndexes, scope, from, to, s.cfg.CNYPerUSD)
	})
}

func (s *Store) UsageTotals(scope string, queries []UsageQuery) ([]UsageTotal, error) {
	return withRepository(s, func(repo Repository) ([]UsageTotal, error) {
		totals := make([]UsageTotal, len(queries))
		cutoff := s.cfg.RequestEventRetentionDays.cutoff(s.Now())
		for i, query := range queries {
			if query.From.Before(cutoff) {
				query.From = cutoff
			}
			total, err := repo.UsageTotal(scope, query, s.cfg.CNYPerUSD)
			if err != nil {
				return nil, err
			}
			totals[i] = total
		}
		return totals, nil
	})
}

func (s *Store) UsageByModel(scope string, query UsageQuery) ([]ModelUsage, error) {
	return withRepository(s, func(repo Repository) ([]ModelUsage, error) {
		var err error
		if query.From, query.To, err = s.cfg.usageTimeRange(query.From, query.To, s.Now()); err != nil {
			return nil, err
		}
		return repo.UsageByModel(scope, query, s.cfg.CNYPerUSD)
	})
}

func (s *Store) UsageCredentials(scope string, query UsageQuery) ([]string, error) {
	return withRepository(s, func(repo Repository) ([]string, error) {
		var err error
		if query.From, query.To, err = s.cfg.usageTimeRange(query.From, query.To, s.Now()); err != nil {
			return nil, err
		}
		return repo.UsageCredentials(scope, query)
	})
}

func (c Config) usageTimeRange(from, to, now time.Time) (time.Time, time.Time, error) {
	if to.IsZero() || to.After(now) {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-30 * 24 * time.Hour)
	}
	if cutoff := c.RequestEventRetentionDays.cutoff(now); from.Before(cutoff) {
		from = cutoff
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, invalidf("The time range is outside the retained request history")
	}
	return from, to, nil
}

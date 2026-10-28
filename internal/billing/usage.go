package billing

import "time"

// Usage costs sum in USD, converting CNY costs at the configured rate.
// ChargedIn is the one currency every cost in a sum was charged in, empty when
// they mix currencies or cost nothing, so that a display can keep it.
type CredentialUsageStats struct {
	Requests        int64    `json:"requests"`
	Failed          int64    `json:"failed"`
	InputTokens     int64    `json:"input_tokens"`
	CacheReadTokens int64    `json:"cache_read_tokens"`
	OutputTokens    int64    `json:"output_tokens"`
	CostUSD         float64  `json:"cost_usd"`
	ChargedIn       Currency `json:"charged_in,omitempty"`
}

// ModelUsage is one model's part of a usage total, most tokens first.
// InputTokens include cached ones.
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

// UsageQuery selects the requests made from From until To that match every
// other field set; a list matches any of its values. Its JSON gives the usage
// endpoint's parameters for the same requests.
type UsageQuery struct {
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	AuthIndex string    `json:"auth_index,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	ModelMatch
	// The usage distribution's rows: keys, the models usage is attributed to,
	// and sources. The endpoint names them by dimension.
	KeyScopes []string `json:"-"`
	Models    []string `json:"-"`
	Sources   []string `json:"-"`
}

// ModelMatch selects upstream models containing one of Include and none of
// Exclude, ignoring case. An empty Include matches every model.
type ModelMatch struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// A non-empty scope limits usage to one API key's requests.
func (s *Store) CredentialUsageStats(authIndexes []string, scope string, from, to time.Time) (map[string]CredentialUsageStats, error) {
	from, to, err := usageTimeRange(from, to, s.Now())
	if err != nil {
		return nil, err
	}
	return withRepository(s, func(repo Repository) (map[string]CredentialUsageStats, error) {
		return repo.CredentialUsageStats(authIndexes, scope, from, to, s.cfg.CNYPerUSD)
	})
}

func (s *Store) UsageTotals(scope string, queries []UsageQuery) ([]UsageTotal, error) {
	return withRepository(s, func(repo Repository) ([]UsageTotal, error) {
		totals := make([]UsageTotal, len(queries))
		for i, query := range queries {
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
	var err error
	if query.From, query.To, err = usageTimeRange(query.From, query.To, s.Now()); err != nil {
		return nil, err
	}
	return withRepository(s, func(repo Repository) ([]ModelUsage, error) {
		return repo.UsageByModel(scope, query, s.cfg.CNYPerUSD)
	})
}

func (s *Store) UsageSources(scope string, query UsageQuery) ([]string, error) {
	var err error
	if query.From, query.To, err = usageTimeRange(query.From, query.To, s.Now()); err != nil {
		return nil, err
	}
	return withRepository(s, func(repo Repository) ([]string, error) {
		return repo.UsageSources(scope, query)
	})
}

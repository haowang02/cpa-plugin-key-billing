package billing

import "time"

type RequestError struct {
	StatusCode int    `json:"status_code,omitempty"`
	ErrorType  string `json:"error_type,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Body       string `json:"body,omitempty"`
}

type RequestErrorEvent struct {
	Event RequestEvent
	Error RequestError
}

// RequestErrorRow is projected from request_errors plus the immutable context
// of its parent request_events row.
type RequestErrorRow struct {
	At            time.Time `json:"at"`
	Scope         string    `json:"scope,omitempty"`
	Preview       string    `json:"preview,omitempty"`
	Label         string    `json:"label,omitempty"`
	AuthIndex     string    `json:"auth_index,omitempty"`
	Source        string    `json:"source,omitempty"`
	Provider      string    `json:"provider,omitempty"`
	ExecutorType  string    `json:"executor_type,omitempty"`
	UpstreamModel string    `json:"upstream_model,omitempty"`
	BillingModel  string    `json:"billing_model,omitempty"`
	LatencyMS     int64     `json:"latency_ms,omitempty"`
	TTFTMS        int64     `json:"ttft_ms,omitempty"`
	RequestError
}

type RequestErrorQuery struct {
	Scope, KeyScope, Model, Source, Executor, Provider, ErrorType string
	StatusCode                                                    int
	From, To                                                      time.Time
	IncludeFilters                                                bool
	Offset, Limit                                                 int
}

type RequestErrorView struct {
	Entries []RequestErrorRow         `json:"entries"`
	Total   int                       `json:"total"`
	Filters *RequestErrorFilterValues `json:"filter_options,omitempty"`
}

type RequestErrorFilterValues struct {
	APIKeys     []APIKeyFilterOption `json:"api_keys"`
	Models      []string             `json:"models"`
	Sources     []string             `json:"sources"`
	Executors   []string             `json:"executors,omitempty"`
	Providers   []string             `json:"providers,omitempty"`
	StatusCodes []int                `json:"status_codes,omitempty"`
	ErrorTypes  []string             `json:"error_types,omitempty"`
}

func (s *Store) RequestErrors(query RequestErrorQuery) (RequestErrorView, error) {
	view, err := withRepository(s, func(repo Repository) (RequestErrorView, error) {
		return repo.RequestErrors(query, s.Now().Add(-RequestEventRetention))
	})
	if view.Entries == nil {
		view.Entries = []RequestErrorRow{}
	}
	return view, err
}

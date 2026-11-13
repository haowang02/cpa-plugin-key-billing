package plugin

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/messages"
)

// quotaMeter selects a credential's requests for a quota window.
// A nil meter leaves usage unknown; a zero meter counts every model.
type quotaMeter struct {
	Models billing.ModelMatch `json:"models,omitzero"`
	// Exclusive windows alone meter their models, so windows over every
	// model leave those models out.
	Exclusive bool `json:"exclusive,omitempty"`
}

// Only Spark's additional limit is known to meter a model; other additional limits and Code Review meter
// traffic the plugin cannot tell apart.
type codexLimit struct {
	key, labelPrefix, model string
}

var (
	codexMainLimit       = codexLimit{key: "codex"}
	codexCodeReviewLimit = codexLimit{key: "code-review", labelPrefix: "Code Review "}
)

func codexAdditionalLimit(name string) codexLimit {
	if name == "" {
		return codexLimit{key: "additional", labelPrefix: "Additional "}
	}
	limit := codexLimit{key: name, labelPrefix: name + " "}
	// Codex names the Spark limit after its model or calls it Spark.
	switch strings.NewReplacer(".", "_", "-", "_", " ", "_").Replace(strings.ToLower(name)) {
	case "gpt_5_3_codex_spark", "codex_spark", "spark":
		limit.model = "gpt-5.3-codex-spark"
	}
	return limit
}

func codexLimitByKey(key string) codexLimit {
	switch unnamed := codexAdditionalLimit(""); key {
	case codexMainLimit.key:
		return codexMainLimit
	case codexCodeReviewLimit.key:
		return codexCodeReviewLimit
	case unnamed.key:
		return unnamed
	}
	return codexAdditionalLimit(key)
}

// Claude reports only its unified limit.
func reportedLimit(provider, key string) (string, *quotaMeter) {
	if provider == "claude" {
		return "", &quotaMeter{}
	}
	limit := codexLimitByKey(key)
	return limit.labelPrefix, limit.meter()
}

func (limit codexLimit) meter() *quotaMeter {
	switch {
	case limit == codexMainLimit:
		return &quotaMeter{}
	case limit.model != "":
		return &quotaMeter{Models: billing.ModelMatch{Include: []string{limit.model}}, Exclusive: true}
	}
	return nil
}

// Claude's unified limit covers every model and is reported on each request.
const claudeUnifiedLimit = "unified"

func claudeModelMeter(family string) *quotaMeter {
	return &quotaMeter{Models: billing.ModelMatch{Include: []string{family}}}
}

func antigravityGroupMeter(label string) *quotaMeter {
	words := strings.FieldsFunc(strings.ToLower(label), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	var models billing.ModelMatch
	switch strings.Join(words, " ") {
	case "gemini", "gemini models":
		// Argon has its own pool.
		models = billing.ModelMatch{Include: []string{"gemini-"}, Exclude: []string{"gemini-4-argon"}}
	case "claude gpt", "claude gpt models", "claude and gpt", "claude and gpt models":
		models.Include = []string{"claude-", "gpt-"}
	case "gemini 4 argon", "gemini 4 argon models":
		models.Include = []string{"gemini-4-argon"}
	default:
		return nil
	}
	return &quotaMeter{Models: models}
}

func periodSeconds(start, resetAt string) int64 {
	from, errFrom := time.Parse(time.RFC3339, strings.TrimSpace(start))
	to, errTo := time.Parse(time.RFC3339, resetAt)
	if errFrom != nil || errTo != nil || !to.After(from) {
		return 0
	}
	return int64(to.Sub(from) / time.Second)
}

const (
	quotaHistoryRange = 30 * 24 * time.Hour
	// Read one more week, the longest reported window, so a period running at the start of the range is read whole.
	quotaHistoryLookback = 7 * 24 * time.Hour
)

type quotaCredential struct {
	authIndex, provider string
}

type quotaWindowHistory struct {
	row     quotaRow
	periods []billing.QuotaWindowPeriod
}

// A response reports only the windows of the limit it used, so a window it leaves out is unchanged, not gone.
func (a *App) quotaWindowHistories(credential quotaCredential, from, now time.Time) ([]quotaWindowHistory, error) {
	windows, err := a.store.QuotaWindowHistories(credential.authIndex, credential.provider, from, now)
	if err != nil {
		return nil, err
	}
	var histories []quotaWindowHistory
	for _, window := range windows {
		labelPrefix, meter := reportedLimit(credential.provider, window.LimitKey)
		if meter == nil {
			continue
		}
		reports := make([]billing.QuotaWindowReport, len(window.Reports))
		for i, report := range window.Reports {
			report.UsedPercent = min(report.UsedPercent, 100)
			report.Plan = codexPlanLabel(report.Plan)
			reports[i] = report
		}
		row := quotaWindow{Seconds: window.Seconds, Meter: meter}.row(window.LimitKey, labelPrefix)
		length := time.Duration(window.Seconds) * time.Second
		histories = append(histories, quotaWindowHistory{row: row, periods: billing.QuotaWindowPeriods(length, reports, now)})
	}
	slices.SortFunc(histories, func(left, right quotaWindowHistory) int { return compareQuotaWindows(left.row, right.row) })
	return histories, nil
}

func compareQuotaWindows(left, right quotaRow) int {
	rank := func(row quotaRow) int {
		if row.LimitKey == codexMainLimit.key || row.LimitKey == claudeUnifiedLimit {
			return 0
		}
		return 1
	}
	return cmp.Or(cmp.Compare(rank(left), rank(right)), strings.Compare(left.LimitKey, right.LimitKey),
		cmp.Compare(left.WindowSeconds, right.WindowSeconds))
}

// A period that began before from is read again from its start, so earlier resets still place it.
func (a *App) completeQuotaHistories(credential quotaCredential, from, now time.Time) ([]quotaWindowHistory, error) {
	start := from.Add(-quotaHistoryLookback)
	for {
		histories, err := a.quotaWindowHistories(credential, start, now)
		if err != nil {
			return nil, err
		}
		earliest := start
		for _, history := range histories {
			index := slices.IndexFunc(history.periods, func(period billing.QuotaWindowPeriod) bool { return period.EndsAfter(from) })
			if index >= 0 && history.periods[index].Start.Before(earliest) {
				earliest = history.periods[index].Start
			}
		}
		if !earliest.Before(start) {
			return histories, nil
		}
		start = earliest
	}
}

func (row quotaRow) sameWindow(other quotaRow) bool {
	return row.LimitKey == other.LimitKey && row.WindowSeconds == other.WindowSeconds
}

// Once the reset has passed, the window has reported no period since.
func (row quotaRow) periodReset(now time.Time) (time.Time, bool) {
	resetAt, err := time.Parse(time.RFC3339, row.ResetAt)
	return resetAt, row.Meter != nil && row.WindowSeconds > 0 && err == nil && resetAt.After(now)
}

func (row quotaRow) currentPeriod(histories []quotaWindowHistory, now time.Time) (billing.QuotaWindowPeriod, bool) {
	resetAt, ok := row.periodReset(now)
	if !ok {
		return billing.QuotaWindowPeriod{}, false
	}
	for _, history := range histories {
		if history.row.sameWindow(row) {
			if period, ok := billing.QuotaPeriodEnding(history.periods, resetAt); ok {
				return period, true
			}
		}
	}
	return billing.QuotaWindowPeriod{Start: resetAt.Add(-time.Duration(row.WindowSeconds) * time.Second), ResetAt: resetAt}, true
}

func (row quotaRow) usageQuery(credential quotaCredential, period billing.QuotaWindowPeriod, now time.Time, exclusive []string) billing.UsageQuery {
	end := period.End
	if end.IsZero() {
		end = now
	}
	models := row.Meter.Models
	if len(models.Include) == 0 {
		models.Exclude = exclusive
	}
	return billing.UsageQuery{From: period.Start, To: end, AuthIndex: credential.authIndex, Provider: credential.provider, ModelMatch: models}
}

func exclusiveModels(rows []quotaRow) []string {
	var models []string
	for _, row := range rows {
		if row.Meter != nil && row.Meter.Exclusive {
			models = append(models, row.Meter.Models.Include...)
		}
	}
	return models
}

func (a *App) attachQuotaUsage(scope string, quotas map[quotaCredential]*authFileQuota) error {
	now := a.store.Now()
	var queries []billing.UsageQuery
	var rows []*quotaRow
	for credential, quota := range quotas {
		// The windows that requests report take their current periods from history.
		from := now
		for _, row := range quota.Quota {
			if resetAt, ok := row.periodReset(now); ok && row.LimitKey != "" {
				if start := resetAt.Add(-time.Duration(row.WindowSeconds) * time.Second); start.Before(from) {
					from = start
				}
			}
		}
		var histories []quotaWindowHistory
		if from.Before(now) {
			var err error
			if histories, err = a.completeQuotaHistories(credential, from, now); err != nil {
				return err
			}
		}
		exclusive := exclusiveModels(quota.Quota)
		for i := range quota.Quota {
			if period, ok := quota.Quota[i].currentPeriod(histories, now); ok {
				queries = append(queries, quota.Quota[i].usageQuery(credential, period, now, exclusive))
				rows = append(rows, &quota.Quota[i])
			}
		}
	}
	if len(queries) == 0 {
		return nil
	}
	usage, err := a.store.UsageTotals(scope, queries)
	if err != nil {
		return err
	}
	for i := range usage {
		rows[i].Usage, rows[i].UsageQuery = &usage[i], &queries[i]
	}
	return nil
}

type authFileHistoryResponse struct {
	From    time.Time                    `json:"from"`
	To      time.Time                    `json:"to"`
	Usage   billing.CredentialUsageStats `json:"usage"`
	Windows []quotaWindowView            `json:"windows"`
}

type quotaWindowView struct {
	Label        string            `json:"label"`
	LabelMessage messages.Message  `json:"label_message,omitzero"`
	LabelPrefix  string            `json:"label_prefix,omitempty"`
	Periods      []quotaPeriodView `json:"periods"`
}

type quotaPeriodView struct {
	Start       time.Time              `json:"start"`
	End         time.Time              `json:"end,omitzero"`
	ResetAt     time.Time              `json:"reset_at,omitzero"`
	EndReason   billing.QuotaPeriodEnd `json:"end_reason,omitempty"`
	UsedPercent float64                `json:"used_percent"`
	Plans       []string               `json:"plans,omitempty"`
	Usage       billing.UsageTotal     `json:"usage"`
	UsageQuery  billing.UsageQuery     `json:"usage_query"`
}

// Each listed period carries its whole usage, even from before the 30 days.
func (a *App) readAuthFileHistory(credential quotaCredential, scope string) (authFileHistoryResponse, error) {
	now := a.store.Now()
	response := authFileHistoryResponse{From: now.Add(-quotaHistoryRange), To: now, Windows: []quotaWindowView{}}
	stats, err := a.store.CredentialUsageStats([]string{credential.authIndex}, scope, response.From, now)
	if err != nil {
		return response, err
	}
	response.Usage = stats[credential.authIndex]
	histories, err := a.completeQuotaHistories(credential, response.From, now)
	if err != nil {
		return response, err
	}
	rows := make([]quotaRow, len(histories))
	for i, history := range histories {
		rows[i] = history.row
	}
	exclusive := exclusiveModels(rows)
	var queries []billing.UsageQuery
	for _, history := range histories {
		window := quotaWindowView{Label: history.row.Label, LabelMessage: history.row.LabelMessage, LabelPrefix: history.row.LabelPrefix}
		for i := len(history.periods) - 1; i >= 0; i-- {
			period := history.periods[i]
			if !period.EndsAfter(response.From) {
				break
			}
			query := history.row.usageQuery(credential, period, now, exclusive)
			window.Periods = append(window.Periods, quotaPeriodView{Start: period.Start, End: period.End, ResetAt: period.ResetAt,
				EndReason: period.EndReason, UsedPercent: period.UsedPercent, Plans: period.Plans, UsageQuery: query})
			queries = append(queries, query)
		}
		if len(window.Periods) > 0 {
			response.Windows = append(response.Windows, window)
		}
	}
	usage, err := a.store.UsageTotals(scope, queries)
	if err != nil {
		return response, err
	}
	for w := range response.Windows {
		for p := range response.Windows[w].Periods {
			response.Windows[w].Periods[p].Usage, usage = usage[0], usage[1:]
		}
	}
	return response, nil
}

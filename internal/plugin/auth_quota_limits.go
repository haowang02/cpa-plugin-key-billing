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
	models billing.ModelMatch
	// Exclusive windows alone meter their models, so windows over every
	// model leave those models out.
	exclusive bool
}

// codexLimit is one Codex rate limit. The main limit meters every model but
// those an additional limit meters. Only Spark's additional limit is known to
// meter a model; other additional limits, and Code Review, whose reviews never
// pass through CLIProxyAPI, meter traffic the plugin cannot tell apart.
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

// codexLimitByKey is the limit a window's key names.
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

// reportedLimit gives the label prefix and meter of a window that responses
// report, by its limit's key. Claude reports only its unified limit.
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
		return &quotaMeter{models: billing.ModelMatch{Include: []string{limit.model}}, exclusive: true}
	}
	return nil
}

// claudeUnifiedLimit keys the 5-hour and weekly windows of Claude's unified
// limit, which covers every model and is reported on each request.
const claudeUnifiedLimit = "unified"

// claudeModelMeter meters a weekly limit Claude keeps for one model family.
func claudeModelMeter(family string) *quotaMeter {
	return &quotaMeter{models: billing.ModelMatch{Include: []string{family}}}
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
	return &quotaMeter{models: models}
}

// periodSeconds is the length of a period reported by its start and reset.
func periodSeconds(start, resetAt string) int64 {
	from, errFrom := time.Parse(time.RFC3339, strings.TrimSpace(start))
	to, errTo := time.Parse(time.RFC3339, resetAt)
	if errFrom != nil || errTo != nil || !to.After(from) {
		return 0
	}
	return int64(to.Sub(from) / time.Second)
}

const (
	// quotaHistoryRange is how far back an auth file's quota periods are listed.
	quotaHistoryRange = 30 * 24 * time.Hour
	// quotaHistoryLookback first reads the reports of one more week, the
	// longest window providers report on requests, so that a period running at
	// the start of a range is usually read whole at once.
	quotaHistoryLookback = 7 * 24 * time.Hour
)

// quotaCredential is the credential whose requests quota windows meter: an
// auth file under the provider its quota belongs to.
type quotaCredential struct {
	authIndex, provider string
}

// quotaWindowHistory is a metered window with the periods its reports divide it into.
type quotaWindowHistory struct {
	row     quotaRow
	periods []billing.QuotaWindowPeriod
}

// quotaWindowHistories reconstructs, main limits first, the periods of each
// metered window that the credential's requests reported from from until now.
// A response reports only the windows of the limit it used, so a window it
// leaves out is unchanged rather than gone.
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
		row := quotaWindow{Seconds: window.Seconds, meter: meter}.row(window.LimitKey, labelPrefix)
		length := time.Duration(window.Seconds) * time.Second
		histories = append(histories, quotaWindowHistory{row: row, periods: billing.QuotaWindowPeriods(length, reports, now)})
	}
	slices.SortFunc(histories, func(left, right quotaWindowHistory) int { return compareQuotaWindows(left.row, right.row) })
	return histories, nil
}

// compareQuotaWindows orders the limits over every model before additional
// ones, and each limit's windows from shortest to longest.
func compareQuotaWindows(left, right quotaRow) int {
	rank := func(row quotaRow) int {
		if row.LimitKey == codexMainLimit.key || row.LimitKey == claudeUnifiedLimit {
			return 0
		}
		return 1
	}
	return cmp.Or(cmp.Compare(rank(left), rank(right)), strings.Compare(left.LimitKey, right.LimitKey),
		cmp.Compare(left.windowSeconds, right.windowSeconds))
}

// completeQuotaHistories reconstructs the windows' periods that run after
// from, each whole: when one began before the reports read so far, they are
// read again from its start, so resets before from still place it.
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
	return row.LimitKey == other.LimitKey && row.windowSeconds == other.windowSeconds
}

// currentPeriod is the period a metered row reports on: the period of the
// window's history that ends at the row's reset time, or else the one the
// reset time declares.
func (row quotaRow) currentPeriod(histories []quotaWindowHistory) (billing.QuotaWindowPeriod, bool) {
	resetAt, err := time.Parse(time.RFC3339, row.ResetAt)
	if row.meter == nil || row.windowSeconds <= 0 || err != nil {
		return billing.QuotaWindowPeriod{}, false
	}
	for _, history := range histories {
		if history.row.sameWindow(row) {
			if period, ok := billing.QuotaPeriodEnding(history.periods, resetAt); ok {
				return period, true
			}
		}
	}
	return billing.QuotaWindowPeriod{Start: resetAt.Add(-time.Duration(row.windowSeconds) * time.Second), ResetAt: resetAt}, true
}

// usageQuery selects the requests a period of the row's window covers,
// through now or the period's end.
func (row quotaRow) usageQuery(credential quotaCredential, period billing.QuotaWindowPeriod, now time.Time, exclusive []string) billing.UsageQuery {
	end := period.End
	if end.IsZero() {
		end = now
		if !period.ResetAt.IsZero() && period.ResetAt.Before(now) {
			end = period.ResetAt
		}
	}
	models := row.meter.models
	if len(models.Include) == 0 {
		models.Exclude = exclusive
	}
	return billing.UsageQuery{From: period.Start, To: end, AuthIndex: credential.authIndex, Provider: credential.provider, ModelMatch: models}
}

func exclusiveModels(rows []quotaRow) []string {
	var models []string
	for _, row := range rows {
		if row.meter != nil && row.meter.exclusive {
			models = append(models, row.meter.models.Include...)
		}
	}
	return models
}

// attachQuotaUsage reports what each metered window of quotas consumed in its
// current period. A scope limits usage to one API key.
func (a *App) attachQuotaUsage(scope string, quotas map[quotaCredential]*authQuotaResponse) error {
	now := a.store.Now()
	var queries []billing.UsageQuery
	var rows []*quotaRow
	for credential, quota := range quotas {
		// The windows that requests report take their current periods from history.
		from := now
		for _, row := range quota.Quota {
			if resetAt, err := time.Parse(time.RFC3339, row.ResetAt); err == nil && row.LimitKey != "" && row.meter != nil {
				if start := resetAt.Add(-time.Duration(row.windowSeconds) * time.Second); start.Before(from) {
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
			if period, ok := quota.Quota[i].currentPeriod(histories); ok {
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

// quotaWindowView lists a window's periods, newest first.
type quotaWindowView struct {
	Label        string            `json:"label"`
	LabelMessage messages.Message  `json:"label_message,omitzero"`
	LabelPrefix  string            `json:"label_prefix,omitempty"`
	Periods      []quotaPeriodView `json:"periods"`
}

// quotaPeriodView lists its usage as totals; UsageQuery breaks it down by
// model on request.
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

// readAuthFileHistory lists an auth file's usage over the last 30 days and the
// periods of its metered quota windows that ran in them, each with its whole
// usage, even from before. A scope limits usage to one API key.
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

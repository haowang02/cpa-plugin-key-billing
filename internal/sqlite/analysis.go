package sqlite

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

func (d *DB) Analysis(query billing.RequestEventQuery, rate billing.ExchangeRate, since time.Time) (billing.AnalysisView, error) {
	view := billing.AnalysisView{
		UsageDistribution: billing.UsageDistribution{
			APIKeys:     []billing.AnalysisComposition{},
			Models:      []billing.AnalysisComposition{},
			Credentials: []billing.AnalysisComposition{},
		},
	}
	view.Trends = analysisTrends(query)
	if len(view.Trends.Requests) == 0 {
		return view, nil
	}
	statement, args := analysisSQL(query, rate, since, view.Trends.Start)
	rows, err := d.db.Query(statement, args...)
	if err != nil {
		return billing.AnalysisView{}, fmt.Errorf("Aggregate analysis data: %w", err)
	}
	defer rows.Close()
	keys, models, credentials := analysisGroups{}, analysisGroups{}, analysisGroups{}
	includeKeys := strings.TrimSpace(query.Scope) == "" && strings.TrimSpace(query.KeyScope) == "" && !query.KeyScopeEmpty
	summary, trends := &view.Summary, &view.Trends
	var summaryCurrencies costCurrencies
	for rows.Next() {
		var index int
		var scope, model, credential string
		var part billing.AnalysisSummary
		var currencies costCurrencies
		if err := rows.Scan(&index, &scope, &model, &credential,
			&part.Requests, &part.Failed, &part.InputTokens, &part.OutputTokens,
			&part.CacheReadTokens, &part.CacheWriteTokens, &part.Cost.TotalUSD,
			&part.Cost.InputUSD, &part.Cost.CacheReadUSD, &part.Cost.CacheWriteUSD, &part.Cost.OutputUSD,
			&currencies.usd, &currencies.cny); err != nil {
			return billing.AnalysisView{}, fmt.Errorf("Read analysis data: %w", err)
		}
		summaryCurrencies = summaryCurrencies.with(currencies)
		trends.Requests[index] += float64(part.Requests)
		trends.UncachedInputTokens[index] += float64(part.InputTokens - part.CacheReadTokens - part.CacheWriteTokens)
		trends.OutputTokens[index] += float64(part.OutputTokens)
		trends.CacheReadTokens[index] += float64(part.CacheReadTokens)
		trends.CacheWriteTokens[index] += float64(part.CacheWriteTokens)
		trends.TotalCost[index] += part.Cost.TotalUSD
		part.TotalTokens = part.InputTokens + part.OutputTokens
		trends.TotalTokens[index] += float64(part.TotalTokens)
		summary.Requests += part.Requests
		summary.Failed += part.Failed
		summary.InputTokens += part.InputTokens
		summary.OutputTokens += part.OutputTokens
		summary.TotalTokens += part.TotalTokens
		summary.CacheReadTokens += part.CacheReadTokens
		summary.CacheWriteTokens += part.CacheWriteTokens
		summary.Cost.TotalUSD += part.Cost.TotalUSD
		summary.Cost.InputUSD += part.Cost.InputUSD
		summary.Cost.CacheReadUSD += part.Cost.CacheReadUSD
		summary.Cost.CacheWriteUSD += part.Cost.CacheWriteUSD
		summary.Cost.OutputUSD += part.Cost.OutputUSD
		if includeKeys {
			keys.add(scope, part, currencies)
		}
		models.add(model, part, currencies)
		credentials.add(credential, part, currencies)
	}
	if err := rows.Err(); err != nil {
		return billing.AnalysisView{}, fmt.Errorf("Read analysis data: %w", err)
	}
	if err := rows.Close(); err != nil {
		return billing.AnalysisView{}, fmt.Errorf("Read analysis data: %w", err)
	}
	summary.Succeeded = summary.Requests - summary.Failed
	summary.Cost.ChargedIn = summaryCurrencies.chargedIn()
	if summary.Requests > 0 {
		summary.SuccessRate = float64(summary.Succeeded) * 100 / float64(summary.Requests)
	}
	if summary.InputTokens > 0 {
		summary.CacheRate = float64(summary.CacheReadTokens) * 100 / float64(summary.InputTokens)
	}
	if err := d.labelAnalysisKeys(keys); err != nil {
		return billing.AnalysisView{}, err
	}
	view.UsageDistribution.APIKeys = keys.finish()
	view.UsageDistribution.Models = models.finish()
	view.UsageDistribution.Credentials = credentials.finish()
	for i := range view.UsageDistribution.Models {
		if view.UsageDistribution.Models[i].Key == "" {
			view.UsageDistribution.Models[i].Label = "Unknown model"
		}
	}
	for i := range view.UsageDistribution.Credentials {
		if view.UsageDistribution.Credentials[i].Key == "" {
			view.UsageDistribution.Credentials[i].Label = "Unknown credential"
		}
	}
	return view, nil
}

// The first hour starts on the browser's local hour before From; subtracting its elapsed part keeps the chosen
// occurrence of a repeated DST hour.
func analysisTrends(query billing.RequestEventQuery) billing.AnalysisTrends {
	location := query.Timezone
	if location == nil {
		location = time.UTC
	}
	local := query.From.In(location)
	start := query.From.Add(-time.Duration(local.Minute())*time.Minute -
		time.Duration(local.Second())*time.Second - time.Duration(local.Nanosecond()))
	hours := 0
	if query.To.After(start) {
		hours = int((query.To.Sub(start) + time.Hour - 1) / time.Hour)
	}
	series := func() []float64 { return make([]float64, hours) }
	return billing.AnalysisTrends{
		Start: start, Requests: series(), TotalTokens: series(),
		UncachedInputTokens: series(), OutputTokens: series(),
		CacheReadTokens: series(), CacheWriteTokens: series(), TotalCost: series(),
	}
}

type analysisGroups map[string]*analysisGroup

type analysisGroup struct {
	billing.AnalysisComposition
	currencies costCurrencies
}

func (groups analysisGroups) add(key string, part billing.AnalysisSummary, currencies costCurrencies) {
	if groups[key] == nil {
		groups[key] = &analysisGroup{AnalysisComposition: billing.AnalysisComposition{Key: key, Label: key}}
	}
	group := groups[key]
	group.currencies = group.currencies.with(currencies)
	row := &group.AnalysisComposition
	row.Requests += part.Requests
	row.TotalTokens += part.TotalTokens
	row.InputTokens += part.InputTokens
	row.OutputTokens += part.OutputTokens
	row.CacheReadTokens += part.CacheReadTokens
	row.CacheWriteTokens += part.CacheWriteTokens
	row.CostUSD += part.Cost.TotalUSD
}

func (groups analysisGroups) finish() []billing.AnalysisComposition {
	rows := make([]billing.AnalysisComposition, 0, len(groups))
	var tokens, requests int64
	for _, group := range groups {
		row := group.AnalysisComposition
		row.ChargedIn = group.currencies.chargedIn()
		rows = append(rows, row)
		tokens += row.TotalTokens
		requests += row.Requests
	}
	for index := range rows {
		if tokens > 0 {
			rows[index].Percent = float64(rows[index].TotalTokens) * 100 / float64(tokens)
		} else if requests > 0 {
			rows[index].Percent = float64(rows[index].Requests) * 100 / float64(requests)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TotalTokens != rows[j].TotalTokens {
			return rows[i].TotalTokens > rows[j].TotalTokens
		}
		if rows[i].Requests != rows[j].Requests {
			return rows[i].Requests > rows[j].Requests
		}
		if rows[i].Label != rows[j].Label {
			return rows[i].Label < rows[j].Label
		}
		return rows[i].Key < rows[j].Key
	})
	return rows
}

func analysisSQL(query billing.RequestEventQuery, rate billing.ExchangeRate, since time.Time, start time.Time) (string, []any) {
	where, args := eventFilter(requestEventSource, query, since)
	modelSQL := "coalesce(NULLIF(" + eventModelSQL + ", ''), '')"
	credentialSQL := "coalesce(NULLIF(" + requestEventCredentialName + ", ''), '')"
	costsSQL := strings.Join([]string{usdSumSQL("total_cost", rate), usdSumSQL("uncached_input_cost", rate),
		usdSumSQL("cache_read_cost", rate), usdSumSQL("cache_write_cost", rate), usdSumSQL("output_cost", rate)}, ", ")
	statement := `SELECT (r.at - ?) / ?, r.scope, ` + modelSQL + `, ` + credentialSQL + `,
		count(*), sum(r.failed != 0),
		sum(r.uncached_input_tokens + r.cache_read_tokens + r.cache_write_tokens), sum(r.billed_output_tokens),
		sum(r.cache_read_tokens), sum(r.cache_write_tokens), ` + costsSQL + `, ` + costCurrenciesSQL + where + `
		GROUP BY 1, 2, 3, 4`
	return statement, append([]any{nanos(start), int64(time.Hour)}, args...)
}

// AnalysisSpeed reads the TPS of the selected model's requests. Quantiles need
// every sample, so rows are bucketed here rather than aggregated in SQL.
func (d *DB) AnalysisSpeed(query billing.RequestEventQuery, since time.Time) (billing.AnalysisSpeedView, error) {
	times := speedTrendTimes(query)
	view := billing.AnalysisSpeedView{Trend: make([]billing.SpeedTrendPoint, len(times))}
	if len(times) == 0 {
		return view, nil
	}
	where, args := eventFilter(requestEventSource, query, since)
	rows, err := d.db.Query(`SELECT r.at, r.billed_output_tokens * 1000.0 / r.latency_ms`+where+`
		AND r.failed = 0 AND r.accounting_quality = ? AND r.billed_output_tokens > 0 AND r.latency_ms > 0`,
		append(args, string(billing.TokenAccountingComplete))...)
	if err != nil {
		return billing.AnalysisSpeedView{}, fmt.Errorf("Read speed analysis data: %w", err)
	}
	defer rows.Close()
	var total []float64
	buckets := make([][]float64, len(times))
	for rows.Next() {
		var at int64
		var tps float64
		if err := rows.Scan(&at, &tps); err != nil {
			return billing.AnalysisSpeedView{}, fmt.Errorf("Read speed analysis data: %w", err)
		}
		index := sort.Search(len(times), func(i int) bool { return nanos(times[i]) > at }) - 1
		total = append(total, tps)
		buckets[index] = append(buckets[index], tps)
	}
	if err := rows.Err(); err != nil {
		return billing.AnalysisSpeedView{}, fmt.Errorf("Read speed analysis data: %w", err)
	}
	view.Summary = speedStats(total)
	for index, start := range times {
		view.Trend[index] = billing.SpeedTrendPoint{Time: start, SpeedStats: speedStats(buckets[index])}
	}
	return view, nil
}

// Quantiles cannot be merged, so days are bucketed here; DST makes them 23 or 25 hours long.
func speedTrendTimes(query billing.RequestEventQuery) []time.Time {
	daily := query.To.Sub(query.From) > 24*time.Hour
	start := query.From
	if daily {
		location := query.Timezone
		if location == nil {
			location = time.UTC
		}
		local := query.From.In(location)
		start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	}
	var times []time.Time
	for cursor := start; cursor.Before(query.To); {
		times = append(times, cursor)
		if daily {
			cursor = cursor.AddDate(0, 0, 1)
		} else {
			cursor = cursor.Add(time.Hour)
		}
	}
	return times
}

func speedStats(values []float64) billing.SpeedStats {
	if len(values) == 0 {
		return billing.SpeedStats{}
	}
	slices.Sort(values)
	quantile := func(q float64) float64 {
		position := q * float64(len(values)-1)
		lower := int(position)
		if lower == len(values)-1 {
			return values[lower]
		}
		return values[lower] + (values[lower+1]-values[lower])*(position-float64(lower))
	}
	return billing.SpeedStats{
		Samples: len(values), P10: quantile(0.1), P25: quantile(0.25), P50: quantile(0.5), P75: quantile(0.75), P90: quantile(0.9),
	}
}

func (d *DB) labelAnalysisKeys(keys analysisGroups) error {
	if len(keys) == 0 {
		return nil
	}
	if key := keys[""]; key != nil {
		key.Label = "Unassigned"
	}
	metadata, err := d.db.Query("SELECT scope, preview, label FROM api_keys")
	if err != nil {
		return fmt.Errorf("Read analysis key metadata: %w", err)
	}
	defer metadata.Close()
	for metadata.Next() {
		var scope, preview, label string
		if err := metadata.Scan(&scope, &preview, &label); err != nil {
			return fmt.Errorf("Read analysis key metadata: %w", err)
		}
		if key := keys[scope]; key != nil {
			key.Preview = preview
			if label != "" {
				key.Label = label
			} else if preview != "" {
				key.Label = preview
			}
		}
	}
	if err := metadata.Err(); err != nil {
		return fmt.Errorf("Read analysis key metadata: %w", err)
	}
	return nil
}

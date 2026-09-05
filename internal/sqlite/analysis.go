package sqlite

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

const (
	analysisModelSQL         = "coalesce(NULLIF(r.billing_model, ''), NULLIF(r.upstream_model, ''), '未知模型')"
	analysisInputTokensSQL   = `(r.uncached_input_tokens + r.cache_read_tokens + r.cache_write_tokens)`
	analysisTokensSQL        = `(` + analysisInputTokensSQL + ` + r.billed_output_tokens)`
	analysisCostAvailableSQL = `(r.price_source != 'none' OR ` + analysisTokensSQL + ` = 0)`
)

type analysisDimension struct {
	name       string
	key, label string
	preview    string
	target     *[]billing.AnalysisComposition
}

func (d *DB) Analysis(query billing.RequestEventQuery, since time.Time) (billing.AnalysisView, error) {
	where, args := requestEventFilter(query, since)
	dimensions := []analysisDimension{}
	view := billing.AnalysisView{
		UsageDistribution: billing.UsageDistribution{
			APIKeys: []billing.AnalysisComposition{},
			Models:  []billing.AnalysisComposition{},
			Sources: []billing.AnalysisComposition{},
		},
	}
	summary, errSummary := d.analysisSummary(where, args)
	if errSummary != nil {
		return billing.AnalysisView{}, errSummary
	}
	view.Summary = summary
	trends, errTrends := d.analysisTrends(query, where, args)
	if errTrends != nil {
		return billing.AnalysisView{}, errTrends
	}
	if !summary.Cost.Available {
		trends.TotalCost = []billing.AnalysisTrendPoint{}
	}
	view.Trends = trends
	if query.Scope == "" && query.KeyScope == "" {
		dimensions = append(dimensions, analysisDimension{
			name: "API Key", key: "r.scope",
			label:   "coalesce(NULLIF(k.label, ''), NULLIF(k.preview, ''), NULLIF(r.scope, ''), '未归属')",
			preview: "coalesce(k.preview, '')",
			target:  &view.UsageDistribution.APIKeys,
		})
	}
	dimensions = append(dimensions,
		analysisDimension{name: "模型", key: analysisModelSQL, label: analysisModelSQL, target: &view.UsageDistribution.Models},
		analysisDimension{
			name: "来源", key: "coalesce(NULLIF(c.name, ''), '未知来源')",
			label: "coalesce(NULLIF(c.name, ''), '未知来源')", target: &view.UsageDistribution.Sources,
		},
	)
	for _, dimension := range dimensions {
		preview := dimension.preview
		if preview == "" {
			preview = "''"
		}
		rows, err := d.analysisComposition(where, args, dimension.key, dimension.label, preview, dimension.name)
		if err != nil {
			return billing.AnalysisView{}, err
		}
		*dimension.target = rows
	}
	return view, nil
}

func (d *DB) analysisSummary(where string, args []any) (billing.AnalysisSummary, error) {
	var summary billing.AnalysisSummary
	var costAvailable int
	err := d.db.QueryRow(`SELECT count(*),
		coalesce(sum(CASE WHEN r.failed != 0 THEN 1 ELSE 0 END), 0),
		coalesce(sum(`+analysisTokensSQL+`), 0),
		coalesce(sum(`+analysisInputTokensSQL+`), 0),
		coalesce(sum(r.billed_output_tokens), 0), coalesce(sum(r.cache_read_tokens), 0),
		coalesce(sum(r.cache_write_tokens), 0), coalesce(sum(r.total_usd), 0),
		coalesce(sum(r.uncached_input_usd), 0), coalesce(sum(r.cache_read_usd), 0),
		coalesce(sum(r.cache_write_usd), 0), coalesce(sum(r.output_usd), 0),
		coalesce(min(CASE WHEN `+analysisCostAvailableSQL+` THEN 1 ELSE 0 END), 1)`+where, args...).Scan(
		&summary.Requests, &summary.Failed, &summary.TotalTokens, &summary.InputTokens, &summary.OutputTokens,
		&summary.CacheReadTokens, &summary.CacheWriteTokens,
		&summary.Cost.TotalUSD, &summary.Cost.InputUSD, &summary.Cost.CacheReadUSD,
		&summary.Cost.CacheWriteUSD, &summary.Cost.OutputUSD, &costAvailable,
	)
	if err != nil {
		return billing.AnalysisSummary{}, fmt.Errorf("汇总分析数据：%w", err)
	}
	summary.Cost.Available = costAvailable != 0
	summary.Succeeded = summary.Requests - summary.Failed
	if summary.Requests > 0 {
		summary.SuccessRate = float64(summary.Succeeded) * 100 / float64(summary.Requests)
	}
	if summary.InputTokens > 0 {
		summary.CacheRate = float64(summary.CacheReadTokens) * 100 / float64(summary.InputTokens)
	}
	return summary, nil
}

func (d *DB) analysisTrends(query billing.RequestEventQuery, where string, args []any) (billing.AnalysisTrends, error) {
	location := query.Timezone
	if location == nil {
		location = time.UTC
	}
	daily := query.To.Sub(query.From) > 24*time.Hour
	start := query.From
	if daily {
		local := query.From.In(location)
		start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	}
	points := func() []billing.AnalysisTrendPoint {
		result := []billing.AnalysisTrendPoint{}
		for cursor := start; cursor.Before(query.To); {
			result = append(result, billing.AnalysisTrendPoint{Time: cursor})
			if daily {
				cursor = cursor.AddDate(0, 0, 1)
			} else {
				cursor = cursor.Add(time.Hour)
			}
		}
		return result
	}
	result := billing.AnalysisTrends{
		Requests: points(), TotalTokens: points(), UncachedInputTokens: points(), OutputTokens: points(),
		CacheReadTokens: points(), CacheWriteTokens: points(), CacheRate: points(), TotalCost: points(),
	}
	boundaries := result.Requests
	var bucketSQL strings.Builder
	queryArgs := make([]any, 0, len(boundaries)-1+len(args))
	if len(boundaries) == 1 {
		bucketSQL.WriteString("0")
	} else {
		bucketSQL.WriteString("CASE")
		for index := 1; index < len(boundaries); index++ {
			bucketSQL.WriteString(" WHEN r.at < ? THEN ")
			bucketSQL.WriteString(fmt.Sprint(index - 1))
			queryArgs = append(queryArgs, nanos(boundaries[index].Time))
		}
		bucketSQL.WriteString(" ELSE ")
		bucketSQL.WriteString(fmt.Sprint(len(boundaries) - 1))
		bucketSQL.WriteString(" END")
	}
	queryArgs = append(queryArgs, args...)
	rows, err := d.db.Query(`SELECT `+bucketSQL.String()+`, count(*),
		coalesce(sum(r.uncached_input_tokens), 0), coalesce(sum(r.billed_output_tokens), 0),
		coalesce(sum(r.cache_read_tokens), 0), coalesce(sum(r.cache_write_tokens), 0),
		coalesce(sum(r.total_usd), 0)`+
		where+` GROUP BY 1 ORDER BY 1`, queryArgs...)
	if err != nil {
		return billing.AnalysisTrends{}, fmt.Errorf("聚合分析趋势：%w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var index int
		var requests, uncachedInputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64
		var totalCost float64
		if err := rows.Scan(
			&index, &requests, &uncachedInputTokens, &outputTokens,
			&cacheReadTokens, &cacheWriteTokens, &totalCost,
		); err != nil {
			return billing.AnalysisTrends{}, fmt.Errorf("读取分析趋势：%w", err)
		}
		if index < 0 || index >= len(result.Requests) {
			return billing.AnalysisTrends{}, fmt.Errorf("读取分析趋势：桶索引 %d 超出范围", index)
		}
		totalInputTokens := uncachedInputTokens + cacheReadTokens + cacheWriteTokens
		result.Requests[index].Value = float64(requests)
		result.TotalTokens[index].Value = float64(totalInputTokens + outputTokens)
		result.UncachedInputTokens[index].Value = float64(uncachedInputTokens)
		result.OutputTokens[index].Value = float64(outputTokens)
		result.CacheReadTokens[index].Value = float64(cacheReadTokens)
		result.CacheWriteTokens[index].Value = float64(cacheWriteTokens)
		if totalInputTokens > 0 {
			result.CacheRate[index].Value = float64(cacheReadTokens) * 100 / float64(totalInputTokens)
		}
		result.TotalCost[index].Value = totalCost
	}
	if err := rows.Err(); err != nil {
		return billing.AnalysisTrends{}, fmt.Errorf("读取分析趋势：%w", err)
	}
	return result, nil
}

func (d *DB) analysisComposition(where string, args []any, keySQL, labelSQL, previewSQL, name string) ([]billing.AnalysisComposition, error) {
	rows, err := d.db.Query(`SELECT `+keySQL+`, `+labelSQL+`, `+previewSQL+`, sum(`+analysisTokensSQL+`),
		count(*), sum(r.total_usd),
		min(CASE WHEN `+analysisCostAvailableSQL+` THEN 1 ELSE 0 END)`+
		where+` GROUP BY 1, 2, 3`, args...)
	if err != nil {
		return nil, fmt.Errorf("聚合%s用量分布：%w", name, err)
	}
	defer rows.Close()
	result := []billing.AnalysisComposition{}
	for rows.Next() {
		var row billing.AnalysisComposition
		var available int
		if err := rows.Scan(&row.Key, &row.Label, &row.Preview, &row.TotalTokens, &row.Requests, &row.CostUSD, &available); err != nil {
			return nil, fmt.Errorf("读取%s用量分布：%w", name, err)
		}
		row.CostAvailable = available != 0
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取%s用量分布：%w", name, err)
	}
	finishSQLComposition(result)
	return result, nil
}

func finishSQLComposition(rows []billing.AnalysisComposition) {
	var tokens, requests int64
	for _, row := range rows {
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
		if rows[i].TotalTokens == rows[j].TotalTokens {
			if rows[i].Requests == rows[j].Requests {
				return rows[i].Label < rows[j].Label
			}
			return rows[i].Requests > rows[j].Requests
		}
		return rows[i].TotalTokens > rows[j].TotalTokens
	})
}

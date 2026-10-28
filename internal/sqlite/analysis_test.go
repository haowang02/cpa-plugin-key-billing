package sqlite

import (
	"math"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func TestAnalysisAggregatesOnlyDisplayedUsageDistributions(t *testing.T) {
	database := requestErrorDatabase(t)
	view, err := database.Analysis(billing.RequestEventQuery{
		From: eventStart.Add(-time.Hour),
		To:   eventStart.Add(3 * time.Hour),
	}, 7, eventStart.Add(-billing.RequestEventRetention))
	if err != nil {
		t.Fatal(err)
	}
	usage := view.UsageDistribution
	if len(usage.APIKeys) != 2 || len(usage.Models) != 1 || len(usage.Sources) != 1 {
		t.Fatalf("usage distribution = %+v", usage)
	}
	if usage.APIKeys[0].Label != "Alice" || usage.APIKeys[0].Preview != "sk-aaa…0001" ||
		usage.Models[0].Preview != "" || usage.Sources[0].Preview != "" {
		t.Fatalf("distribution identities = %+v", usage)
	}
	if usage.Models[0].Requests != 3 || usage.Models[0].TotalTokens != 3000 || usage.Models[0].CostUSD != 1.5 {
		t.Fatalf("model distribution = %+v", usage.Models)
	}
	summary := view.Summary
	if summary.Requests != 3 || summary.Succeeded != 1 || summary.Failed != 2 ||
		summary.TotalTokens != 3000 || summary.InputTokens != 1500 || summary.CacheRate != 0 ||
		summary.Cost.TotalUSD != 1.5 || math.Abs(summary.Cost.InputUSD-0.6) > 1e-9 ||
		math.Abs(summary.Cost.OutputUSD-0.9) > 1e-9 {
		t.Fatalf("summary = %+v", summary)
	}
	if usage.APIKeys[0].Percent+usage.APIKeys[1].Percent != 100 {
		t.Fatalf("API Key percentages = %+v", usage.APIKeys)
	}
	trend := view.Trends.Requests
	if len(trend) != 4 || trend[0].Value != 0 || trend[1].Value != 3 ||
		!trend[0].Time.Equal(eventStart.Add(-time.Hour)) {
		t.Fatalf("hourly request trend = %+v", trend)
	}
}

func TestAnalysisDailyTrendUsesBrowserTimeZone(t *testing.T) {
	database := openTestDB(t)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Preview: "sk-tes…0001"}
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	events := []billing.RequestEvent{
		requestEvent("scope-a", time.Date(2026, 3, 8, 4, 59, 0, 0, time.UTC)),
		requestEvent("scope-a", time.Date(2026, 3, 8, 5, 1, 0, 0, time.UTC)),
		requestEvent("scope-a", time.Date(2026, 3, 9, 4, 1, 0, 0, time.UTC)),
	}
	mustSave(t, database, state, billing.Changes{
		AllKeys: true, NormalRequestEvents: events,
	})

	view, err := database.Analysis(billing.RequestEventQuery{
		From: from, To: from.Add(72 * time.Hour), Timezone: location,
	}, 7, from.Add(-billing.RequestEventRetention))
	if err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2026, 3, 7, 0, 0, 0, 0, location)
	trend := view.Trends.Requests
	if len(trend) != 4 || !trend[0].Time.Equal(wantStart) ||
		trend[0].Value != 1 || trend[1].Value != 1 || trend[2].Value != 1 || trend[3].Value != 0 {
		t.Fatalf("daily request trend = %+v", trend)
	}
}

func TestAnalysisScopeSkipsTheRedundantKeyDimension(t *testing.T) {
	database := requestErrorDatabase(t)
	view, err := database.Analysis(billing.RequestEventQuery{
		Scope: "scope-a",
		From:  eventStart.Add(-time.Hour),
		To:    eventStart.Add(3 * time.Hour),
	}, 7, eventStart.Add(-billing.RequestEventRetention))
	if err != nil {
		t.Fatal(err)
	}
	usage := view.UsageDistribution
	if len(usage.APIKeys) != 0 {
		t.Fatalf("scoped API Key distribution = %+v", usage.APIKeys)
	}
	if len(usage.Models) != 1 || usage.Models[0].Requests != 2 {
		t.Fatalf("scoped model distribution = %+v", usage.Models)
	}
	if len(usage.Sources) != 1 || usage.Sources[0].Requests != 2 {
		t.Fatalf("scoped source distribution = %+v", usage.Sources)
	}
}

func TestAnalysisIncludesTokenAndCostBreakdowns(t *testing.T) {
	database := openTestDB(t)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Preview: "sk-tes…0001"}
	event := requestEvent("scope-a", eventStart)
	event.Cost = billing.Cost{
		Currency:            billing.USD,
		Total:               5,
		UncachedInput:       1,
		CacheRead:           3,
		CacheWrite:          0.5,
		Output:              0.5,
		UncachedInputTokens: 100,
		CacheReadTokens:     300,
		CacheWriteTokens:    50,
		BilledOutputTokens:  50,
	}
	failed := requestEvent("scope-a", eventStart.Add(75*time.Minute))
	failed.Cost = billing.Cost{UncachedInputTokens: 400, CacheReadTokens: 100, BilledOutputTokens: 100}
	zeroInput := requestEvent("scope-b", eventStart)
	zeroInput.BillingModel, zeroInput.Account = "output-only", "zero@example.com"
	zeroInput.Cost = billing.Cost{Currency: billing.CNY, Total: 7, Output: 7, BilledOutputTokens: 40}
	outside := requestEvent("scope-a", eventStart.Add(2*time.Hour))
	mustSave(t, database, state, billing.Changes{
		AllKeys: true, NormalRequestEvents: []billing.RequestEvent{event, zeroInput, outside},
		RequestErrorEvents: []billing.RequestErrorEvent{{Event: failed}},
	})

	query := billing.RequestEventQuery{From: eventStart, To: eventStart.Add(2 * time.Hour)}
	view, err := database.Analysis(query, 7, eventStart)
	if err != nil {
		t.Fatal(err)
	}
	summary := view.Summary
	if summary.Requests != 3 || summary.Failed != 1 || summary.TotalTokens != 1140 || summary.InputTokens != 950 || summary.OutputTokens != 190 ||
		summary.CacheReadTokens != 400 || summary.CacheWriteTokens != 50 ||
		math.Abs(summary.CacheRate-800.0/19) > 1e-9 {
		t.Fatalf("token summary = %+v", summary)
	}
	// CNY costs convert into USD at the rate; a total of both has no currency of its own.
	if summary.Cost.TotalUSD != 6 || summary.Cost.InputUSD != 1 ||
		summary.Cost.CacheReadUSD != 3 || summary.Cost.CacheWriteUSD != 0.5 ||
		summary.Cost.OutputUSD != 1.5 || summary.Cost.ChargedIn != "" {
		t.Fatalf("cost summary = %+v", summary.Cost)
	}
	trends := view.Trends
	if len(trends.UncachedInputTokens) != 2 || trends.UncachedInputTokens[0].Value != 100 ||
		trends.OutputTokens[0].Value != 90 || trends.CacheReadTokens[0].Value != 300 ||
		trends.CacheWriteTokens[0].Value != 50 || trends.TotalTokens[0].Value != 540 || trends.TotalTokens[1].Value != 600 {
		t.Fatalf("token trends = %+v", trends)
	}
	for name, rows := range map[string][]billing.AnalysisComposition{
		"keys": view.UsageDistribution.APIKeys, "models": view.UsageDistribution.Models, "sources": view.UsageDistribution.Sources,
	} {
		if len(rows) != 2 {
			t.Fatalf("%s distribution = %+v", name, rows)
		}
		row := rows[0]
		if row.Requests != 2 || row.TotalTokens != 1100 || row.InputTokens != 950 || row.OutputTokens != 150 ||
			row.CacheReadTokens != 400 || row.CacheWriteTokens != 50 || row.ChargedIn != billing.USD {
			t.Errorf("%s token breakdown = %+v", name, row)
		}
		zero := rows[1]
		if zero.InputTokens != 0 || zero.OutputTokens != 40 || zero.TotalTokens != 40 || zero.CacheReadTokens != 0 || zero.CacheWriteTokens != 0 ||
			zero.ChargedIn != billing.CNY {
			t.Errorf("%s output-only breakdown = %+v", name, zero)
		}
	}
	query.Scope = "scope-a"
	scoped, err := database.Analysis(query, 7, eventStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.UsageDistribution.APIKeys) != 0 || len(scoped.UsageDistribution.Models) != 1 || len(scoped.UsageDistribution.Sources) != 1 ||
		scoped.UsageDistribution.Models[0].InputTokens != 950 || scoped.UsageDistribution.Sources[0].CacheReadTokens != 400 {
		t.Fatalf("scoped distribution = %+v", scoped.UsageDistribution)
	}
}

func TestAnalysisFullRetentionKeepsZeroUsageAndEmptyScopes(t *testing.T) {
	d := openTestDB(t)
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2025, 9, 1, 12, 30, 0, 0, location)
	to := from.Add(billing.RequestEventRetention)
	state := billing.NewState()
	mustSave(t, d, state, billing.Changes{
		NormalRequestEvents: []billing.RequestEvent{
			{At: from, Scope: ""},
			{At: to, Scope: "outside"},
		},
		RequestErrorEvents: []billing.RequestErrorEvent{
			{Event: billing.RequestEvent{At: to.Add(-time.Nanosecond), Scope: "orphan"}},
		},
	})
	view, err := d.Analysis(billing.RequestEventQuery{From: from, To: to, Timezone: location}, 7, from)
	if err != nil {
		t.Fatal(err)
	}
	if view.Summary.Requests != 2 || view.Summary.Failed != 1 || view.Summary.TotalTokens != 0 || len(view.Trends.Requests) != 366 {
		t.Fatalf("summary=%+v, buckets=%d", view.Summary, len(view.Trends.Requests))
	}
	if view.Trends.Requests[0].Value != 1 || view.Trends.Requests[365].Value != 1 {
		t.Fatal("range boundaries changed")
	}
	keys := view.UsageDistribution.APIKeys
	if len(keys) != 2 || keys[0].Percent != 50 || keys[1].Percent != 50 {
		t.Fatalf("zero-usage key distribution=%+v", keys)
	}
	if view.UsageDistribution.Models[0].Label != "Unknown model" || view.UsageDistribution.Sources[0].Label != "Unknown source" {
		t.Fatalf("fallback labels=%+v", view.UsageDistribution)
	}
}

func TestAnalysisFallbackLabelsDoNotMergeUserDefinedModels(t *testing.T) {
	d := openTestDB(t)
	from := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	events := []billing.RequestEvent{}
	for _, model := range []string{"", "Unknown model", "未知模型"} {
		events = append(events, billing.RequestEvent{At: from, BillingModel: model})
	}
	mustSave(t, d, billing.NewState(), billing.Changes{NormalRequestEvents: events})
	view, err := d.Analysis(billing.RequestEventQuery{From: from, To: from.Add(time.Hour)}, 7, from)
	if err != nil {
		t.Fatal(err)
	}
	models := view.UsageDistribution.Models
	if len(models) != 3 {
		t.Fatalf("missing and user-defined models must remain separate: %+v", models)
	}
	for _, model := range models {
		if model.Requests != 1 || (model.Key != "" && model.Label != model.Key) {
			t.Fatalf("user-defined model was changed: %+v", model)
		}
	}
}

func TestAnalysisSpeedMeasuresTPSOfOneModel(t *testing.T) {
	database := openTestDB(t)
	event := func(offset time.Duration, latency, output int64) billing.RequestEvent {
		entry := requestEvent("scope-a", eventStart.Add(offset))
		entry.LatencyMS, entry.Cost.BilledOutputTokens = latency, output
		return entry
	}
	unmeasured := event(40*time.Minute, 1000, 300)
	unmeasured.AccountingQuality = billing.TokenAccountingUnclassified
	otherModel := event(12*time.Minute, 100, 900)
	otherModel.BillingModel = "gpt-5.6-luna"
	failed := event(15*time.Minute, 1000, 100)
	failed.Failed = true
	mustSave(t, database, billing.NewState(), billing.Changes{
		NormalRequestEvents: []billing.RequestEvent{
			event(10*time.Minute, 1000, 100), event(20*time.Minute, 2000, 100), event(30*time.Minute, 4000, 100),
			event(45*time.Minute, 1000, 0), event(70*time.Minute, 2000, 100), unmeasured, otherModel,
		},
		RequestErrorEvents: []billing.RequestErrorEvent{{Event: failed, Error: billing.RequestError{StatusCode: 502}}},
	})

	view, err := database.AnalysisSpeed(billing.RequestEventQuery{
		Model: "gpt-5.5", From: eventStart, To: eventStart.Add(2 * time.Hour),
	}, eventStart.Add(-billing.RequestEventRetention))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Trend) != 2 || !view.Trend[1].Time.Equal(eventStart.Add(time.Hour)) ||
		view.Trend[0].SpeedStats != (billing.SpeedStats{Samples: 3, P10: 30, P25: 37.5, P50: 50, P75: 75, P90: 90}) ||
		view.Trend[1].Samples != 1 || view.Trend[1].P50 != 50 {
		t.Fatalf("speed trend = %+v", view.Trend)
	}
	if view.Summary.Samples != 4 || view.Summary.P50 != 50 {
		t.Fatalf("summary = %+v", view.Summary)
	}
}

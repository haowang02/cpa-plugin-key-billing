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
	}, eventStart.Add(-billing.RequestEventRetention))
	if err != nil {
		t.Fatal(err)
	}
	usage := view.UsageDistribution
	if len(usage.APIKeys) != 2 || len(usage.Models) != 1 || len(usage.Sources) != 1 {
		t.Fatalf("usage distribution = %+v", usage)
	}
	if usage.Models[0].Requests != 3 || usage.Models[0].TotalTokens != 3000 || usage.Models[0].CostUSD != 1.5 {
		t.Fatalf("model distribution = %+v", usage.Models)
	}
	summary := view.Summary
	if summary.Requests != 3 || summary.Succeeded != 1 || summary.Failed != 2 ||
		summary.TotalTokens != 3000 || summary.InputTokens != 1500 || summary.CacheRate != 0 ||
		summary.Cost.TotalUSD != 1.5 || math.Abs(summary.Cost.Input.USD-0.6) > 1e-9 ||
		math.Abs(summary.Cost.Output.USD-0.9) > 1e-9 {
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
	}, from.Add(-billing.RequestEventRetention))
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
	}, eventStart.Add(-billing.RequestEventRetention))
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

func TestAnalysisSummaryIncludesTokenAndCostBreakdowns(t *testing.T) {
	database := openTestDB(t)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Preview: "sk-tes…0001"}
	event := requestEvent("scope-a", eventStart)
	event.Cost = billing.Cost{
		TotalUSD:            5,
		UncachedInputUSD:    1,
		CacheReadUSD:        3,
		CacheWriteUSD:       0.5,
		OutputUSD:           0.5,
		UncachedInputTokens: 100,
		CacheReadTokens:     300,
		CacheWriteTokens:    50,
		BilledOutputTokens:  50,
	}
	mustSave(t, database, state, billing.Changes{
		AllKeys:             true,
		NormalRequestEvents: []billing.RequestEvent{event},
	})

	view, err := database.Analysis(billing.RequestEventQuery{
		From: eventStart.Add(-time.Minute),
		To:   eventStart.Add(time.Minute),
	}, eventStart.Add(-billing.RequestEventRetention))
	if err != nil {
		t.Fatal(err)
	}
	summary := view.Summary
	if summary.TotalTokens != 500 || summary.InputTokens != 450 || summary.OutputTokens != 50 ||
		summary.CacheReadTokens != 300 || summary.CacheWriteTokens != 50 ||
		math.Abs(summary.CacheRate-200.0/3) > 1e-9 {
		t.Fatalf("token summary = %+v", summary)
	}
	if summary.Cost.TotalUSD != 5 || summary.Cost.Input.USD != 1 ||
		summary.Cost.CacheRead.USD != 3 || summary.Cost.CacheWrite.USD != 0.5 ||
		summary.Cost.Output.USD != 0.5 {
		t.Fatalf("cost summary = %+v", summary.Cost)
	}
	trends := view.Trends
	if len(trends.UncachedInputTokens) != 1 || trends.UncachedInputTokens[0].Value != 100 ||
		trends.OutputTokens[0].Value != 50 || trends.CacheReadTokens[0].Value != 300 ||
		trends.CacheWriteTokens[0].Value != 50 || trends.TotalTokens[0].Value != 500 {
		t.Fatalf("token trends = %+v", trends)
	}
}

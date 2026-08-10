package billing

import (
	"testing"
	"time"
)

func TestPlanValidateLengthsAndNever(t *testing.T) {
	valid := []Period{
		{Kind: PeriodDaily},
		{Kind: PeriodWeekly},
		{Kind: PeriodMonthly},
		{Kind: PeriodCustom, Seconds: 3600},
		{Kind: PeriodNever},
	}
	for _, period := range valid {
		if errValidate := (Plan{ID: "p", AmountUSD: 1, Period: period}).Validate(); errValidate != nil {
			t.Fatalf("period %+v rejected: %v", period, errValidate)
		}
	}
	invalid := []Plan{
		{AmountUSD: 1, Period: Period{Kind: PeriodDaily}},
		{ID: "p", Period: Period{Kind: PeriodDaily}},
		{ID: "p", AmountUSD: 1, Period: Period{Kind: PeriodCustom}},
		{ID: "p", AmountUSD: 1, Period: Period{Kind: "yearly"}},
	}
	for _, plan := range invalid {
		if plan.Validate() == nil {
			t.Fatalf("invalid plan accepted: %+v", plan)
		}
	}
}

func TestCycleLengthsStartAtEachKeysFirstUse(t *testing.T) {
	start := time.Date(2026, 3, 8, 1, 30, 0, 0, time.FixedZone("browser-like", -5*3600))
	tests := []struct {
		kind    PeriodKind
		seconds int64
		want    time.Duration
	}{
		{kind: PeriodDaily, want: 24 * time.Hour},
		{kind: PeriodWeekly, want: 7 * 24 * time.Hour},
		{kind: PeriodMonthly, want: 30 * 24 * time.Hour},
		{kind: PeriodCustom, seconds: 90, want: 90 * time.Second},
	}
	for _, test := range tests {
		plan := Plan{Period: Period{Kind: test.kind, Seconds: test.seconds}}
		if got := plan.CycleEnd(start).Sub(start); got != test.want {
			t.Fatalf("%s length = %v, want %v", test.kind, got, test.want)
		}
	}
	if end := (Plan{Period: Period{Kind: PeriodNever}}).CycleEnd(start); !end.IsZero() {
		t.Fatalf("never-reset end = %v, want zero", end)
	}
}

func TestCycleIsInactiveUntilUseAndAgainAfterReset(t *testing.T) {
	plan := Plan{ID: "p", AmountUSD: 5, Period: Period{Kind: PeriodDaily}}
	key := &KeyState{PlanID: "p"}
	firstUse := time.Date(2026, 8, 3, 10, 15, 0, 0, time.UTC)
	if settleExpiredCycle(key, plan, firstUse) || !key.Cycle.StartAt.IsZero() {
		t.Fatalf("an idle key started unexpectedly: %+v", key.Cycle)
	}
	if !activateCycle(key, plan, firstUse) || !key.Cycle.StartAt.Equal(firstUse) || !key.Cycle.EndAt.Equal(firstUse.Add(24*time.Hour)) {
		t.Fatalf("first cycle = %+v", key.Cycle)
	}
	key.Cycle.SpentUSD = 3
	if !settleExpiredCycle(key, plan, key.Cycle.EndAt) || key.Cycle != (Cycle{}) {
		t.Fatalf("expired cycle did not return to initial state: %+v", key.Cycle)
	}

	nextUse := firstUse.Add(3 * 24 * time.Hour)
	if !activateCycle(key, plan, nextUse) || !key.Cycle.StartAt.Equal(nextUse) {
		t.Fatalf("next cycle did not start at next use: %+v", key.Cycle)
	}
}

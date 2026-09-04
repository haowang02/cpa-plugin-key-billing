package billing

import (
	"math"
	"testing"
	"time"
)

func TestPlanValidate(t *testing.T) {
	valid := []int64{
		0,
		5 * 60 * 60,
	}
	for _, seconds := range valid {
		if errValidate := (Plan{ID: "p", AmountUSD: 1, PeriodSeconds: seconds}).Validate(); errValidate != nil {
			t.Fatalf("period %d rejected: %v", seconds, errValidate)
		}
	}
	invalid := []Plan{
		{AmountUSD: 1, PeriodSeconds: 86400},
		{ID: "p", PeriodSeconds: 86400},
		{ID: "p", AmountUSD: 1, PeriodSeconds: -1},
		{ID: "p", AmountUSD: 1, PeriodSeconds: maxPeriodSeconds + 1},
		{ID: "p", AmountUSD: math.Inf(1), PeriodSeconds: 86400},
	}
	for _, plan := range invalid {
		if plan.Validate() == nil {
			t.Fatalf("invalid plan accepted: %+v", plan)
		}
	}
}

func TestCycleIsInactiveUntilUseAndAgainAfterReset(t *testing.T) {
	plan := Plan{ID: "p", AmountUSD: 5, PeriodSeconds: 5 * 60 * 60}
	key := &KeyState{PlanID: "p"}
	firstUse := time.Date(2026, 8, 3, 10, 15, 0, 0, time.UTC)
	if settleExpiredCycle(key, plan, firstUse) || !key.Cycle.StartAt.IsZero() {
		t.Fatalf("an idle key started unexpectedly: %+v", key.Cycle)
	}
	if !activateCycle(key, plan, firstUse) || !key.Cycle.StartAt.Equal(firstUse) || !key.Cycle.EndAt.Equal(firstUse.Add(5*time.Hour)) {
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

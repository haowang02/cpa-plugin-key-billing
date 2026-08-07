package billing

import (
	"testing"
	"time"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, errLoad := time.LoadLocation(name)
	if errLoad != nil {
		t.Fatalf("load %s: %v", name, errLoad)
	}
	return loc
}

func assertWindow(t *testing.T, gotStart, gotEnd, wantStart, wantEnd time.Time) {
	t.Helper()
	if !gotStart.Equal(wantStart) || !gotEnd.Equal(wantEnd) {
		t.Fatalf("window = [%s, %s), want [%s, %s)",
			gotStart.Format(time.RFC3339), gotEnd.Format(time.RFC3339),
			wantStart.Format(time.RFC3339), wantEnd.Format(time.RFC3339))
	}
}

func TestPlanValidate(t *testing.T) {
	tests := []struct {
		name    string
		plan    Plan
		wantErr bool
	}{
		{name: "daily", plan: Plan{ID: "p", AmountUSD: 1, Period: Period{Kind: PeriodDaily}}},
		{name: "weekly", plan: Plan{ID: "p", AmountUSD: 1, Period: Period{Kind: PeriodWeekly}}},
		{name: "monthly", plan: Plan{ID: "p", AmountUSD: 1, Period: Period{Kind: PeriodMonthly}}},
		{name: "custom", plan: Plan{ID: "p", AmountUSD: 1, Period: Period{Kind: PeriodCustom, Seconds: 3600}}},

		{name: "missing id", plan: Plan{AmountUSD: 1, Period: Period{Kind: PeriodDaily}}, wantErr: true},
		{name: "zero amount", plan: Plan{ID: "p", Period: Period{Kind: PeriodDaily}}, wantErr: true},
		{name: "negative amount", plan: Plan{ID: "p", AmountUSD: -1, Period: Period{Kind: PeriodDaily}}, wantErr: true},
		{name: "unknown kind", plan: Plan{ID: "p", Period: Period{Kind: "yearly"}}, wantErr: true},
		{name: "empty kind", plan: Plan{ID: "p"}, wantErr: true},
		{name: "custom without seconds", plan: Plan{ID: "p", Period: Period{Kind: PeriodCustom}}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errValidate := test.plan.Validate()
			if test.wantErr && errValidate == nil {
				t.Fatal("Validate accepted an invalid plan")
			}
			if !test.wantErr && errValidate != nil {
				t.Fatalf("Validate rejected a valid plan: %v", errValidate)
			}
		})
	}
}

func TestCycleWindowDailyUsesTheConfiguredTimezone(t *testing.T) {
	shanghai := mustLoad(t, "Asia/Shanghai")
	plan := Plan{ID: "p", Period: Period{Kind: PeriodDaily}}
	// 20:00 UTC is already 04:00 the next day in Shanghai, so the window must
	// be the Shanghai day, not the UTC one.
	now := time.Date(2026, 8, 3, 20, 0, 0, 0, time.UTC)
	start, end := plan.CycleWindow(now, shanghai)
	assertWindow(t, start, end,
		time.Date(2026, 8, 4, 0, 0, 0, 0, shanghai),
		time.Date(2026, 8, 5, 0, 0, 0, 0, shanghai))
}

// TestCycleWindowDailySurvivesDST is why boundaries use calendar arithmetic
// instead of adding 24 hours: a spring-forward day is 23 hours long and a
// fall-back day is 25, and both must still start and end at local midnight.
func TestCycleWindowDailySurvivesDST(t *testing.T) {
	newYork := mustLoad(t, "America/New_York")
	plan := Plan{ID: "p", Period: Period{Kind: PeriodDaily}}

	springForward := time.Date(2026, 3, 8, 12, 0, 0, 0, newYork)
	start, end := plan.CycleWindow(springForward, newYork)
	assertWindow(t, start, end,
		time.Date(2026, 3, 8, 0, 0, 0, 0, newYork),
		time.Date(2026, 3, 9, 0, 0, 0, 0, newYork))
	if length := end.Sub(start); length != 23*time.Hour {
		t.Fatalf("spring-forward day = %s, want 23h", length)
	}

	fallBack := time.Date(2026, 11, 1, 12, 0, 0, 0, newYork)
	start, end = plan.CycleWindow(fallBack, newYork)
	if length := end.Sub(start); length != 25*time.Hour {
		t.Fatalf("fall-back day = %s, want 25h", length)
	}
}

func TestCycleWindowWeekly(t *testing.T) {
	monday := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	if monday.Weekday() != time.Monday {
		t.Fatalf("test fixture drift: 2026-08-03 is a %s", monday.Weekday())
	}
	plan := Plan{ID: "p", Period: Period{Kind: PeriodWeekly}}

	// Mid-week lands in the window that opened on the preceding Monday.
	start, end := plan.CycleWindow(time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC), time.UTC)
	assertWindow(t, start, end, monday, time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC))

	// The last moment before the next reset still belongs to the old window.
	start, end = plan.CycleWindow(time.Date(2026, 8, 9, 23, 59, 59, 0, time.UTC), time.UTC)
	assertWindow(t, start, end, monday, time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC))

	// Exactly on the boundary opens the new window.
	start, end = plan.CycleWindow(time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), time.UTC)
	assertWindow(t, start, end,
		time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC))
}

// TestCycleWindowMonthlyHandlesShortMonths is trivial now that the boundary is
// always the first, but February is where a day-of-month scheme would break, so
// it stays covered.
func TestCycleWindowMonthlyHandlesShortMonths(t *testing.T) {
	plan := Plan{ID: "p", Period: Period{Kind: PeriodMonthly}}
	start, end := plan.CycleWindow(time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC), time.UTC)
	assertWindow(t, start, end,
		time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))

	// A leap year does not move the boundary either.
	start, end = plan.CycleWindow(time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC), time.UTC)
	assertWindow(t, start, end,
		time.Date(2028, 2, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2028, 3, 1, 0, 0, 0, 0, time.UTC))
}

func TestCycleWindowCustom(t *testing.T) {
	anchor := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	plan := Plan{ID: "p", Period: Period{Kind: PeriodCustom, Seconds: 3600, Anchor: anchor}}

	start, end := plan.CycleWindow(time.Date(2026, 8, 3, 2, 30, 0, 0, time.UTC), time.UTC)
	assertWindow(t, start, end,
		time.Date(2026, 8, 3, 2, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 3, 3, 0, 0, 0, time.UTC))

	// Exactly on a boundary opens the new window.
	start, end = plan.CycleWindow(time.Date(2026, 8, 3, 3, 0, 0, 0, time.UTC), time.UTC)
	assertWindow(t, start, end,
		time.Date(2026, 8, 3, 3, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 3, 4, 0, 0, 0, time.UTC))
}

// TestCycleWindowCustomBeforeAnchor exercises the floor division: a clock
// behind the anchor must still land inside a whole window rather than snapping
// forward, which would let a key spend twice in one period.
func TestCycleWindowCustomBeforeAnchor(t *testing.T) {
	anchor := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	plan := Plan{ID: "p", Period: Period{Kind: PeriodCustom, Seconds: 3600, Anchor: anchor}}

	start, end := plan.CycleWindow(time.Date(2026, 8, 2, 23, 30, 0, 0, time.UTC), time.UTC)
	assertWindow(t, start, end,
		time.Date(2026, 8, 2, 23, 0, 0, 0, time.UTC),
		anchor)
}

func TestRollCycleArchivesClosedWindow(t *testing.T) {
	plan := Plan{ID: "daily-5", AmountUSD: 5, Period: Period{Kind: PeriodDaily}}
	key := &KeyState{Scope: "s"}

	day1 := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	if !rollCycle(key, plan, day1, time.UTC) {
		t.Fatal("first roll reported no change")
	}
	if key.Cycle.PlanID != plan.ID {
		t.Fatalf("Cycle.PlanID = %q, want %q", key.Cycle.PlanID, plan.ID)
	}
	key.Cycle.SpentUSD = 3
	key.Cycle.Requests = 4

	// Same window: nothing should move.
	if rollCycle(key, plan, day1.Add(2*time.Hour), time.UTC) {
		t.Fatal("roll inside the same window reported a change")
	}
	if key.Cycle.SpentUSD != 3 {
		t.Fatalf("SpentUSD = %v, want the accumulated 3 to survive", key.Cycle.SpentUSD)
	}

	day2 := time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC)
	if !rollCycle(key, plan, day2, time.UTC) {
		t.Fatal("crossing midnight reported no change")
	}
	if key.Cycle.SpentUSD != 0 || key.Cycle.Requests != 0 {
		t.Fatalf("new window did not start clean: %+v", key.Cycle)
	}
	if len(key.RecentCycles) != 1 {
		t.Fatalf("RecentCycles = %+v, want the closed window archived", key.RecentCycles)
	}
	archived := key.RecentCycles[0]
	if archived.SpentUSD != 3 || archived.Requests != 4 || archived.LimitUSD != 5 || archived.PlanID != "daily-5" {
		t.Fatalf("archived cycle = %+v", archived)
	}
}

func TestRollCycleDoesNotArchiveEmptyWindows(t *testing.T) {
	plan := Plan{ID: "p", Period: Period{Kind: PeriodDaily}}
	key := &KeyState{Scope: "s"}
	rollCycle(key, plan, time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC), time.UTC)
	rollCycle(key, plan, time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC), time.UTC)
	rollCycle(key, plan, time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC), time.UTC)
	if len(key.RecentCycles) != 0 {
		t.Fatalf("RecentCycles = %+v, want idle windows not archived", key.RecentCycles)
	}
}

func TestRollCycleCapsArchiveLength(t *testing.T) {
	plan := Plan{ID: "p", AmountUSD: 1, Period: Period{Kind: PeriodDaily}}
	key := &KeyState{Scope: "s"}
	for day := 1; day <= MaxRecentCycles+5; day++ {
		rollCycle(key, plan, time.Date(2026, 1, day, 10, 0, 0, 0, time.UTC), time.UTC)
		key.Cycle.SpentUSD = float64(day)
		key.Cycle.Requests = 1
	}
	rollCycle(key, plan, time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC), time.UTC)

	if len(key.RecentCycles) != MaxRecentCycles {
		t.Fatalf("RecentCycles length = %d, want %d", len(key.RecentCycles), MaxRecentCycles)
	}
	// The oldest entries must be the ones dropped.
	if key.RecentCycles[len(key.RecentCycles)-1].SpentUSD != float64(MaxRecentCycles+5) {
		t.Fatalf("newest archived cycle = %+v", key.RecentCycles[len(key.RecentCycles)-1])
	}
}

// TestCustomPeriodRejectsAnOverflowingLength covers a period long enough that
// converting it to nanoseconds wraps. It wraps to exactly zero at 1<<55
// seconds, which turned the window arithmetic into a division by zero: the
// panic reached the host as an error, and the host fails interceptors open, so
// a single bad plan disabled quota enforcement for every key using it.
func TestCustomPeriodRejectsAnOverflowingLength(t *testing.T) {
	plan := Plan{ID: "p1", AmountUSD: 10, Period: Period{Kind: PeriodCustom, Seconds: 1 << 55}}
	if err := plan.Validate(); err == nil {
		t.Fatal("Validate accepted a period whose nanosecond conversion overflows")
	}

	// A document edited by hand can still carry one, so the window math has to
	// survive it rather than rely on validation having run.
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	start, end := plan.CycleWindow(now, time.UTC)
	if !end.After(start) {
		t.Fatalf("window = [%s, %s), want a non-empty window", start, end)
	}
}

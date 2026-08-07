package billing

import (
	"math"
	"strings"
	"time"
)

// maxCustomPeriodSeconds is the longest custom period that still converts to a
// time.Duration. Beyond it the nanosecond conversion overflows, and it does so
// silently: 1<<55 seconds lands on exactly zero, which would turn the window
// arithmetic below into a division by zero. Validate rejects anything larger so
// the request path never has to survive it, and CycleWindow clamps as well
// because a state document can be edited by hand.
const maxCustomPeriodSeconds = int64(math.MaxInt64) / int64(time.Second)

// Validate reports whether a plan is internally consistent. It is enforced at
// the Management API boundary so an invalid plan can never reach the request
// path, where a period error would have to fail open.
func (p Plan) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return invalidf("订阅计划 ID 不能为空")
	}
	if !(p.AmountUSD > 0) {
		return invalidf("订阅计划 %s：周期额度必须大于 0", p.ID)
	}
	switch p.Period.Kind {
	case PeriodDaily, PeriodWeekly, PeriodMonthly:
		return nil
	case PeriodCustom:
		if p.Period.Seconds <= 0 {
			return invalidf("订阅计划 %s：自定义周期必须大于 0 秒", p.ID)
		}
		if p.Period.Seconds > maxCustomPeriodSeconds {
			return invalidf("订阅计划 %s：自定义周期不能超过 %d 秒", p.ID, maxCustomPeriodSeconds)
		}
		return nil
	default:
		return invalidf("订阅计划 %s：不支持周期类型 %q", p.ID, p.Period.Kind)
	}
}

// CycleWindow returns the half-open window [start, end) containing now, in the
// plugin's configured time zone.
//
// Boundaries are computed with calendar arithmetic rather than by adding fixed
// durations, so a window stays anchored to local midnight across DST
// transitions, where a day is 23 or 25 hours long.
func (p Plan) CycleWindow(now time.Time, loc *time.Location) (time.Time, time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)

	switch p.Period.Kind {
	case PeriodWeekly:
		// Weeks run Monday to Sunday. time.Weekday counts from Sunday, so
		// shifting by six turns Monday into the zero point.
		back := (int(local.Weekday()) + 6) % 7
		start := time.Date(local.Year(), local.Month(), local.Day()-back, 0, 0, 0, 0, loc)
		end := time.Date(local.Year(), local.Month(), local.Day()-back+7, 0, 0, 0, 0, loc)
		return start, end

	case PeriodMonthly:
		// Months run from the first to the first. time.Date normalizes month 13
		// into January of the next year, which is how the end boundary is taken.
		start := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
		end := time.Date(local.Year(), local.Month()+1, 1, 0, 0, 0, 0, loc)
		return start, end

	case PeriodCustom:
		seconds := p.Period.Seconds
		if seconds <= 0 {
			seconds = 1
		} else if seconds > maxCustomPeriodSeconds {
			seconds = maxCustomPeriodSeconds
		}
		period := time.Duration(seconds) * time.Second
		anchor := p.Period.Anchor
		if anchor.IsZero() {
			anchor = p.CreatedAt
		}
		if anchor.IsZero() {
			anchor = now
		}
		// Floor division: a now before the anchor still lands in a whole
		// window rather than snapping forward to the anchor.
		elapsed := now.Sub(anchor)
		windows := int64(elapsed / period)
		if elapsed < 0 && elapsed%period != 0 {
			windows--
		}
		start := anchor.Add(time.Duration(windows) * period)
		return start, start.Add(period)

	default: // PeriodDaily and anything unrecognized.
		start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		end := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc)
		return start, end
	}
}

// FindPlan returns the plan with the given ID.
func (s *State) FindPlan(id string) (Plan, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Plan{}, false
	}
	for _, plan := range s.Plans {
		if plan.ID == id {
			return plan, true
		}
	}
	return Plan{}, false
}

// rollCycle advances a key's window to the one containing now, archiving the
// window that just closed.
//
// Rolling is lazy: it happens whenever a key is read or written rather than on
// a timer, so a restarted process picks up wherever the clock actually is and
// idle keys cost nothing.
func rollCycle(key *KeyState, plan Plan, now time.Time, loc *time.Location) bool {
	if key == nil {
		return false
	}
	start, end := plan.CycleWindow(now, loc)
	if key.Cycle.StartAt.Equal(start) && key.Cycle.EndAt.Equal(end) {
		return false
	}
	archiveCycle(key, plan.AmountUSD, plan.ID)
	key.Cycle = Cycle{PlanID: plan.ID, StartAt: start, EndAt: end}
	return true
}

// archiveCycle retains the current window in the trend history when it holds
// anything worth keeping. limitUSD is the budget that applied while the window
// was open, and fallbackPlanID attributes windows opened before the plan ID was
// recorded on the cycle itself.
//
// Every path that abandons a window — rolling, rebinding, unbinding, a manual
// reset — goes through here, so spend is never silently erased from history.
func archiveCycle(key *KeyState, limitUSD float64, fallbackPlanID string) {
	if key == nil || key.Cycle.StartAt.IsZero() {
		return
	}
	if key.Cycle.SpentUSD <= 0 && key.Cycle.Requests <= 0 {
		return
	}
	planID := key.Cycle.PlanID
	if planID == "" {
		planID = fallbackPlanID
	}
	key.RecentCycles = append(key.RecentCycles, CycleSummary{
		StartAt:  key.Cycle.StartAt,
		EndAt:    key.Cycle.EndAt,
		SpentUSD: key.Cycle.SpentUSD,
		Requests: key.Cycle.Requests,
		LimitUSD: limitUSD,
		PlanID:   planID,
	})
	if len(key.RecentCycles) > MaxRecentCycles {
		key.RecentCycles = key.RecentCycles[len(key.RecentCycles)-MaxRecentCycles:]
	}
}

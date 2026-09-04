package billing

import (
	"math"
	"strings"
	"time"
)

const maxPeriodSeconds = int64(math.MaxInt64) / int64(time.Second)

func (p Plan) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return invalidf("订阅计划 ID 不能为空")
	}
	if !(p.AmountUSD > 0) || math.IsInf(p.AmountUSD, 0) {
		return invalidf("订阅计划 %s：额度必须大于 0", p.ID)
	}
	if p.PeriodSeconds < 0 {
		return invalidf("订阅计划 %s：周期不能小于 0 秒", p.ID)
	}
	if p.PeriodSeconds > maxPeriodSeconds {
		return invalidf("订阅计划 %s：周期不能超过 %d 秒", p.ID, maxPeriodSeconds)
	}
	return nil
}

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

// settleExpiredCycle returns an elapsed timed subscription to its inactive
// initial state. It never starts a new period; reads and manual resets therefore
// cannot make an idle key's clock run.
func settleExpiredCycle(key *KeyState, plan Plan, now time.Time) bool {
	if plan.PeriodSeconds == 0 || key.Cycle.StartAt.IsZero() || key.Cycle.EndAt.IsZero() || now.Before(key.Cycle.EndAt) {
		return false
	}
	key.Cycle = Cycle{}
	return true
}

// activateCycle settles an expired period and lazily starts the next one at the
// instant this key is actually used. A never-reset plan records its first-use
// time for history but intentionally has no EndAt.
func activateCycle(key *KeyState, plan Plan, now time.Time) bool {
	changed := settleExpiredCycle(key, plan, now)
	if !key.Cycle.StartAt.IsZero() {
		return changed
	}
	end := time.Time{}
	if plan.PeriodSeconds > 0 {
		end = now.Add(time.Duration(plan.PeriodSeconds) * time.Second)
	}
	key.Cycle = Cycle{PlanID: plan.ID, StartAt: now, EndAt: end}
	return true
}

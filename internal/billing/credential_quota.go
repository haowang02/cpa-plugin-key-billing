package billing

import "time"

// A response reports only the windows of the limit it used.
type QuotaReport struct {
	Provider         string
	Plan             string
	CreditBalance    string
	CreditsUnlimited *bool
	Windows          []QuotaReportWindow
}

// A window's limit key and length identify it from one report to the next.
type QuotaReportWindow struct {
	LimitKey    string
	Seconds     int64
	UsedPercent float64
	ResetAt     time.Time
}

type QuotaObservation struct {
	At time.Time
	QuotaReport
}

// LatestQuotaReports returns, oldest first, the newest report since since of each window and value;
// an observation keeps only what it is the newest report of.
func (s *Store) LatestQuotaReports(authIndexes []string, since time.Time) (map[string][]QuotaObservation, error) {
	return withRepository(s, func(repo Repository) (map[string][]QuotaObservation, error) {
		return repo.LatestQuotaReports(authIndexes, since)
	})
}

type QuotaQuery struct {
	Provider string
	At       time.Time
	Quota    []byte
}

// SaveQuotaQuery replaces the credential's previous query and drops every query made before since.
func (s *Store) SaveQuotaQuery(authIndex string, query QuotaQuery, since time.Time) error {
	_, err := withRepository(s, func(repo Repository) (struct{}, error) {
		return struct{}{}, repo.SaveQuotaQuery(authIndex, query, since)
	})
	return err
}

func (s *Store) QuotaQueries(authIndexes []string, since time.Time) (map[string]QuotaQuery, error) {
	return withRepository(s, func(repo Repository) (map[string]QuotaQuery, error) {
		return repo.QuotaQueries(authIndexes, since)
	})
}

type QuotaWindowHistory struct {
	LimitKey string
	Seconds  int64
	Reports  []QuotaWindowReport
}

func (s *Store) QuotaWindowHistories(authIndex, provider string, from, to time.Time) ([]QuotaWindowHistory, error) {
	return withRepository(s, func(repo Repository) ([]QuotaWindowHistory, error) {
		return repo.QuotaWindowHistories(authIndex, provider, from, to)
	})
}

type QuotaWindowReport struct {
	At          time.Time
	UsedPercent float64
	ResetAt     time.Time
	Plan        string
}

type QuotaPeriodEnd string

const (
	QuotaPeriodScheduled  QuotaPeriodEnd = "scheduled"
	QuotaPeriodEarlyReset QuotaPeriodEnd = "early_reset"
)

// A zero End means the period still runs; ResetAt is when it was scheduled to end.
type QuotaWindowPeriod struct {
	Start, End, ResetAt time.Time
	EndReason           QuotaPeriodEnd
	// UsedPercent is the highest share reported in the period.
	UsedPercent float64
	Plans       []string
}

func (p QuotaWindowPeriod) EndsAfter(at time.Time) bool { return p.End.IsZero() || p.End.After(at) }

// Shared with CPA-Manager-Plus. Concurrent requests report out of order, so a counter reset needs a drop to
// almost none, or by half and 20 points; reset times within a minute are one boundary.
const (
	quotaResetMinimumPriorPercent = 5
	quotaResetNearZeroPercent     = 1
	quotaResetLargeDropPercent    = 20
	quotaBoundaryJitter           = time.Minute
	// A request that began before a counter reset can still report the old
	// counter this long after the reset.
	quotaReorderHorizon = 10 * time.Minute
)

// A period begins when its reset time says the window began and ends at that reset time or at a counter reset;
// any other change of reset time reschedules it.
func QuotaWindowPeriods(length time.Duration, reports []QuotaWindowReport, now time.Time) []QuotaWindowPeriod {
	var periods []QuotaWindowPeriod
	var current *QuotaWindowPeriod
	var last QuotaWindowReport
	// reset is the latest early reset while a request that began before it
	// may still report the counter it cleared.
	var reset struct {
		at, resetAt time.Time
		prior       float64
		moved       bool
	}
	end := func(at time.Time, reason QuotaPeriodEnd) {
		if !current.ResetAt.IsZero() && !at.Before(current.ResetAt) {
			at, reason = current.ResetAt, QuotaPeriodScheduled
		}
		current.End, current.EndReason = maxTime(at, current.Start), reason
		periods = append(periods, *current)
		current, reset.at = nil, time.Time{}
	}
	for _, report := range reports {
		// A reset time that had already passed says nothing about the window.
		scheduled := report.ResetAt.After(report.At)
		start := report.At
		if scheduled {
			start = minTime(start, report.ResetAt.Add(-length))
		}
		if current != nil && !reset.at.IsZero() && report.At.Before(reset.at.Add(quotaReorderHorizon)) &&
			(reset.moved && scheduled && absDuration(report.ResetAt.Sub(reset.resetAt)) <= quotaBoundaryJitter ||
				!reset.moved && report.UsedPercent >= reset.prior-quotaResetNearZeroPercent) {
			// The report shows the window before the reset, so its request began first.
			previous := periods[len(periods)-1]
			periods = periods[:len(periods)-1]
			previous.End, previous.EndReason = time.Time{}, ""
			previous.UsedPercent = max(previous.UsedPercent, current.UsedPercent)
			previous.Plans = appendPlans(previous.Plans, current.Plans...)
			current, reset.at = &previous, time.Time{}
		}
		if current != nil {
			moved := scheduled && !current.ResetAt.IsZero() && absDuration(report.ResetAt.Sub(current.ResetAt)) > quotaBoundaryJitter
			switch {
			case moved && !start.Before(current.ResetAt.Add(-quotaBoundaryJitter)):
				end(current.ResetAt, QuotaPeriodScheduled)
			case quotaCounterReset(last.UsedPercent, report.UsedPercent) ||
				moved && report.ResetAt.After(current.ResetAt) && report.UsedPercent < last.UsedPercent &&
					report.UsedPercent <= quotaResetNearZeroPercent:
				transition := report.At
				if moved {
					transition = maxTime(start, last.At)
				}
				resetAt := current.ResetAt
				end(transition, QuotaPeriodEarlyReset)
				if ended := periods[len(periods)-1]; ended.EndReason == QuotaPeriodEarlyReset {
					reset.at, reset.resetAt, reset.prior, reset.moved = ended.End, resetAt, last.UsedPercent, moved
				}
				start = periods[len(periods)-1].End
			case moved || current.ResetAt.IsZero() && scheduled:
				// A window nothing has used yet begins anew with each report.
				if current.UsedPercent == 0 {
					current.Start = maxTime(current.Start, start)
				}
				current.ResetAt = report.ResetAt
			}
		}
		if current == nil {
			if len(periods) > 0 {
				start = maxTime(start, periods[len(periods)-1].End)
			}
			current = &QuotaWindowPeriod{Start: start}
			if scheduled {
				current.ResetAt = report.ResetAt
			}
		}
		current.UsedPercent = max(current.UsedPercent, report.UsedPercent)
		current.Plans = appendPlans(current.Plans, report.Plan)
		last = report
	}
	if current != nil && !current.ResetAt.IsZero() && !now.Before(current.ResetAt) {
		end(current.ResetAt, QuotaPeriodScheduled)
	}
	if current != nil {
		periods = append(periods, *current)
	}
	return periods
}

func QuotaPeriodEnding(periods []QuotaWindowPeriod, resetAt time.Time) (QuotaWindowPeriod, bool) {
	for i := len(periods) - 1; i >= 0; i-- {
		if absDuration(periods[i].ResetAt.Sub(resetAt)) <= quotaBoundaryJitter {
			return periods[i], true
		}
	}
	return QuotaWindowPeriod{}, false
}

func quotaCounterReset(previous, current float64) bool {
	return previous >= quotaResetMinimumPriorPercent && current < previous &&
		(current <= quotaResetNearZeroPercent || previous-current >= quotaResetLargeDropPercent && current <= previous/2)
}

func appendPlans(plans []string, values ...string) []string {
	for _, plan := range values {
		if plan != "" && (len(plans) == 0 || plans[len(plans)-1] != plan) {
			plans = append(plans, plan)
		}
	}
	return plans
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

func minTime(left, right time.Time) time.Time {
	if right.Before(left) {
		return right
	}
	return left
}

func maxTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

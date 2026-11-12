package billing

import "time"

// QuotaReport is what one response said about its credential's quota under
// Provider. A response reports only the windows of the limit it used.
type QuotaReport struct {
	Provider         string
	Plan             string
	CreditBalance    string
	CreditsUnlimited *bool
	Windows          []QuotaReportWindow
}

// QuotaReportWindow is a quota window as one response reported it. Its limit
// key and length identify it from one report to the next.
type QuotaReportWindow struct {
	LimitKey    string
	Seconds     int64
	UsedPercent float64
	// ResetAt is zero when the response did not say when the window resets.
	ResetAt time.Time
}

// QuotaObservation is the report stored for a request made At.
type QuotaObservation struct {
	At time.Time
	QuotaReport
}

// LatestQuotaReports returns, oldest first, the newest report made since since
// of each quota window and value on each credential. An observation keeps only
// the windows and values it is the newest report of.
func (s *Store) LatestQuotaReports(authIndexes []string, since time.Time) (map[string][]QuotaObservation, error) {
	return withRepository(s, func(repo Repository) (map[string][]QuotaObservation, error) {
		return repo.LatestQuotaReports(authIndexes, since)
	})
}

// QuotaQuery is what a credential's provider answered when its quota was last
// queried, encoded by the querier.
type QuotaQuery struct {
	Provider string
	At       time.Time
	Quota    []byte
}

// SaveQuotaQuery keeps a credential's quota query in place of its previous
// one and drops every query made before since.
func (s *Store) SaveQuotaQuery(authIndex string, query QuotaQuery, since time.Time) error {
	_, err := withRepository(s, func(repo Repository) (struct{}, error) {
		return struct{}{}, repo.SaveQuotaQuery(authIndex, query, since)
	})
	return err
}

// QuotaQueries returns the quota query of each credential made since since.
func (s *Store) QuotaQueries(authIndexes []string, since time.Time) (map[string]QuotaQuery, error) {
	return withRepository(s, func(repo Repository) (map[string]QuotaQuery, error) {
		return repo.QuotaQueries(authIndexes, since)
	})
}

// QuotaWindowHistory is what a credential's requests reported about one quota
// window, oldest first.
type QuotaWindowHistory struct {
	LimitKey string
	Seconds  int64
	Reports  []QuotaWindowReport
}

// QuotaWindowHistories returns what a credential's requests to provider made
// from from until to reported about each window.
func (s *Store) QuotaWindowHistories(authIndex, provider string, from, to time.Time) ([]QuotaWindowHistory, error) {
	return withRepository(s, func(repo Repository) ([]QuotaWindowHistory, error) {
		return repo.QuotaWindowHistories(authIndex, provider, from, to)
	})
}

// QuotaWindowReport is what one request's response said about a quota window.
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

// QuotaWindowPeriod is one period of a quota window, from Start until End;
// a zero End means it still runs. ResetAt is when it was scheduled to end.
type QuotaWindowPeriod struct {
	Start, End, ResetAt time.Time
	EndReason           QuotaPeriodEnd
	// UsedPercent is the highest share of the window reported in the period.
	UsedPercent float64
	Plans       []string
}

// EndsAfter reports whether the period still runs at, or ends after, at.
func (p QuotaWindowPeriod) EndsAfter(at time.Time) bool { return p.End.IsZero() || p.End.After(at) }

// Boundary rules shared with CPA-Manager-Plus. Requests that run concurrently
// report usage slightly out of order, so a counter reset needs a drop from
// meaningful usage to almost none, or by at least half and 20 points. Reset
// times within a minute of each other are the same boundary.
const (
	quotaResetMinimumPriorPercent = 5
	quotaResetNearZeroPercent     = 1
	quotaResetLargeDropPercent    = 20
	quotaBoundaryJitter           = time.Minute
	// A request that began before a counter reset can still report the old
	// counter this long after the reset.
	quotaReorderHorizon = 10 * time.Minute
)

// QuotaWindowPeriods splits the reports on a window of the given length,
// oldest first, into periods. A period begins when its reset time says the
// window began and ends at that reset time or at a counter reset. Any other
// change of reset time reschedules the period.
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

// QuotaPeriodEnding returns the period scheduled to end at resetAt.
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

package billing

import (
	"sync"
	"testing"
	"time"
)

func sharedStore(t *testing.T, now time.Time) *Store {
	s := newAccountStore(t, now)
	s.ReplaceAll(func(state *State) {
		state.Plans = []Plan{{ID: "shared", CycleScope: CycleScopePlan, Windows: []QuotaWindow{
			{ID: "short", Name: "Short", AmountUSD: wantSubsetCost, PeriodSeconds: 3600},
			{ID: "long", Name: "Long", AmountUSD: 100, PeriodSeconds: 7200},
		}}, {ID: "other", CycleScope: CycleScopePlan, Windows: []QuotaWindow{{ID: "default", Name: "Other", AmountUSD: 10, PeriodSeconds: 3600}}}}
		for _, scope := range []string{"a", "b", "c"} {
			state.Keys[scope] = &KeyState{PlanID: "shared"}
		}
		state.Keys["other"] = &KeyState{PlanID: "other"}
	})
	return s
}

func TestSharedCycleStartsOnlyWithPositiveBilling(t *testing.T) {
	start := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	s := sharedStore(t, start)
	s.Authorize("a", start)
	zero := subsetEvent("a", start)
	zero.Breakdown = TokenBreakdown{}
	s.RecordUsage(zero)
	s.RecordUsageError(zero, RequestError{StatusCode: 502})
	missing := subsetEvent("a", start)
	missing.RequestedAt = time.Time{}
	s.RecordUsage(missing)
	for _, scope := range []string{"a", "b"} {
		v, _ := s.KeyViewForScope(scope)
		if v.Windows[0].Started {
			t.Fatal("admission/zero/invalid usage started a plan")
		}
	}
	billedAt := start.Add(5 * time.Minute)
	event := subsetEvent("a", billedAt)
	event.RequestedAt = start
	s.RecordUsage(event)
	s.now = func() time.Time { return billedAt }
	a, _ := s.KeyViewForScope("a")
	b, _ := s.KeyViewForScope("b")
	if !a.Windows[0].StartAt.Equal(billedAt) || !b.Windows[0].StartAt.Equal(billedAt) || !a.Blocked || b.Blocked || quotaAmountUsed(b.Windows[0]) != 0 {
		t.Fatalf("shared quota a=%+v b=%+v", a, b)
	}
	s.RecordUsage(subsetEvent("b", billedAt.Add(time.Minute)))
	if s.Authorize("b", billedAt.Add(time.Minute)).Allowed {
		t.Fatal("second key quota not enforced")
	}
	s.now = func() time.Time { return billedAt.Add(10*time.Hour + 30*time.Minute) }
	c, _ := s.KeyViewForScope("c")
	if !c.Windows[0].StartAt.Equal(billedAt.Add(10 * time.Hour)) {
		t.Fatal("idle plan drifted", c)
	}
	other, _ := s.KeyViewForScope("other")
	if other.Windows[0].Started {
		t.Fatal("unrelated plan started")
	}
}

func TestSharedPlanConcurrentUsageKeepsIndependentBalances(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	s := sharedStore(t, now)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			scope := "a"
			if i%2 == 1 {
				scope = "b"
			}
			s.RecordUsage(subsetEvent(scope, now))
		}(i)
	}
	wg.Wait()
	for _, scope := range []string{"a", "b"} {
		v, _ := s.KeyViewForScope(scope)
		assertClose(t, "concurrent independent balance", quotaAmountUsed(v.Windows[0]), 15*wantSubsetCost)
		if !v.Windows[0].StartAt.Equal(now) {
			t.Fatal("clock changed")
		}
	}
}

func TestSharedResetRestartsPlanAndRejectsPreResetUsage(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	s := sharedStore(t, now)
	s.RecordUsage(subsetEvent("a", now))
	s.RecordUsage(subsetEvent("b", now))
	s.RecordUsage(subsetEvent("other", now))
	s.ReplaceAll(func(state *State) { state.Keys["c"].DeletedAt = now })
	resetAt := now.Add(time.Minute)
	s.now = func() time.Time { return resetAt }
	result, err := s.ResetCycles(ResetRequest{Mode: "all", Scopes: []string{"a"}})
	if err != nil || result.Keys != 3 {
		t.Fatal(result, err)
	}
	old := subsetEvent("b", resetAt.Add(time.Second))
	old.RequestedAt = now
	s.RecordUsage(old)
	if !s.Plans()[0].StartedAt.IsZero() || len(s.state.Keys["b"].Cycles) != 0 {
		t.Fatal("old completion restarted plan")
	}
	other, _ := s.KeyViewForScope("other")
	if quotaAmountUsed(other.Windows[0]) == 0 {
		t.Fatal("reset affected other plan")
	}
	next := resetAt.Add(2 * time.Minute)
	s.now = func() time.Time { return next }
	s.RecordUsage(subsetEvent("b", next))
	a, _ := s.KeyViewForScope("a")
	b, _ := s.KeyViewForScope("b")
	if !a.Windows[0].StartAt.Equal(next) || !b.Windows[0].StartAt.Equal(next) || quotaAmountUsed(a.Windows[0]) != 0 || quotaAmountUsed(b.Windows[0]) != wantSubsetCost {
		t.Fatal(a, b)
	}
}

func TestCycleScopeSwitchClearsAllKeysAndPreservesHistory(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	s := sharedStore(t, now)
	s.RecordUsage(subsetEvent("a", now))
	scope := CycleScopeKey
	if _, err := s.UpdatePlanWithBindings(PlanPatch{ID: "shared", CycleScope: &scope}, nil); err != nil {
		t.Fatal(err)
	}
	if len(s.state.Keys["a"].Cycles) != 0 || !s.Plans()[0].StartedAt.IsZero() {
		t.Fatal("switch retained shared cycle")
	}
	s.Authorize("a", now)
	s.Authorize("b", now.Add(time.Minute))
	if s.state.Keys["a"].Cycles["short"].StartAt.Equal(s.state.Keys["b"].Cycles["short"].StartAt) {
		t.Fatal("key mode is not independent")
	}
	scope = CycleScopePlan
	if _, err := s.UpdatePlanWithBindings(PlanPatch{ID: "shared", CycleScope: &scope}, nil); err != nil {
		t.Fatal(err)
	}
	if s.Authorize("a", now).Windows[0].Started {
		t.Fatal("shared mode started at admission")
	}
	if len(mustRequestEvents(t, s, RequestEventQuery{}).Entries) != 1 {
		t.Fatal("history lost")
	}
	scope = "invalid"
	if _, err := s.UpdatePlanWithBindings(PlanPatch{ID: "shared", CycleScope: &scope}, nil); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func quotaAmountUsed(window QuotaWindowView) float64 {
	for _, d := range window.Dimensions {
		if d.Metric == QuotaAmount {
			v, _ := d.Used.Float64()
			return v
		}
	}
	return 0
}

func TestSharedFreeUsageStartsOnlyEnabledDimensions(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	for _, metric := range []QuotaMetric{QuotaAmount, QuotaTokens, QuotaRequests} {
		t.Run(string(metric), func(t *testing.T) {
			s := sharedStore(t, now)
			s.ReplaceAll(func(state *State) {
				state.Prices["gpt-5.5"] = CustomPrice{ModelID: "gpt-5.5"}
				w := QuotaWindow{ID: "w", Name: "Budget", PeriodSeconds: 3600}
				switch metric {
				case QuotaAmount:
					w.AmountUSD = 1
				case QuotaTokens:
					w.TokenLimit = 1
				case QuotaRequests:
					w.RequestLimit = 1
				}
				state.Plans[0].Windows = []QuotaWindow{w}
			})
			s.RecordUsage(subsetEvent("a", now))
			v, _ := s.KeyViewForScope("a")
			if metric == QuotaAmount {
				if v.Windows[0].Started {
					t.Fatal("free usage started amount plan")
				}
			} else if !v.Windows[0].Started || !v.Blocked {
				t.Fatal("free usage bypassed enabled quota", v)
			}
		})
	}
}

func TestSharedOutOfOrderFirstBillingChargesBothKeys(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	s := sharedStore(t, now)
	s.RecordUsage(subsetEvent("a", now.Add(time.Second)))
	// The older callback completes price resolution after the newer callback.
	s.RecordUsage(subsetEvent("b", now))
	s.now = func() time.Time { return now.Add(time.Second) }
	for _, scope := range []string{"a", "b"} {
		v, _ := s.KeyViewForScope(scope)
		if !v.Windows[0].StartAt.Equal(now.Add(time.Second)) || quotaAmountUsed(v.Windows[0]) != wantSubsetCost {
			t.Fatal("concurrent first billing was lost", v)
		}
	}
}

package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

func quotaStore(t *testing.T, window QuotaWindow) (*Store, *memoryRepository, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store, repo := newAccountStoreWithRepository(t, now)
	window.ID, window.Name, window.PeriodSeconds = "w", "额度", 3600
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{{ID: "p", Currency: USD, Windows: []QuotaWindow{window}}}
		state.Keys["s"] = &KeyState{PlanID: "p", Preview: "sk-tes…0001"}
	})
	store.Authorize("s", now)
	return store, repo, now
}

func currentQuotaCycle(t *testing.T, store *Store) QuotaCycle {
	t.Helper()
	var cycle QuotaCycle
	store.Read(func(state *State) { cycle = state.Keys["s"].Cycles["w"] })
	return cycle
}

func TestQuotaCountsReportedUsage(t *testing.T) {
	store, _, now := quotaStore(t, QuotaWindow{RequestLimit: 2, TokenLimit: 2500})
	for range 3 {
		if d := store.Authorize("s", now); !d.Allowed || currentQuotaCycle(t, store).UsedRequests != 0 {
			t.Fatalf("admission consumed quota: %+v", d)
		}
	}
	store.RecordUsageError(subsetEvent("s", now), RequestError{StatusCode: 502})
	d := store.Authorize("s", now)
	if !d.Allowed || currentQuotaCycle(t, store).UsedRequests != 0 || currentQuotaCycle(t, store).UsedTokens != 1500 {
		t.Fatalf("failed usage: %+v", d)
	}
	zero := subsetEvent("s", now)
	zero.Breakdown = TokenBreakdown{}
	store.RecordUsage(zero)
	d = store.Authorize("s", now)
	if !d.Allowed || currentQuotaCycle(t, store).UsedRequests != 1 {
		t.Fatalf("successful missing usage: %+v", d)
	}
	store.RecordUsage(subsetEvent("s", now))
	d = store.Authorize("s", now)
	w := d.Windows[0]
	if d.Allowed || currentQuotaCycle(t, store).UsedRequests != 2 || currentQuotaCycle(t, store).UsedTokens != 3000 || len(w.Dimensions) != 2 || !w.Dimensions[0].Blocked || !w.Dimensions[1].Blocked ||
		w.Dimensions[0].Metric != QuotaTokens || w.Dimensions[0].Remaining != "0" {
		t.Fatalf("combined exhaustion: %+v", d)
	}
}

func TestQuotaTracksTokensWithoutPricing(t *testing.T) {
	store, _, now := quotaStore(t, QuotaWindow{TokenLimit: 1000})
	store.ReplaceAll(func(state *State) { state.Prices["gpt-5.5"] = CustomPrice{ModelID: "gpt-5.5", Currency: USD} })
	store.RecordUsage(subsetEvent("s", now))
	w := store.Authorize("s", now).Windows[0]
	if !w.Blocked || currentQuotaCycle(t, store).UsedTokens != 1500 || currentQuotaCycle(t, store).Spending.USD != 0 {
		t.Fatalf("zero price lost tokens: %+v", w)
	}

	store, _, now = quotaStore(t, QuotaWindow{TokenLimit: 1000})
	event := subsetEvent("s", now)
	event.Breakdown = TokenBreakdown{Quality: TokenAccountingUnclassified, TotalTokens: 1200, UnclassifiedTokens: 1200}
	store.RecordUsage(event)
	if w = store.Authorize("s", now).Windows[0]; currentQuotaCycle(t, store).UsedTokens != 1200 || !w.Blocked || currentQuotaCycle(t, store).Spending.USD != 0 {
		t.Fatalf("available tokens lost: %+v", w)
	}
	event.Breakdown.Quality = TokenAccountingInconsistent
	store.RecordUsage(event)
	if w = store.Authorize("s", now).Windows[0]; currentQuotaCycle(t, store).UsedTokens != 1200 {
		t.Fatalf("contradictory tokens charged: %+v", w)
	}
}

func TestQuotaPreservesOverageAndRetriesWrites(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprint(shared), func(t *testing.T) {
			store, repo, now := quotaStore(t, QuotaWindow{RequestLimit: 1, TokenLimit: 1})
			if shared {
				store.ReplaceAll(func(state *State) {
					state.Plans[0].SharedQuota = true
					state.Plans[0].Windows[0].CycleAnchorAt = now.Add(time.Hour)
					state.Keys["other"] = &KeyState{PlanID: "p", Preview: "sk-dum…0002"}
				})
				store.Authorize("s", now)
				store.Authorize("other", now)
			}
			repo.fail = errors.New("dummy write failure")
			var group sync.WaitGroup
			for i := range 8 {
				scope := "s"
				if shared && i%2 == 1 {
					scope = "other"
				}
				group.Add(1)
				go func() {
					defer group.Done()
					store.RecordUsage(subsetEvent(scope, now))
				}()
			}
			group.Wait()
			w := store.Authorize("s", now).Windows[0]
			if w.Dimensions[0].Used != "12000" || w.Dimensions[1].Used != "8" || !w.Blocked {
				t.Fatalf("concurrent overage lost: %+v", w)
			}
			repo.fail = nil
			store.Authorize("s", now)
			if len(repo.requestEvents) != 8 || !store.dirty.empty() {
				t.Fatal("write retry duplicated or lost usage")
			}
		})
	}
}

func TestQuotaConvertsSpendingIntoPlanCurrency(t *testing.T) {
	store, _, now := quotaStore(t, QuotaWindow{Amount: 1})
	store.ReplaceAll(func(state *State) {
		state.Prices["cny-model"] = CustomPrice{ModelID: "cny-model", Currency: CNY, PriceRates: PriceRates{InputPer1M: 7, OutputPer1M: 7}}
	})
	store.RecordUsage(subsetEvent("s", now))
	event := subsetEvent("s", now)
	event.UpstreamModel, event.RouteModel = "cny-model", "cny-model"
	store.RecordUsage(event)
	spending := currentQuotaCycle(t, store).Spending
	assertClose(t, "USD spending", spending.USD, wantSubsetCost)
	assertClose(t, "CNY spending", spending.CNY, 1500*7/1e6)
	used := func() float64 {
		value, _ := store.Authorize("s", now).Windows[0].Dimensions[0].Used.Float64()
		return value
	}
	assertClose(t, "USD plan", used(), wantSubsetCost+1500/1e6)
	cny := CNY
	if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: "p", Currency: &cny}, nil); err != nil {
		t.Fatal(err)
	}
	store.cfg.CNYPerUSD = 8
	assertClose(t, "CNY plan", used(), wantSubsetCost*8+1500*7/1e6)
}

func TestLimitEditsKeepCurrentCounters(t *testing.T) {
	store, _, now := quotaStore(t, QuotaWindow{Amount: 10})
	store.RecordUsage(subsetEvent("s", now))
	windows := []QuotaWindow{{ID: "w", Name: "额度", PeriodSeconds: 3600, RequestLimit: 1}}
	if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: "p", Windows: &windows}, nil); err != nil {
		t.Fatal(err)
	}
	if d := store.Authorize("s", now); d.Allowed || currentQuotaCycle(t, store).UsedRequests != 1 {
		t.Fatalf("enabling request limit reset counters: %+v", d)
	}
	windows[0].RequestLimit = 2
	if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: "p", Windows: &windows}, nil); err != nil {
		t.Fatal(err)
	}
	if !store.Authorize("s", now).Allowed {
		t.Fatal("raised limit did not take effect")
	}
}

func TestQuotaPreservesNumericBoundaries(t *testing.T) {
	store, _, now := quotaStore(t, QuotaWindow{Amount: 1, TokenLimit: maxQuotaCount, RequestLimit: maxQuotaCount})
	store.ReplaceAll(func(state *State) {
		key := state.Keys["s"]
		cycle := key.Cycles["w"]
		cycle.Spending.USD = math.MaxFloat64 * 0.75
		cycle.UsedTokens, cycle.UsedRequests = maxQuotaCount-1, maxQuotaCount-1
		key.Cycles["w"] = cycle
		chargeQuota(key, nil, now, quotaUsage{Spending: Spending{USD: math.MaxFloat64 * 0.75}, Tokens: 1, Requests: 1})
	})
	view := store.Authorize("s", now)
	for _, balance := range view.Windows[0].Dimensions {
		if !balance.Blocked || balance.Remaining != "0" || balance.UsedPercent != 100 {
			t.Fatalf("boundary not exhausted: %+v", balance)
		}
		if balance.Metric != QuotaAmount && balance.Used != "9007199254740991" {
			t.Fatalf("integer counter lost precision: %+v", balance)
		}
	}
	cycle := currentQuotaCycle(t, store)
	if _, err := json.Marshal(cycle); err != nil || cycle.Spending.USD != math.MaxFloat64 {
		t.Fatalf("overflow made the cycle unpersistable: %+v, %v", cycle, err)
	}
	store.ReplaceAll(func(state *State) {
		cycle.UsedTokens, cycle.UsedRequests = math.MaxInt64-1, math.MaxInt64
		state.Keys["s"].Cycles["w"] = cycle
	})
	store.RecordUsage(subsetEvent("s", now))
	cycle = currentQuotaCycle(t, store)
	if cycle.UsedTokens != math.MaxInt64 || cycle.UsedRequests != math.MaxInt64 {
		t.Fatalf("usage counter overflow wrapped: %+v", cycle)
	}
}

func TestSharedQuotaLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	store.now = func() time.Time { return now }
	store.ReplaceAll(func(state *State) {
		for _, scope := range []string{"a", "b", "c"} {
			state.Keys[scope] = &KeyState{Preview: "sk-dum…0001"}
		}
	})
	window := QuotaWindow{
		Name: "Hourly", PeriodSeconds: 3600, CycleAnchorAt: now.Add(time.Hour),
		Amount: 2 * wantSubsetCost, TokenLimit: 3000, RequestLimit: 2,
	}
	long := window
	long.Name, long.PeriodSeconds, long.CycleAnchorAt = "Budget", 7200, now.Add(2*time.Hour)
	plan, err := store.CreatePlanWithBindings(Plan{ID: "pool", SharedQuota: true, Windows: []QuotaWindow{window, long}}, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	store.Authorize("a", now)
	store.RecordUsage(subsetEvent("a", now))
	if err := store.BindKey("b", plan.ID); err != nil {
		t.Fatal(err)
	}
	if !store.Authorize("b", now).Allowed {
		t.Fatal("joining key was blocked before the limit")
	}
	store.RecordUsage(subsetEvent("b", now))
	if err := store.BindKey("c", plan.ID); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"a", "b", "c"} {
		if d := store.Authorize(scope, now); d.Allowed || !d.SharedQuota || !d.Windows[0].Blocked || !d.Windows[1].Blocked {
			t.Fatalf("pool exhaustion did not block %s: %+v", scope, d)
		}
		for _, balance := range store.Authorize(scope, now).Windows[0].Dimensions {
			if !balance.Blocked {
				t.Fatalf("shared %s usage was not accumulated: %+v", balance.Metric, balance)
			}
		}
	}
	if err := store.UnbindKey("a"); err != nil {
		t.Fatal(err)
	}
	if err := store.BindKey("a", plan.ID); err != nil {
		t.Fatal(err)
	}
	if !store.PlanViews()[0].Quota.Blocked || store.Authorize("a", now).Allowed {
		t.Fatal("rebinding refunded pool usage")
	}
	store.ReplaceAll(func(state *State) { state.Keys["b"].DeletedAt = now })
	if store.Authorize("c", now).Allowed {
		t.Fatal("deleting a member refunded pool usage")
	}
	if _, err := store.ClearRequestEvents(0); err != nil {
		t.Fatal(err)
	}
	if !store.PlanViews()[0].Quota.Blocked {
		t.Fatal("history cleanup refunded pool usage")
	}
	now = now.Add(time.Hour)
	d := store.Authorize("c", now)
	if d.Allowed || d.Windows[0].Blocked || !d.Windows[1].Blocked || len(store.state.FindPlan(plan.ID).Cycles) != 1 {
		t.Fatalf("window expiration affected the wrong quota: %+v", d)
	}
	if _, err := store.ResetQuota(ResetRequest{Mode: "all", Scopes: []string{"c", "missing"}}); err == nil || !store.PlanViews()[0].Quota.Blocked {
		t.Fatal("invalid selection partially reset the pool")
	}
	result, err := store.ResetQuota(ResetRequest{Mode: "all", Scopes: []string{"a", "c"}})
	if err != nil || result.Pools != 1 || result.Windows != 1 || len(store.state.Keys["b"].Cycles) != 0 {
		t.Fatalf("pool reset was not atomic or counted twice: %+v, %v", result, err)
	}
	if view := store.PlanViews()[0].Quota; view.Blocked || !view.Windows[0].EndAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("manual reset changed the schedule: %+v", view)
	}
	store.Authorize("c", now)
	store.RecordUsage(subsetEvent("c", now))
	now = now.Add(time.Hour)
	if d := store.Authorize("a", now); !d.Allowed || d.Windows[0].Dimensions[0].Used != "0" || d.Windows[1].Dimensions[0].Used != "0" {
		t.Fatalf("scheduled reset did not replenish pool: %+v", d)
	}
	store.RecordUsage(subsetEvent("a", now))
	shared := false
	if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: plan.ID, SharedQuota: &shared}, nil); err != nil {
		t.Fatal(err)
	}
	if len(store.state.FindPlan(plan.ID).Cycles) != 0 {
		t.Fatal("scope change kept shared counters")
	}
	for _, scope := range []string{"a", "c"} {
		if d := store.Authorize(scope, now); d.SharedQuota || d.Windows[0].Dimensions[0].Used != "0" {
			t.Fatal("scope change retained per-key counters")
		}
	}
}

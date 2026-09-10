package billing

import (
	"strconv"
	"testing"
	"time"
)

func TestQuotaWindowSchedules(t *testing.T) {
	for _, unified := range []bool{false, true} {
		t.Run(strconv.FormatBool(unified), func(t *testing.T) {
			now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
			store := newAccountStore(t, now)
			store.ReplaceAll(func(state *State) {
				windows := []QuotaWindow{
					{ID: "short", Name: "短时", AmountUSD: wantSubsetCost, PeriodSeconds: 3600},
					{ID: "long", Name: "预算", AmountUSD: wantSubsetCost, PeriodSeconds: 7200},
				}
				if unified {
					for i := range windows {
						windows[i].CycleAnchorAt = now.Add(time.Duration(windows[i].PeriodSeconds) * time.Second)
					}
				}
				state.Plans = []Plan{{ID: "p", Windows: windows}}
				state.Keys["s"] = &KeyState{PlanID: "p"}
				state.Keys["later"] = &KeyState{PlanID: "p"}
			})
			for _, scope := range []string{"", "unknown"} {
				if !store.Authorize(scope, now).Allowed {
					t.Fatal("unknown key blocked")
				}
			}
			view, _ := store.KeyViewForScope("s")
			if view.Windows[0].Started != unified || len(store.state.Keys["s"].Cycles) != 0 {
				t.Fatal("quota display changed activation")
			}
			first := store.Authorize("s", now)
			if !first.Allowed || !first.Windows[0].StartAt.Equal(first.Windows[1].StartAt) {
				t.Fatalf("first admission: %+v", first)
			}
			later := store.Authorize("later", now.Add(30*time.Minute))
			if later.Windows[0].EndAt.Equal(first.Windows[0].EndAt) != unified {
				t.Fatalf("second key schedule: %+v", later)
			}
			store.RecordUsage(subsetEvent("s", now))
			blocked := store.Authorize("s", now)
			if blocked.Allowed || !blocked.RetryAt.Equal(now.Add(2*time.Hour)) || !store.Authorize("later", now.Add(30*time.Minute)).Allowed {
				t.Fatalf("independent balances: %+v", blocked)
			}
			for _, w := range blocked.Windows {
				used, _ := w.Dimensions[0].Used.Float64()
				assertClose(t, "window cost", used, wantSubsetCost)
			}
			if rows := mustRequestEvents(t, store, RequestEventQuery{}).Entries; len(rows) != 1 || rows[0].Cost.TotalUSD != wantSubsetCost {
				t.Fatalf("duplicated history: %+v", rows)
			}
			store.now = func() time.Time { return now.Add(time.Hour) }
			view, _ = store.KeyViewForScope("s")
			if view.Windows[0].Started != unified || !view.Windows[1].Blocked {
				t.Fatalf("expiry view: %+v", view)
			}
			blocked = store.Authorize("s", store.Now())
			if blocked.Allowed || blocked.Windows[0].Started != unified || !blocked.Windows[1].Blocked || len(store.state.Keys["s"].Cycles) != 1 {
				t.Fatalf("short expiry while blocked: %+v", blocked)
			}
			at := now.Add(10*time.Hour + 17*time.Minute)
			next := store.Authorize("s", at)
			wantStart := at
			if unified {
				wantStart = now.Add(10 * time.Hour)
			}
			if !next.Allowed || !next.Windows[0].StartAt.Equal(wantStart) || next.Windows[1].Dimensions[0].Used != "0" {
				t.Fatalf("idle restart: %+v", next)
			}
			if err := store.state.Keys["s"].ValidateCycles(store.state.Plans[0]); err != nil {
				t.Fatal(err)
			}
			boundary := next.Windows[1].EndAt
			store.Authorize("s", boundary)
			if !store.state.Keys["s"].Cycles["long"].StartAt.Equal(boundary) {
				t.Fatal("boundary did not start next cycle")
			}
		})
	}
}

func TestQuotaWindowsWithDifferentDimensions(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	plan := Plan{ID: "p", Windows: []QuotaWindow{
		{ID: "requests", Name: "请求", RequestLimit: 1, PeriodSeconds: 3600},
		{ID: "tokens", Name: "Token", TokenLimit: 3000, PeriodSeconds: 7200},
		{ID: "amount", Name: "金额", AmountUSD: 4 * wantSubsetCost, PeriodSeconds: 86400},
	}}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{plan}
		state.Keys["s"] = &KeyState{PlanID: plan.ID}
	})
	for hour := range 4 {
		at := now.Add(time.Duration(hour) * time.Hour)
		store.now = func() time.Time { return at }
		before := store.Authorize("s", at)
		if !before.Allowed || before.Windows[0].Dimensions[0].Used != "0" || before.Windows[1].Dimensions[0].Used.String() != strconv.Itoa((hour%2)*1500) {
			t.Fatalf("hour %d: windows did not reset independently: %+v", hour, before)
		}
		spent, _ := before.Windows[2].Dimensions[0].Used.Float64()
		assertClose(t, "amount carried across shorter windows", spent, float64(hour)*wantSubsetCost)
		store.RecordUsage(subsetEvent("s", at))
		after := store.Authorize("s", at)
		if after.Allowed || !after.Windows[0].Blocked || after.Windows[1].Blocked != (hour%2 == 1) || after.Windows[2].Blocked != (hour == 3) {
			t.Fatalf("hour %d: dimension enforcement interfered: %+v", hour, after)
		}
		reset := at.Add(time.Hour)
		if hour == 3 {
			reset = now.Add(24 * time.Hour)
		}
		if !after.RetryAt.Equal(reset) {
			t.Fatalf("hour %d: retry at %v, want %v", hour, after.RetryAt, reset)
		}
	}
	store.now = func() time.Time { return now.Add(4 * time.Hour) }
	blocked := store.Authorize("s", store.Now())
	if blocked.Allowed || blocked.Windows[0].Started || blocked.Windows[1].Started || !blocked.Windows[2].Blocked {
		t.Fatalf("short window reset bypassed the amount limit: %+v", blocked)
	}
	store.now = func() time.Time { return now.Add(24 * time.Hour) }
	restored := store.Authorize("s", store.Now())
	if !restored.Allowed || restored.Windows[0].Dimensions[0].Used != "0" || restored.Windows[1].Dimensions[0].Used != "0" || restored.Windows[2].Dimensions[0].Used != "0" {
		t.Fatalf("windows did not recover after all exhausted quotas reset: %+v", restored)
	}
}

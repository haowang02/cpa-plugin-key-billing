package billing

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func newAccountStore(t *testing.T, now time.Time) *Store {
	store, _ := newAccountStoreWithRepository(t, now)
	return store
}

func newAccountStoreWithRepository(t *testing.T, now time.Time) (*Store, *memoryRepository) {
	t.Helper()
	store, repo := newStoreWithRepository(t)
	store.now = func() time.Time { return now }
	store.ReplaceAll(func(state *State) {
		state.Prices = map[string]CustomPrice{"gpt-5.5": {
			ModelID: "gpt-5.5", PriceRates: PriceRates{InputPer1M: 1,
				OutputPer1M:     2,
				CacheReadPer1M:  floatPtr(0.1),
				CacheWritePer1M: floatPtr(1.25)},
		}}
	})
	return store, repo
}

func subsetEvent(scope string, at time.Time) UsageEvent {
	return UsageEvent{
		Scope: scope, KeyPreview: "sk-tes…0001", AuthIndex: "auth-codex", ExecutorType: "CodexExecutor",
		ReasoningEffort: "high", ServiceTier: "auto", At: at, RequestedAt: at,
		UpstreamModel: "gpt-5.5", RouteModel: "gpt-5.5",
		Breakdown: completeBreakdown(500, 400, 100, 500, 200),
	}
}

func admittedEvent(store *Store, scope string, at time.Time) UsageEvent {
	store.Authorize(scope, at)
	return subsetEvent(scope, at)
}

const wantSubsetCost = 0.0005 + 0.00004 + 0.000125 + 0.001

func TestRecordUsageSeparatesNormalAndErrorEvents(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store, repo := newAccountStoreWithRepository(t, now)
	if _, err := store.ClearPluginLogs(); err != nil {
		t.Fatal(err)
	}
	store.RecordUsage(subsetEvent("scope-a", now))
	store.RecordUsageError(subsetEvent("scope-a", now.Add(time.Hour)), RequestError{StatusCode: 502})

	entries := mustRequestEvents(t, store, RequestEventQuery{}).Entries
	if len(entries) != 2 || len(repo.requestEvents) != 1 || len(repo.requestErrors) != 1 {
		t.Fatalf("request events = %d, normal writes = %d, error writes = %d", len(entries), len(repo.requestEvents), len(repo.requestErrors))
	}
	if !entries[0].Failed || entries[1].Failed {
		t.Fatalf("request events = %+v", entries)
	}
	errors, err := store.RequestErrors(RequestErrorQuery{})
	if err != nil || len(errors.Entries) != 1 || errors.Entries[0].StatusCode != 502 {
		t.Fatalf("request errors = %+v, err = %v", errors.Entries, err)
	}
	if logs := mustPluginLogs(t, store); len(logs) != 0 {
		t.Fatalf("usage events leaked into plugin logs: %+v", logs)
	}
}

func TestRecordUsageStoresSafeAccount(t *testing.T) {
	const downstreamKey = "sk-dummy-downstream-0001"
	for _, test := range []struct{ authType, account, want string }{
		{"oauth", "user@example.com", "user@example.com"},
		{"apikey", "sk-dummy-upstream-0001", "sk-dum…0001"},
		{"oauth", downstreamKey, ""},
		{"", "dummy-unknown-secret", ""},
	} {
		t.Run(test.authType+"/"+test.account, func(t *testing.T) {
			now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
			store := newAccountStore(t, now)
			event := subsetEvent(CallerScope(downstreamKey), now)
			event.AuthIndex = ""
			event.Provider, event.AuthType, event.Account = "codex", test.authType, test.account
			store.RecordUsage(event)
			store.RecordUsageError(event, RequestError{StatusCode: 502})
			view := mustRequestEvents(t, store, RequestEventQuery{})
			if len(view.Entries) != 2 {
				t.Fatalf("events = %+v", view)
			}
			for _, entry := range view.Entries {
				if entry.Provider != "codex" || entry.Account != test.want {
					t.Fatalf("event identity = %+v", entry)
				}
			}
		})
	}
}

func TestRecordUsageGroupsAndPricesByBillingModel(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	store.ReplaceAll(func(state *State) {
		state.Prices["claude/gpt-latest"] = CustomPrice{
			ModelID: "claude/gpt-latest", PriceRates: PriceRates{InputPer1M: 3, OutputPer1M: 4},
		}
	})
	event := subsetEvent("scope-a", now)
	event.RouteModel = "claude/gpt-latest"
	store.RecordUsage(event)

	entries := mustRequestEvents(t, store, RequestEventQuery{}).Entries
	if len(entries) != 1 || entries[0].UpstreamModel != "gpt-5.5" || entries[0].BillingModel != "claude/gpt-latest" {
		t.Fatalf("request events = %+v", entries)
	}
	assertClose(t, "CostUSD", entries[0].Cost.TotalUSD, 0.0015+0.0012+0.0003+0.002)
}

func TestRecordUsagePreservesHostModelAndDurationValues(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	event := subsetEvent("scope-a", now)
	event.UpstreamModel = "gpt-5.5(high)"
	event.RouteModel = "gpt-5.5(high)"
	event.Latency = 999 * time.Microsecond
	event.TTFT = 1500 * time.Microsecond
	store.RecordUsage(event)

	entries := mustRequestEvents(t, store, RequestEventQuery{}).Entries
	if len(entries) != 1 {
		t.Fatalf("request events = %+v", entries)
	}
	entry := entries[0]
	if entry.UpstreamModel != "gpt-5.5(high)" || entry.BillingModel != "gpt-5.5" ||
		entry.LatencyMS != 0 || entry.TTFTMS != 1 {
		t.Fatalf("request event = %+v", entry)
	}
}

func TestUsageRequestTimesPreserveWindowAttribution(t *testing.T) {
	start := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, start)
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{{ID: "p", Windows: []QuotaWindow{
			{ID: "short", Name: "短时", AmountUSD: 5, PeriodSeconds: 3600},
			{ID: "long", Name: "预算", AmountUSD: 10, PeriodSeconds: 86400},
		}}}
		state.Keys["scope-a"] = &KeyState{PlanID: "p"}
	})
	store.Authorize("scope-a", start)
	store.Authorize("scope-a", start.Add(2*time.Hour))
	event := subsetEvent("scope-a", start.Add(150*time.Minute))
	wantShort := QuotaCycle{PlanID: "p", StartAt: start.Add(2 * time.Hour), EndAt: start.Add(3 * time.Hour)}
	wantLong := QuotaCycle{PlanID: "p", StartAt: start, EndAt: start.Add(24 * time.Hour),
		SpentUSD: wantSubsetCost, UsedTokens: 1500, UsedRequests: 1}
	for _, requestedAt := range []time.Time{start, {}, start.Add(48 * time.Hour)} {
		event.RequestedAt = requestedAt
		store.RecordUsage(event)
		store.Read(func(state *State) {
			cycles := state.Keys["scope-a"].Cycles
			if len(cycles) != 2 || cycles["short"] != wantShort || cycles["long"] != wantLong {
				t.Fatalf("request time %s changed current cycles: %+v", requestedAt, cycles)
			}
		})
	}
	if rows := mustRequestEvents(t, store, RequestEventQuery{}).Entries; len(rows) != 3 {
		t.Fatal("usage history lost")
	}
	found := false
	for _, entry := range mustPluginLogs(t, store) {
		found = found || strings.Contains(entry.Message, "请求时间")
	}
	if !found {
		t.Fatal("missing time was not diagnosed")
	}
}

func TestUsageAfterAdministrativeChange(t *testing.T) {
	for _, unified := range []bool{false, true} {
		for _, operation := range []string{"reset", "rebind", "schedule"} {
			t.Run(fmt.Sprintf("%t/%s", unified, operation), func(t *testing.T) {
				start := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
				now := start
				store := newAccountStore(t, start)
				store.now = func() time.Time { return now }
				store.ReplaceAll(func(state *State) {
					state.Plans = []Plan{
						{ID: "daily", Windows: []QuotaWindow{{ID: "default", Name: "额度", AmountUSD: 5, PeriodSeconds: 86400}}},
						{ID: "weekly", Windows: []QuotaWindow{{ID: "default", Name: "额度", AmountUSD: 5, PeriodSeconds: 604800}}},
					}
					if unified {
						for i := range state.Plans {
							w := &state.Plans[i].Windows[0]
							w.CycleAnchorAt = start.Add(time.Duration(w.PeriodSeconds) * time.Second)
						}
					}
					state.Keys["scope-a"] = &KeyState{PlanID: "daily"}
				})
				store.Authorize("scope-a", now)
				store.RecordUsage(subsetEvent("scope-a", start))
				now = start.Add(time.Hour)
				var err error
				switch operation {
				case "reset":
					_, err = store.ResetQuota(ResetRequest{Mode: "all", Scopes: []string{"scope-a"}})
				case "rebind":
					err = store.BindKey("scope-a", "weekly")
				case "schedule":
					windows := append([]QuotaWindow(nil), store.state.Plans[0].Windows...)
					if unified {
						windows[0].CycleAnchorAt = now.Add(12 * time.Hour)
					} else {
						windows[0].PeriodSeconds = 43200
					}
					_, err = store.UpdatePlanWithBindings(PlanPatch{ID: "daily", Windows: &windows}, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				late := subsetEvent("scope-a", now)
				late.RequestedAt = start
				store.RecordUsage(late)
				if store.state.Keys["scope-a"].Cycles["default"].SpentUSD != 0 {
					t.Fatal("old completion restored cleared usage")
				}
				store.Authorize("scope-a", now)
				before := store.state.Keys["scope-a"].Cycles["default"]
				store.RecordUsage(late)
				if after := store.state.Keys["scope-a"].Cycles["default"]; after != before {
					t.Fatalf("old request charged replacement cycle: %+v", after)
				}
				store.RecordUsage(subsetEvent("scope-a", now))
				cycle := store.state.Keys["scope-a"].Cycles["default"]
				if cycle.SpentUSD != wantSubsetCost || cycle.UsedRequests != 1 || cycle.UsedTokens != 1500 {
					t.Fatalf("new usage lost: %+v", cycle)
				}
				if len(mustRequestEvents(t, store, RequestEventQuery{}).Entries) != 4 {
					t.Fatal("administrative change lost request history")
				}
			})
		}
	}
}

func TestReferenceUsageLogsAppliedTierRatesAndBillingModel(t *testing.T) {
	store, _ := newReferencePriceStore(t, 0)
	if _, err := store.ClearPluginLogs(); err != nil {
		t.Fatal(err)
	}
	store.RecordUsage(UsageEvent{
		UpstreamModel: "gpt-5.6-sol", RouteModel: "codex/gpt-5.6-sol(xhigh)", At: store.Now(),
		Breakdown: completeBreakdown(300000, 0, 0, 100, 0),
	})
	logs := mustPluginLogs(t, store)
	if len(logs) != 1 || logs[0].Level != PluginLogDebug {
		t.Fatalf("reference usage logs = %+v", logs)
	}
	for _, want := range []string{`billing_model="codex/gpt-5.6-sol"`, "输入=$10", "输出=$45", "费用=$3.00450000"} {
		if !strings.Contains(logs[0].Message, want) {
			t.Fatalf("reference usage log missing %q: %s", want, logs[0].Message)
		}
	}
}

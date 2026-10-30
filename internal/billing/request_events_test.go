package billing

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRecordUsagePersistsComputedRequestEvent(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	store.RecordUsage(subsetEvent("scope-a", now))

	view := mustRequestEvents(t, store, RequestEventQuery{})
	if len(view.Entries) != 1 || view.Total != 1 {
		t.Fatalf("view = %+v", view)
	}
	entry := view.Entries[0]
	if entry.Scope != "scope-a" || entry.ExecutorType != "CodexExecutor" ||
		entry.ReasoningEffort != "high" || entry.ServiceTier != "auto" ||
		entry.UpstreamModel != "gpt-5.5" || entry.BillingModel != "gpt-5.5" ||
		entry.PriceSource != PriceSourceCustom || entry.Failed || entry.AccountingQuality != TokenAccountingComplete {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.Cost.UncachedInputTokens != 500 || entry.Cost.CacheReadTokens != 400 ||
		entry.Cost.CacheWriteTokens != 100 || entry.Cost.BilledOutputTokens != 500 {
		t.Fatalf("Cost = %+v", entry.Cost)
	}
	assertClose(t, "Total", entry.Cost.Total, wantSubsetCost)
}

func TestRequestEventsKeepTheRetentionWindow(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store, repo := newAccountStoreWithRepository(t, start)
	store.RecordUsage(subsetEvent("scope-a", start))
	store.RecordUsageError(subsetEvent("scope-a", start.Add(time.Hour)), RequestError{StatusCode: 502})
	cfg := store.Config()
	cfg.RequestEventRetentionDays = 7
	if err := store.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return start.Add(7 * 24 * time.Hour) }
	if view := mustRequestEvents(t, store, RequestEventQuery{}); view.Total != 2 {
		t.Fatalf("boundary view = %+v", view)
	}
	store.now = func() time.Time { return start.Add(7*24*time.Hour + time.Hour + time.Nanosecond) }
	if view := mustRequestEvents(t, store, RequestEventQuery{}); view.Total != 0 {
		t.Fatalf("expired view = %+v", view)
	}
	failures, err := store.RequestErrors(RequestErrorQuery{})
	if err != nil || failures.Total != 0 {
		t.Fatalf("expired errors = %+v, %v", failures, err)
	}
	store.RecordUsage(subsetEvent("scope-a", store.Now()))
	if len(repo.requestEvents) != 1 || len(repo.requestErrors) != 0 {
		t.Fatal("append did not prune expired requests and errors")
	}
	store.now = func() time.Time { return start.Add(10 * 24 * time.Hour) }
	cfg.RequestEventRetentionDays = 1
	if err := store.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if len(repo.requestEvents) != 0 {
		t.Fatal("reconfiguration did not apply retention")
	}
}

func TestClearRequestEventsPreservesQuotaAndFlushesPendingUsage(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store, repo := newAccountStoreWithRepository(t, start)
	plan, err := store.CreatePlanWithBindings(Plan{Name: "Cleanup quota", Currency: USD, Windows: []QuotaWindow{{Name: "Weekly", Amount: 10, PeriodSeconds: 604800}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store.ReplaceAll(func(state *State) { state.ensureKey("scope-a", "sk-demo…0001") })
	if err := store.BindKey("scope-a", plan.ID); err != nil {
		t.Fatal(err)
	}
	store.RecordUsageError(subsetEvent("scope-a", start), RequestError{StatusCode: 502})
	repo.fail = errors.New("disk unavailable")
	store.RecordUsage(subsetEvent("scope-a", start.Add(time.Hour)))
	if _, err := store.ClearRequestEvents(0); err == nil {
		t.Fatal("clear ignored a failed pending write")
	}
	if len(repo.requestErrors) != 1 || store.dirty.empty() {
		t.Fatal("failed cleanup discarded pending usage or persisted history")
	}
	repo.fail = nil
	store.now = func() time.Time { return start.Add(24*time.Hour + time.Hour) }
	before := store.KeyViews()
	cleared, err := store.ClearRequestEvents(1)
	if err != nil || cleared != 1 {
		t.Fatalf("clear before boundary = %d, %v", cleared, err)
	}
	if len(repo.requestEvents) != 1 || len(repo.requestErrors) != 0 || !store.dirty.empty() {
		t.Fatal("cleanup did not flush pending usage or preserve the cutoff boundary")
	}
	cleared, err = store.ClearRequestEvents(0)
	if err != nil || cleared != 1 {
		t.Fatalf("clear all = %d, %v", cleared, err)
	}
	if len(repo.requestEvents) != 0 || len(repo.requestErrors) != 0 {
		t.Fatal("clear all left events")
	}
	after := store.KeyViews()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("cleanup changed API keys")
	}
	store.RecordUsage(subsetEvent("scope-a", start.Add(2*time.Hour)))
	if view := mustRequestEvents(t, store, RequestEventQuery{}); view.Total != 1 {
		t.Fatalf("deleted events restored = %+v", view)
	}
}

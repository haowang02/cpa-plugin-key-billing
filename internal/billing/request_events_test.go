package billing

import (
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
		entry.PriceSource != PriceSourceOverride || entry.Failed || entry.AccountingQuality != TokenAccountingComplete {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.Cost.UncachedInputTokens != 500 || entry.Cost.CacheReadTokens != 400 ||
		entry.Cost.CacheWriteTokens != 100 || entry.Cost.BilledOutputTokens != 500 {
		t.Fatalf("Cost = %+v", entry.Cost)
	}
	assertClose(t, "TotalUSD", entry.Cost.TotalUSD, wantSubsetCost)
}

func TestRequestEventsKeepTheRetentionWindow(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store, repo := newAccountStoreWithRepository(t, start)
	for i := range 3 {
		store.RecordUsage(subsetEvent("scope-a", start.Add(time.Duration(i)*time.Minute)))
	}
	if view := mustRequestEvents(t, store, RequestEventQuery{}); len(view.Entries) != 3 || view.Total != 3 {
		t.Fatalf("view total = %d, want every entry inside the window", view.Total)
	}

	store.now = func() time.Time { return start.Add(RequestEventRetention + 24*time.Hour) }
	if view := mustRequestEvents(t, store, RequestEventQuery{}); view.Total != 0 {
		t.Fatalf("view = %+v, want the window emptied by age", view)
	}
	store.RecordUsage(subsetEvent("scope-a", store.Now()))
	if len(repo.requestEvents) != 1 {
		t.Fatalf("stored %d entries, want the stale ones dropped on append", len(repo.requestEvents))
	}
}

package billing

import (
	"strings"
	"testing"
	"time"
)

func failingStore(t *testing.T) *Store {
	t.Helper()
	store, _ := newStoreWithRepository(t)
	store.ReplaceAll(func(state *State) {
		state.Keys["scope-a"] = &KeyState{Preview: "sk-tes…0001", Label: "Alice"}
	})
	return store
}

func admissionPluginLogs(t *testing.T, store *Store) []PluginLog {
	t.Helper()
	events := mustPluginLogs(t, store)
	kept := make([]PluginLog, 0, len(events))
	for _, event := range events {
		if strings.HasPrefix(event.Message, "Quota blocked: ") {
			kept = append(kept, event)
		}
	}
	return kept
}

// A client that retries a blocked key would otherwise write the same line into
// the log on every attempt.
func TestQuotaBlockIsReportedOncePerCycle(t *testing.T) {
	store := failingStore(t)
	cycle := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	window := QuotaWindow{ID: "d", Name: "额度", Amount: 10, PeriodSeconds: 7 * 24 * 3600}
	blocked := Decision{
		PlanID: "weekly", PlanName: "Weekly 10",
		QuotaView: QuotaView{Blocked: true, RetryAt: cycle.Add(7 * 24 * time.Hour), Windows: []QuotaWindowView{
			window.view(QuotaCycle{StartAt: cycle, EndAt: cycle.Add(7 * 24 * time.Hour), Spending: Spending{USD: 10.4}}, USD, 7),
		}},
	}

	for range 3 {
		store.ReportQuotaBlock("scope-a", "/v1/messages", blocked)
	}
	events := admissionPluginLogs(t, store)
	if len(events) != 1 || events[0].Level != PluginLogInfo {
		t.Fatalf("events = %+v, want the onset reported once, as information", events)
	}
	for _, want := range []string{"Quota blocked: ", "Alice · sk-tes…0001", "/v1/messages", "$10.4000 / $10.0000", "Weekly 10"} {
		if !strings.Contains(events[0].Message, want) {
			t.Fatalf("message = %q, want it to name %q", events[0].Message, want)
		}
	}

	// The next window is a new exhaustion and worth saying again.
	rolled := blocked
	rolled.Windows = append([]QuotaWindowView(nil), blocked.Windows...)
	rolled.Windows[0].StartAt = cycle.Add(7 * 24 * time.Hour)
	store.ReportQuotaBlock("scope-a", "/v1/messages", rolled)
	if events := admissionPluginLogs(t, store); len(events) != 2 {
		t.Fatalf("events = %+v, want the new window reported", events)
	}

	// A key that is not blocked has nothing to report.
	store.ReportQuotaBlock("scope-a", "/v1/messages", Decision{Allowed: true})
	if events := admissionPluginLogs(t, store); len(events) != 2 {
		t.Fatalf("events = %+v, want an allowed request left out", events)
	}
}

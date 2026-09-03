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
		if strings.HasPrefix(event.Message, "额度拦截：") {
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
	blocked := Decision{
		PlanID: "weekly", PlanName: "Weekly 10", LimitUSD: 10, SpentUSD: 10.4,
		CycleStartAt: cycle, ResetAt: cycle.Add(7 * 24 * time.Hour),
	}

	for range 3 {
		store.ReportQuotaBlock("scope-a", "/v1/messages", blocked)
	}
	events := admissionPluginLogs(t, store)
	if len(events) != 1 || events[0].Level != PluginLogInfo {
		t.Fatalf("events = %+v, want the onset reported once, as information", events)
	}
	for _, want := range []string{"额度拦截：", "Alice · sk-tes…0001", "/v1/messages", "$10.4000 / $10.0000", "Weekly 10"} {
		if !strings.Contains(events[0].Message, want) {
			t.Fatalf("message = %q, want it to name %q", events[0].Message, want)
		}
	}

	// The next window is a new exhaustion and worth saying again.
	rolled := blocked
	rolled.CycleStartAt = cycle.Add(7 * 24 * time.Hour)
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

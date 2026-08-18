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
		state.Credentials["auth-codex"] = Credential{Provider: "codex", Account: "ops@example.com"}
	})
	return store
}

func finish(store *Store, outcome RequestOutcome, record *UsageRecord, reason string) {
	store.BeginRequest("req-1", PendingRequest{
		Scope: "scope-a", Endpoint: "/v1/messages", AuthIndex: "auth-codex",
	})
	store.SetRequestCredential("req-1", "auth-codex")
	store.FinishRequest("req-1", record, outcome, reason)
}

func requestEvents(t *testing.T, store *Store) []Event {
	t.Helper()
	events := mustEvents(t, store)
	kept := make([]Event, 0, len(events))
	for _, event := range events {
		if strings.HasPrefix(event.Message, "请求失败：") || strings.HasPrefix(event.Message, "额度拦截：") ||
			strings.HasPrefix(event.Message, "模型拦截：") {
			kept = append(kept, event)
		}
	}
	return kept
}

// The plugin log has to name who the failed request belonged to, what it was
// asking for, which credential served it and why it ended — an operator reading
// it should not have to correlate anything by hand.
func TestFailedRequestIsReportedWithItsDetails(t *testing.T) {
	store := failingStore(t)
	finish(store, OutcomeFailed, &UsageRecord{
		BillingModel: "gpt-5.5", UpstreamModel: "gpt-5.5", Generate: true, Responded: true,
	}, "upstream stream closed before a terminal event（internal_server_error）")

	events := requestEvents(t, store)
	if len(events) != 1 || events[0].Level != EventError {
		t.Fatalf("events = %+v, want one error entry", events)
	}
	for _, want := range []string{
		"请求失败：", "Alice · sk-tes…0001", "/v1/messages", "gpt-5.5",
		"codex · ops@example.com", "req-1", "upstream stream closed before a terminal event",
	} {
		if !strings.Contains(events[0].Message, want) {
			t.Fatalf("message = %q, want it to name %q", events[0].Message, want)
		}
	}
}

// An upstream that refuses a request outright produces no usage, so nothing
// about it reaches the billing log. The plugin log is then the only record that
// the request happened at all.
func TestFailedRequestWithNothingToBillIsStillReported(t *testing.T) {
	store := failingStore(t)
	finish(store, OutcomeFailed, &UsageRecord{Generate: true, Responded: false}, "")

	events := requestEvents(t, store)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want the unbillable failure reported", events)
	}
	if !strings.Contains(events[0].Message, "插件未观察到错误内容") {
		t.Fatalf("message = %q, want it to say no error content was seen", events[0].Message)
	}
	if entries := mustLogs(t, store, LogQuery{}).Entries; len(entries) != 0 {
		t.Fatalf("billing log = %+v, want a request with nothing to bill left out of it", entries)
	}
}

// A request that ended normally, and one the client walked away from, are not
// failures. Cancellation is ordinary client behaviour and is already visible in
// the billing log, where it carries the tokens it produced.
func TestSucceededAndCanceledRequestsAreNotReported(t *testing.T) {
	store := failingStore(t)
	record := &UsageRecord{BillingModel: "gpt-5.5", Generate: true, Responded: true}

	finish(store, "", record, "")
	finish(store, OutcomeCanceled, record, "")

	if events := requestEvents(t, store); len(events) != 0 {
		t.Fatalf("events = %+v, want nothing reported for a request that did not fail", events)
	}
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
	events := requestEvents(t, store)
	if len(events) != 1 || events[0].Level != EventInfo {
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
	if events := requestEvents(t, store); len(events) != 2 {
		t.Fatalf("events = %+v, want the new window reported", events)
	}

	// A key that is not blocked has nothing to report.
	store.ReportQuotaBlock("scope-a", "/v1/messages", Decision{Allowed: true})
	if events := requestEvents(t, store); len(events) != 2 {
		t.Fatalf("events = %+v, want an allowed request left out", events)
	}
}

// A client looping on a model it may not call would otherwise write the same
// line into the log on every attempt.
func TestModelBlockIsReportedOncePerModel(t *testing.T) {
	store := failingStore(t)
	refused := ModelDecision{Model: "chat/slow", Models: []string{"chat/fast"}}

	for range 3 {
		store.ReportModelBlock("scope-a", "/v1/messages", refused)
	}
	events := requestEvents(t, store)
	if len(events) != 1 || events[0].Level != EventInfo {
		t.Fatalf("events = %+v, want the onset reported once, as information", events)
	}
	for _, want := range []string{"模型拦截：", "Alice · sk-tes…0001", "/v1/messages", "chat/slow", "chat/fast"} {
		if !strings.Contains(events[0].Message, want) {
			t.Fatalf("message = %q, want it to name %q", events[0].Message, want)
		}
	}

	// Another model is another thing the operator has not been told about.
	other := refused
	other.Model = "chat/other"
	store.ReportModelBlock("scope-a", "/v1/messages", other)
	if events := requestEvents(t, store); len(events) != 2 {
		t.Fatalf("events = %+v, want the second model reported", events)
	}

	// Changing what the key may call makes the first refusal worth reporting
	// again: it is no longer the state of affairs the operator was told about.
	store.ReplaceAll(func(state *State) {
		state.ModelGroups = []ModelGroup{{ID: "g", Name: "基础", Models: []string{"chat/fast"}}}
	})
	if errSet := store.SetKeyModels("scope-a", []string{"g"}, nil); errSet != nil {
		t.Fatalf("SetKeyModels error = %v", errSet)
	}
	store.ReportModelBlock("scope-a", "/v1/messages", refused)
	if events := requestEvents(t, store); len(events) != 3 {
		t.Fatalf("events = %+v, want the refusal reported after the grant changed", events)
	}

	// An allowed request has nothing to report.
	store.ReportModelBlock("scope-a", "/v1/messages", ModelDecision{Allowed: true})
	if events := requestEvents(t, store); len(events) != 3 {
		t.Fatalf("events = %+v, want an allowed request left out", events)
	}
}

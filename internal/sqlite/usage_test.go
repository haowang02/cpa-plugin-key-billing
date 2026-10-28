package sqlite

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func TestUsageByModelAppliesEveryFilter(t *testing.T) {
	d := openTestDB(t)
	event := func(minute int, scope, authIndex, provider, model string, tokens int64) billing.RequestEvent {
		entry := requestEvent(scope, eventStart.Add(time.Duration(minute)*time.Minute))
		entry.AuthIndex, entry.Provider, entry.UpstreamModel, entry.BillingModel = authIndex, provider, model, ""
		entry.Cost = billing.Cost{UncachedInputTokens: tokens, BilledOutputTokens: tokens, Total: float64(tokens) / 100}
		return entry
	}
	mustSave(t, d, billing.NewState(), billing.Changes{NormalRequestEvents: []billing.RequestEvent{
		event(0, "alice", "auth-a", "codex", "gpt-5.5", 100),
		event(1, "alice", "auth-a", "codex", "gpt-5.3-codex-spark", 200),
		event(2, "bob", "auth-a", "codex", "gpt-5.5", 400),
		event(3, "bob", "auth-b", "claude", "claude-opus", 800),
		event(4, "", "auth-b", "claude", "", 1600),
	}})
	for _, test := range []struct {
		name, scope string
		query       billing.UsageQuery
		want        string
	}{
		{"most tokens first", "", billing.UsageQuery{}, "×1 claude-opus×1 gpt-5.5×2 gpt-5.3-codex-spark×1"},
		{"a credential's metered models", "", billing.UsageQuery{AuthIndex: "auth-a", Provider: "Codex",
			ModelMatch: billing.ModelMatch{Exclude: []string{"SPARK"}}}, "gpt-5.5×2"},
		{"scope", "alice", billing.UsageQuery{}, "gpt-5.3-codex-spark×1 gpt-5.5×1"},
		{"keys with the unassigned one", "", billing.UsageQuery{KeyScopes: []string{"", "alice"}}, "×1 gpt-5.3-codex-spark×1 gpt-5.5×1"},
		{"unknown model", "", billing.UsageQuery{Models: []string{""}}, "×1"},
		{"source", "", billing.UsageQuery{Sources: []string{"claude · ops@example.com"}}, "×1 claude-opus×1"},
	} {
		test.query.From, test.query.To = eventStart, eventStart.Add(time.Hour)
		usage, err := d.UsageByModel(test.scope, test.query, 7)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, row := range usage {
			got = append(got, row.Model+"×"+strconv.FormatInt(row.Requests, 10))
		}
		if strings.Join(got, " ") != test.want {
			t.Errorf("%s: %v, want %s", test.name, got, test.want)
		}
	}
}

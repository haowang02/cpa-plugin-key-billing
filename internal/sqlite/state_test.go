package sqlite

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	return openDatabase(t, filepath.Join(t.TempDir(), "state.db"))
}

func openDatabase(t *testing.T, path string) *DB {
	t.Helper()
	database, errOpen := Open(path)
	if errOpen != nil {
		t.Fatalf("Open error = %v", errOpen)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func mustSave(t *testing.T, database *DB, state *billing.State, changes billing.Changes) {
	t.Helper()
	if errSave := database.Save(state, changes); errSave != nil {
		t.Fatalf("Save error = %v", errSave)
	}
}

func mustLoad(t *testing.T, database *DB) billing.Snapshot {
	t.Helper()
	snapshot, errLoad := database.Load()
	if errLoad != nil {
		t.Fatalf("Load error = %v", errLoad)
	}
	return snapshot
}

func price(value float64) *float64 { return &value }

// Everything the plugin enforces against has to come back exactly as it went
// in: an unset cache price is not a zero one, and an inactive subscription
// cycle is recognized by comparing against the zero time.
func TestStateSurvivesAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	start := time.Date(2026, 8, 12, 9, 30, 0, 0, time.UTC)

	state := billing.NewState()
	state.Plans = []billing.Plan{
		{ID: "weekly", Name: "Weekly 10", AmountUSD: 10, Period: billing.Period{Kind: billing.PeriodWeekly}},
		{ID: "custom", Name: "Custom", AmountUSD: 2.5, Period: billing.Period{Kind: billing.PeriodCustom, Seconds: 3600}},
	}
	state.Prices = []billing.PriceRule{
		{Pattern: "gpt-5.5", InputPer1M: 1, OutputPer1M: 2, CacheReadPer1M: price(0.1)},
		{Pattern: "*claude*", InputPer1M: 3, OutputPer1M: 15, CacheWritePer1M: price(0), LongContext: &billing.LongContextPrice{
			ThresholdInputTokens: 200000, InputPer1M: 6, OutputPer1M: 22.5, CacheReadPer1M: price(0.6),
		}},
	}
	state.Keys["scope-a"] = &billing.KeyState{
		Preview: "sk-tes…0001", Label: "Alice", InConfig: true, PlanID: "weekly",
		Cycle:    billing.Cycle{PlanID: "weekly", StartAt: start, EndAt: start.Add(7 * 24 * time.Hour), SpentUSD: 1.5},
		Lifetime: billing.Totals{CostUSD: 1.5, Requests: 3, UncachedInputTokens: 100, OutputTokens: 40},
		ByModel: map[string]*billing.Totals{
			"gpt-5.5": {CostUSD: 1.5, Requests: 3, UncachedInputTokens: 100, OutputTokens: 40},
		},
	}
	state.Keys["scope-b"] = &billing.KeyState{
		DeletedAt: start.Add(time.Hour), PlanID: "custom",
		ByModel: map[string]*billing.Totals{},
	}
	state.Credentials["auth-1"] = billing.Credential{Provider: "codex", Account: "ops@example.com"}

	database := openDatabase(t, path)
	mustSave(t, database, state, billing.Changes{AllKeys: true, Plans: true, Prices: true, Credentials: true})
	if errClose := database.Close(); errClose != nil {
		t.Fatalf("Close error = %v", errClose)
	}

	reloaded := mustLoad(t, openDatabase(t, path)).State
	if !reflect.DeepEqual(reloaded.Plans, state.Plans) {
		t.Fatalf("plans = %+v, want %+v", reloaded.Plans, state.Plans)
	}
	if !reflect.DeepEqual(reloaded.Prices, state.Prices) {
		t.Fatalf("prices = %+v, want %+v", reloaded.Prices, state.Prices)
	}
	if !reflect.DeepEqual(reloaded.Credentials, state.Credentials) {
		t.Fatalf("credentials = %+v, want %+v", reloaded.Credentials, state.Credentials)
	}
	if !reflect.DeepEqual(reloaded.Keys, state.Keys) {
		t.Fatalf("keys = %+v, want %+v", reloaded.Keys["scope-a"], state.Keys["scope-a"])
	}
	if cycle := reloaded.Keys["scope-b"].Cycle; cycle != (billing.Cycle{}) {
		t.Fatalf("cycle = %+v, want the zero cycle to survive as itself", cycle)
	}
}

// Billing one request must write that request's key, not the whole record.
func TestSaveWritesOnlyTheNamedKeys(t *testing.T) {
	database := openTestDB(t)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Lifetime: billing.Totals{CostUSD: 1}, ByModel: map[string]*billing.Totals{}}
	state.Keys["scope-b"] = &billing.KeyState{Lifetime: billing.Totals{CostUSD: 2}, ByModel: map[string]*billing.Totals{}}
	mustSave(t, database, state, billing.Changes{AllKeys: true})

	state.Keys["scope-a"].Lifetime.CostUSD = 5
	state.Keys["scope-b"].Lifetime.CostUSD = 9
	mustSave(t, database, state, billing.Changes{Keys: []string{"scope-a"}})

	stored := mustLoad(t, database).State
	if stored.Keys["scope-a"].Lifetime.CostUSD != 5 {
		t.Fatalf("scope-a = %v, want the named key written", stored.Keys["scope-a"].Lifetime.CostUSD)
	}
	if stored.Keys["scope-b"].Lifetime.CostUSD != 2 {
		t.Fatalf("scope-b = %v, want the unnamed key untouched", stored.Keys["scope-b"].Lifetime.CostUSD)
	}

	// Replacing the whole set is what retires a record the state dropped.
	delete(state.Keys, "scope-b")
	mustSave(t, database, state, billing.Changes{AllKeys: true})
	if stored = mustLoad(t, database).State; len(stored.Keys) != 1 || stored.Keys["scope-b"] != nil {
		t.Fatalf("keys = %+v, want only the surviving record", stored.Keys)
	}
}

// Per-model totals belong to the key that earned them: a model a key stopped
// using leaves with it, and so does every model of a key that was retired.
func TestPerModelTotalsLeaveWithWhatTheyBelongedTo(t *testing.T) {
	database := openTestDB(t)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{ByModel: map[string]*billing.Totals{
		"gpt-5.5": {Requests: 1}, "claude": {Requests: 2},
	}}
	state.Keys["scope-b"] = &billing.KeyState{ByModel: map[string]*billing.Totals{"claude": {Requests: 3}}}
	mustSave(t, database, state, billing.Changes{AllKeys: true})

	delete(state.Keys["scope-a"].ByModel, "claude")
	mustSave(t, database, state, billing.Changes{Keys: []string{"scope-a"}})
	byModel := mustLoad(t, database).State.Keys["scope-a"].ByModel
	if len(byModel) != 1 || byModel["gpt-5.5"] == nil {
		t.Fatalf("ByModel = %+v, want only the model still in use", byModel)
	}

	delete(state.Keys, "scope-b")
	mustSave(t, database, state, billing.Changes{AllKeys: true})
	var orphans int
	if errCount := database.db.QueryRow(
		"SELECT count(*) FROM key_models WHERE scope NOT IN (SELECT scope FROM api_keys)").Scan(&orphans); errCount != nil {
		t.Fatalf("count orphans: %v", errCount)
	}
	if orphans != 0 {
		t.Fatalf("orphaned model totals = %d, want the retired key to have taken them", orphans)
	}
}

// A database written by a newer plugin is refused rather than misread: opening
// it would otherwise mean writing today's columns over tomorrow's rows.
func TestOpenRejectsANewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)
	if _, errVersion := database.db.Exec("PRAGMA user_version = 99"); errVersion != nil {
		t.Fatalf("set user_version: %v", errVersion)
	}
	if errClose := database.Close(); errClose != nil {
		t.Fatalf("Close error = %v", errClose)
	}
	if _, errOpen := Open(path); errOpen == nil {
		t.Fatal("Open accepted a database from a newer plugin")
	}
}

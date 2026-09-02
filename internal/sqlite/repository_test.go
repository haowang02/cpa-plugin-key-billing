package sqlite

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
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
	database, err := Open(path)
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func mustSave(t *testing.T, database *DB, state *billing.State, changes billing.Changes) {
	t.Helper()
	if err := database.Save(state, changes); err != nil {
		t.Fatalf("Save error = %v", err)
	}
}

func mustLoad(t *testing.T, database *DB) billing.Snapshot {
	t.Helper()
	snapshot, err := database.Load(time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	return snapshot
}

func price(value float64) *float64 { return &value }

func TestRepositoryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	start := time.Date(2026, 8, 12, 9, 30, 0, 0, time.UTC)
	state := billing.NewState()
	state.Plans = []billing.Plan{{ID: "weekly", Name: "Weekly 10", AmountUSD: 10, Period: billing.Period{Kind: billing.PeriodWeekly}}}
	state.Prices = []billing.PriceRule{{Pattern: "gpt-5.5", InputPer1M: 1, OutputPer1M: 2, CacheReadPer1M: price(.1)}}
	state.ModelGroups = []billing.ModelGroup{{ID: "fast", Name: "Fast", Models: []string{"gpt-5.5"}}}
	state.Keys["scope-a"] = &billing.KeyState{Preview: "sk-tes…0001", Label: "Alice", InConfig: true,
		PlanID: "weekly", ConcurrencyLimit: 7, ModelGroupIDs: []string{"fast"}, Models: []string{"other"},
		Cycle: billing.Cycle{PlanID: "weekly", StartAt: start, EndAt: start.Add(7 * 24 * time.Hour), SpentUSD: 1.5}}
	state.Credentials["auth-1"] = billing.Credential{Provider: "codex", Account: "ops@example.com"}

	database := openDatabase(t, path)
	mustSave(t, database, state, billing.Changes{AllKeys: true, Plans: true, Prices: true, ModelGroups: true, Credentials: true,
		RequestErrorEvents: []billing.RequestErrorEvent{{Event: billing.RequestEvent{At: start, Scope: "scope-a", AuthIndex: "auth-1", Provider: "codex", BillingModel: "gpt-5.5"},
			Error: billing.RequestError{StatusCode: 429, ErrorType: "rate_limit", Body: "limited"}}}})
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openDatabase(t, path)
	loaded := mustLoad(t, reopened)
	if !reflect.DeepEqual(loaded.State, state) {
		t.Fatalf("state = %+v, want %+v", loaded.State, state)
	}
	errors, err := reopened.RequestErrors(billing.RequestErrorQuery{Limit: 10}, time.Time{})
	if err != nil || len(errors.Entries) != 1 || errors.Entries[0].StatusCode != 429 {
		t.Fatalf("request errors = %+v, err = %v", errors.Entries, err)
	}
}

func TestSaveWritesOnlyNamedKeys(t *testing.T) {
	database := openTestDB(t)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Label: "A"}
	state.Keys["scope-b"] = &billing.KeyState{Label: "B"}
	mustSave(t, database, state, billing.Changes{AllKeys: true})
	state.Keys["scope-a"].Label = "A2"
	state.Keys["scope-b"].Label = "B2"
	mustSave(t, database, state, billing.Changes{Keys: []string{"scope-a"}})
	stored := mustLoad(t, database).State
	if stored.Keys["scope-a"].Label != "A2" || stored.Keys["scope-b"].Label != "B" {
		t.Fatalf("keys = %+v", stored.Keys)
	}
}

func TestKeyGrantIsRewrittenWithTheKey(t *testing.T) {
	database := openTestDB(t)
	state := billing.NewState()
	state.ModelGroups = []billing.ModelGroup{{ID: "fast", Name: "Fast", Models: []string{"gpt-5.5"}}}
	state.Keys["scope-a"] = &billing.KeyState{ModelGroupIDs: []string{"fast"}, Models: []string{"claude"}}
	state.Keys["scope-b"] = &billing.KeyState{ModelGroupIDs: []string{"fast"}}
	mustSave(t, database, state, billing.Changes{AllKeys: true, ModelGroups: true})
	state.Keys["scope-a"].ModelGroupIDs = nil
	state.Keys["scope-a"].Models = []string{"other"}
	mustSave(t, database, state, billing.Changes{Keys: []string{"scope-a"}})
	stored := mustLoad(t, database).State
	if len(stored.Keys["scope-a"].ModelGroupIDs) != 0 || !reflect.DeepEqual(stored.Keys["scope-a"].Models, []string{"other"}) || len(stored.Keys["scope-b"].ModelGroupIDs) != 1 {
		t.Fatalf("stored grants = %+v", stored.Keys)
	}
}

func TestFreshSchemaVersionAndTables(t *testing.T) {
	database := openTestDB(t)
	var version int
	if err := database.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("schema version = %d, err = %v", version, err)
	}
	want := map[string]bool{
		"api_keys": true, "key_model_groups": true, "key_allowed_models": true,
		"model_groups": true, "model_group_models": true, "plans": true,
		"prices": true, "credentials": true, "request_events": true,
		"request_errors": true, "plugin_logs": true,
	}
	rows, err := database.db.Query(`SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !want[name] {
			t.Fatalf("unexpected table %q", name)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("missing tables: %v", want)
	}
}

func TestOpenRejectsExistingSchemas(t *testing.T) {
	for _, version := range []int{0, 8, 9} {
		t.Run(fmt.Sprintf("version_%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			database := openDatabase(t, path)
			if _, err := database.db.Exec(fmt.Sprintf("CREATE TABLE old_marker (value TEXT); PRAGMA user_version = %d", version)); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(path); err == nil {
				_ = reopened.Close()
				t.Fatal("Open accepted an existing schema")
			} else if !strings.Contains(err.Error(), "不迁移旧数据") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

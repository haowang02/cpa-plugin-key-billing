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

// A zero cutoff reads the log whole, which is what a test asking what was
// stored wants; retention itself is exercised where it is enforced.
func mustLoad(t *testing.T, database *DB) billing.Snapshot {
	t.Helper()
	snapshot, errLoad := database.Load(time.Time{})
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
		Preview: "sk-tes…0001", Label: "Alice", InConfig: true, PlanID: "weekly", ConcurrencyLimit: 7,
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
	state.ModelGroups = []billing.ModelGroup{
		{ID: "fast", Name: "Fast", Models: []string{"gpt-5.5", "chat/fast"}},
		{ID: "empty", Name: "Empty"},
	}
	state.Keys["scope-a"].ModelGroupIDs = []string{"fast", "empty"}
	state.Keys["scope-a"].Models = []string{"claude-sonnet-4-5"}
	state.Credentials["auth-1"] = billing.Credential{Provider: "codex", Account: "ops@example.com"}

	database := openDatabase(t, path)
	mustSave(t, database, state, billing.Changes{
		AllKeys: true, Plans: true, Prices: true, ModelGroups: true, Credentials: true,
	})
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
	if !reflect.DeepEqual(reloaded.ModelGroups, state.ModelGroups) {
		t.Fatalf("model groups = %+v, want %+v", reloaded.ModelGroups, state.ModelGroups)
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

func TestOpenMigratesConcurrencyLimitWithoutLosingKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Label: "Alice", InConfig: true, ConcurrencyLimit: 9}
	mustSave(t, database, state, billing.Changes{AllKeys: true})
	if _, errDowngrade := database.db.Exec(`
		ALTER TABLE api_keys DROP COLUMN concurrency_limit;
		PRAGMA user_version = 4`); errDowngrade != nil {
		t.Fatalf("prepare version 4 database: %v", errDowngrade)
	}
	if errClose := database.Close(); errClose != nil {
		t.Fatalf("Close error = %v", errClose)
	}

	reopened := openDatabase(t, path)
	key := mustLoad(t, reopened).State.Keys["scope-a"]
	if key == nil || key.Label != "Alice" || !key.InConfig || key.ConcurrencyLimit != 0 {
		t.Fatalf("migrated key = %+v, want preserved data and an unlimited default", key)
	}
	var version int
	if errVersion := reopened.db.QueryRow("PRAGMA user_version").Scan(&version); errVersion != nil {
		t.Fatalf("read user_version: %v", errVersion)
	}
	if version != schemaVersion {
		t.Fatalf("version = %d, want %d", version, schemaVersion)
	}
}

func TestOpenMigratesUsageLogExecutorTypeWithoutLosingEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Label: "Alice"}
	mustSave(t, database, state, billing.Changes{
		AllKeys: true,
		Log: []billing.LogEntry{{
			At: time.Now(), Scope: "scope-a", ExecutorType: "CodexExecutor",
			UpstreamModel: "gpt-5.5", BillingModel: "gpt-5.5",
		}},
	})
	if _, errDowngrade := database.db.Exec(`
		ALTER TABLE usage_log DROP COLUMN executor_type;
		PRAGMA user_version = 5`); errDowngrade != nil {
		t.Fatalf("prepare version 5 database: %v", errDowngrade)
	}
	if errClose := database.Close(); errClose != nil {
		t.Fatalf("Close error = %v", errClose)
	}

	reopened := openDatabase(t, path)
	logs, errLogs := reopened.Logs(billing.LogQuery{Limit: 10}, time.Time{})
	if errLogs != nil {
		t.Fatalf("read migrated logs: %v", errLogs)
	}
	if len(logs.Entries) != 1 || logs.Entries[0].UpstreamModel != "gpt-5.5" || logs.Entries[0].ExecutorType != "" {
		t.Fatalf("migrated logs = %+v", logs.Entries)
	}
	var version int
	if errVersion := reopened.db.QueryRow("PRAGMA user_version").Scan(&version); errVersion != nil {
		t.Fatalf("read user_version: %v", errVersion)
	}
	if version != schemaVersion {
		t.Fatalf("version = %d, want %d", version, schemaVersion)
	}
}

func TestOpenMigratesUsageLogRequestOptionsWithoutLosingEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Label: "Alice"}
	mustSave(t, database, state, billing.Changes{
		AllKeys: true,
		Log: []billing.LogEntry{{
			At: time.Now(), Scope: "scope-a", ExecutorType: "CodexExecutor",
			ReasoningEffort: "high", ServiceTier: "priority",
			UpstreamModel: "gpt-5.5", BillingModel: "gpt-5.5",
		}},
	})
	if _, errDowngrade := database.db.Exec(`
		ALTER TABLE usage_log DROP COLUMN reasoning_effort;
		ALTER TABLE usage_log DROP COLUMN service_tier;
		PRAGMA user_version = 6`); errDowngrade != nil {
		t.Fatalf("prepare version 6 database: %v", errDowngrade)
	}
	if errClose := database.Close(); errClose != nil {
		t.Fatalf("Close error = %v", errClose)
	}

	reopened := openDatabase(t, path)
	logs, errLogs := reopened.Logs(billing.LogQuery{Limit: 10}, time.Time{})
	if errLogs != nil {
		t.Fatalf("read migrated logs: %v", errLogs)
	}
	if len(logs.Entries) != 1 || logs.Entries[0].ExecutorType != "CodexExecutor" ||
		logs.Entries[0].UpstreamModel != "gpt-5.5" || logs.Entries[0].ReasoningEffort != "" ||
		logs.Entries[0].ServiceTier != "" {
		t.Fatalf("migrated logs = %+v", logs.Entries)
	}
	var version int
	if errVersion := reopened.db.QueryRow("PRAGMA user_version").Scan(&version); errVersion != nil {
		t.Fatalf("read user_version: %v", errVersion)
	}
	if version != schemaVersion {
		t.Fatalf("version = %d, want %d", version, schemaVersion)
	}
}

func TestOpenMigratesPluginLogFailureWithoutLosingEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)
	mustAppendEvent(t, database, logStart, billing.EventInfo, "保留旧插件日志", time.Time{})
	if _, errDowngrade := database.db.Exec(`
		ALTER TABLE plugin_log DROP COLUMN request_failure;
		PRAGMA user_version = 7`); errDowngrade != nil {
		t.Fatalf("prepare version 7 database: %v", errDowngrade)
	}
	if errClose := database.Close(); errClose != nil {
		t.Fatalf("Close error = %v", errClose)
	}

	reopened := openDatabase(t, path)
	events := mustEvents(t, reopened, time.Time{})
	if len(events) != 1 || events[0].Message != "保留旧插件日志" || events[0].RequestFailure != nil {
		t.Fatalf("migrated events = %+v", events)
	}
	var version int
	if errVersion := reopened.db.QueryRow("PRAGMA user_version").Scan(&version); errVersion != nil {
		t.Fatalf("read user_version: %v", errVersion)
	}
	if version != schemaVersion {
		t.Fatalf("version = %d, want %d", version, schemaVersion)
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

// A key's grant is rewritten in full with the key, the way its per-model totals
// are: a group it no longer holds has to leave the database with the selection.
func TestKeyGrantIsRewrittenWithTheKey(t *testing.T) {
	database := openTestDB(t)
	state := billing.NewState()
	state.ModelGroups = []billing.ModelGroup{{ID: "fast", Name: "Fast", Models: []string{"gpt-5.5"}}}
	state.Keys["scope-a"] = &billing.KeyState{
		ModelGroupIDs: []string{"fast"}, Models: []string{"claude", "gpt-5.5"},
		ByModel: map[string]*billing.Totals{},
	}
	state.Keys["scope-b"] = &billing.KeyState{ModelGroupIDs: []string{"fast"}, ByModel: map[string]*billing.Totals{}}
	mustSave(t, database, state, billing.Changes{AllKeys: true, ModelGroups: true})

	state.Keys["scope-a"].ModelGroupIDs = nil
	state.Keys["scope-a"].Models = []string{"claude"}
	mustSave(t, database, state, billing.Changes{Keys: []string{"scope-a"}})
	stored := mustLoad(t, database).State
	if key := stored.Keys["scope-a"]; len(key.ModelGroupIDs) != 0 || len(key.Models) != 1 {
		t.Fatalf("grant = %+v / %+v, want the narrowed selection", key.ModelGroupIDs, key.Models)
	}
	if key := stored.Keys["scope-b"]; len(key.ModelGroupIDs) != 1 {
		t.Fatalf("grant = %+v, want the unnamed key untouched", key.ModelGroupIDs)
	}

	// Rewriting the group list must not take the keys' bindings with it, which
	// is why those two tables reference the key rather than the group.
	mustSave(t, database, state, billing.Changes{ModelGroups: true})
	if stored = mustLoad(t, database).State; len(stored.Keys["scope-b"].ModelGroupIDs) != 1 {
		t.Fatalf("grant = %+v, want it to survive a group rewrite", stored.Keys["scope-b"].ModelGroupIDs)
	}

	// A retired key takes its grant with it.
	delete(state.Keys, "scope-b")
	mustSave(t, database, state, billing.Changes{AllKeys: true})
	var orphans int
	if errCount := database.db.QueryRow(
		"SELECT count(*) FROM key_model_groups WHERE scope NOT IN (SELECT scope FROM api_keys)").Scan(&orphans); errCount != nil {
		t.Fatalf("count orphans: %v", errCount)
	}
	if orphans != 0 {
		t.Fatalf("orphaned bindings = %d, want the retired key to have taken them", orphans)
	}
}

func TestOpenRejectsMismatchedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)
	if _, errVersion := database.db.Exec("PRAGMA user_version = 99"); errVersion != nil {
		t.Fatalf("set user_version: %v", errVersion)
	}
	if errClose := database.Close(); errClose != nil {
		t.Fatalf("Close error = %v", errClose)
	}
	if _, errOpen := Open(path); errOpen == nil {
		t.Fatal("Open accepted a database with a mismatched schema")
	}
}

func TestOpenRepairsCurrentSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)
	if _, errDrop := database.db.Exec("DROP TABLE model_groups"); errDrop != nil {
		t.Fatalf("drop model_groups: %v", errDrop)
	}
	if errClose := database.Close(); errClose != nil {
		t.Fatalf("Close error = %v", errClose)
	}

	reopened := openDatabase(t, path)
	state := billing.NewState()
	state.ModelGroups = []billing.ModelGroup{{ID: "fast", Name: "Fast", Models: []string{"gpt-5.5"}}}
	mustSave(t, reopened, state, billing.Changes{ModelGroups: true})
	if groups := mustLoad(t, reopened).State.ModelGroups; len(groups) != 1 || groups[0].ID != "fast" {
		t.Fatalf("groups = %+v, want the recreated table usable", groups)
	}
}

func TestOpenMigratesPreviousSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Label: "Alice", Lifetime: billing.Totals{CostUSD: 7}}
	mustSave(t, database, state, billing.Changes{AllKeys: true})
	if _, errTable := database.db.Exec(`
		CREATE TABLE billing_log (
			id INTEGER PRIMARY KEY, at INTEGER, scope TEXT, auth_index TEXT, upstream_model TEXT,
			billing_model TEXT, outcome TEXT, accounting_quality TEXT, price_source TEXT,
			reasoning_tokens INTEGER, total_usd REAL, uncached_input_usd REAL, cache_read_usd REAL,
			cache_write_usd REAL, output_usd REAL, uncached_input_tokens INTEGER,
			cache_read_tokens INTEGER, cache_write_tokens INTEGER, billed_output_tokens INTEGER,
			tiered INTEGER, long_context INTEGER, threshold_input_tokens INTEGER,
			applied_input_per_1m REAL, applied_output_per_1m REAL,
			applied_cache_read_per_1m REAL, applied_cache_write_per_1m REAL
		);
		INSERT INTO billing_log VALUES (
			1, 100, 'scope-a', 'auth-a', 'upstream', 'route', 'canceled', 'complete', 'override',
			2, 0.5, 0.1, 0.2, 0.0, 0.2, 100, 20, 0, 50, 0, 0, 0, 1, 2, 1, 1
		);
		INSERT INTO billing_log VALUES (
			2, 101, 'scope-a', '', '', '', 'canceled', '', '',
			0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0
		)`); errTable != nil {
		t.Fatalf("create legacy billing_log: %v", errTable)
	}
	if _, errVersion := database.db.Exec("PRAGMA user_version = 3"); errVersion != nil {
		t.Fatalf("set user_version: %v", errVersion)
	}
	if errClose := database.Close(); errClose != nil {
		t.Fatalf("Close error = %v", errClose)
	}

	reopened := openDatabase(t, path)
	loaded := mustLoad(t, reopened)
	if key := loaded.State.Keys["scope-a"]; key == nil || key.Label != "Alice" || key.Lifetime.CostUSD != 7 {
		t.Fatalf("key = %+v", key)
	}
	logs, errLogs := reopened.Logs(billing.LogQuery{Limit: 10}, time.Time{})
	if errLogs != nil {
		t.Fatalf("read migrated logs: %v", errLogs)
	}
	if len(logs.Entries) != 2 || !logs.Entries[0].Failed || logs.Entries[0].Cost != (billing.Cost{}) ||
		!logs.Entries[1].Failed || logs.Entries[1].Cost.TotalUSD != 0.5 ||
		logs.Entries[1].ReasoningTokens != 2 || logs.Entries[1].LatencyMS != 0 || logs.Entries[1].TTFTMS != 0 {
		t.Fatalf("migrated logs = %+v", logs.Entries)
	}
	var version, oldTables int
	if errVersion := reopened.db.QueryRow("PRAGMA user_version").Scan(&version); errVersion != nil {
		t.Fatalf("read user_version: %v", errVersion)
	}
	if errCount := reopened.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'billing_log'").Scan(&oldTables); errCount != nil {
		t.Fatalf("count billing_log: %v", errCount)
	}
	if version != schemaVersion || oldTables != 0 {
		t.Fatalf("version = %d, billing_log tables = %d", version, oldTables)
	}
}

func TestOpenRejectsAndPreservesAnIncompatibleLegacyLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)
	if _, errTable := database.db.Exec(`
		CREATE TABLE billing_log (id INTEGER PRIMARY KEY, at INTEGER, scope TEXT);
		INSERT INTO billing_log VALUES (1, 100, 'scope-a');
		PRAGMA user_version = 3`); errTable != nil {
		t.Fatalf("create incompatible billing_log: %v", errTable)
	}
	reopened, errOpen := Open(path)
	if errOpen == nil {
		_ = reopened.Close()
		t.Fatal("Open accepted an incompatible legacy log")
	}
	var rows, version int
	if errCount := database.db.QueryRow("SELECT count(*) FROM billing_log").Scan(&rows); errCount != nil {
		t.Fatalf("count preserved billing_log: %v", errCount)
	}
	if errVersion := database.db.QueryRow("PRAGMA user_version").Scan(&version); errVersion != nil {
		t.Fatalf("read user_version: %v", errVersion)
	}
	if rows != 1 || version != 3 {
		t.Fatalf("billing_log rows = %d, version = %d; want 1 row at version 3", rows, version)
	}
}

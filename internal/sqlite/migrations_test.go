package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func TestV12PriceMigrationPreservesModelIDsAndHistory(t *testing.T) {
	for _, modelIDs := range [][]string{
		{"model-a", "model-b"},
		{"gpt-*", "model?"},
	} {
		t.Run(modelIDs[0], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "v12.db")
			legacy, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := legacy.Exec(legacyPricingSchemaSQL + "PRAGMA user_version=12;"); err != nil {
				t.Fatal(err)
			}
			for position, modelID := range modelIDs {
				if _, err := legacy.Exec("INSERT INTO prices(position,pattern) VALUES(?,?)", position, modelID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := legacy.Exec(`INSERT INTO credentials VALUES('auth','codex','user@example.com','old format');
				INSERT INTO request_events(at,scope,auth_index,failed) VALUES(1,'old-key','auth',1),(2,'old-key','auth',0)`); err != nil {
				t.Fatal(err)
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}
			database, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			assertMigratedReferencePriceSchema(t, database)
			state := mustLoad(t, database).State
			if len(state.Prices) != len(modelIDs) {
				t.Fatalf("prices lost: %+v", state.Prices)
			}
			for _, modelID := range modelIDs {
				price := state.Prices[billing.NormalizeModelID(modelID)]
				if price.ModelID != modelID || price.InputPer1M != 0 || price.OutputPer1M != 0 {
					t.Fatalf("price changed: %+v", price)
				}
			}
			var events int
			if err := database.db.QueryRow("SELECT count(*) FROM request_events WHERE provider='codex' AND account='user@example.com'").Scan(&events); err != nil || events != 2 {
				t.Fatalf("usage history changed: events=%d err=%v", events, err)
			}
		})
	}
}

func TestV10V11PricingMigrationIsAtomicAndPreservesLegacyJSON(t *testing.T) {
	for _, version := range []int{10, 11} {
		for _, conflict := range []string{"", "reference-prices", "credentials"} {
			t.Run(fmt.Sprintf("v%d/conflict=%s", version, conflict), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "legacy.db")
				raw, err := sql.Open("sqlite3", path)
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				old := legacyPricingSchemaSQL
				old += `ALTER TABLE plans ADD COLUMN period_kind TEXT NOT NULL DEFAULT 'monthly';
INSERT INTO plans(position,id,name,amount_usd) VALUES(0,'plan','legacy plan',100);
INSERT INTO prices(position,pattern) VALUES(0,'model-a');
INSERT INTO credentials VALUES('auth','codex','user@example.com','old format');
INSERT INTO request_events(id,at,scope,auth_index,failed) VALUES(1,1,'legacy-scope','auth',1),(2,2,'legacy-scope','auth',0);
INSERT INTO request_errors(request_event_id,status_code,reason) VALUES(1,502,'preserved failure');`
				if version == 10 {
					old += `DROP TABLE routes;
ALTER TABLE api_keys DROP COLUMN route_bindings_json;
CREATE TABLE model_groups(position INTEGER,id TEXT,name TEXT);
CREATE TABLE model_group_models(position INTEGER,group_id TEXT,model TEXT);
CREATE TABLE key_model_groups(position INTEGER,scope TEXT,group_id TEXT);
CREATE TABLE key_allowed_models(position INTEGER,scope TEXT,model TEXT);
INSERT INTO api_keys(scope,plan_id,cycle_spent_usd) VALUES('legacy-scope','plan',4.5);
INSERT INTO model_groups VALUES(0,'group','Legacy group');
INSERT INTO model_group_models VALUES(0,'group','model-a');
INSERT INTO key_model_groups VALUES(0,'legacy-scope','group');
INSERT INTO key_allowed_models VALUES(0,'legacy-scope','other-model');`
				} else {
					old += `INSERT INTO api_keys(scope,plan_id,cycle_spent_usd,route_bindings_json)
VALUES('legacy-scope','plan',4.5,'[{"kind":"model","value":"model-a"}]');`
				}
				old += `UPDATE api_keys SET cycle_plan_id='plan',cycle_start_at=1,cycle_end_at=2592000000000001;`
				old += `INSERT INTO plans(position,id,name,amount_usd,period_kind) VALUES(1,'legacy-zero','Legacy',20,'never');
                    INSERT INTO api_keys(scope,plan_id,cycle_plan_id,cycle_start_at,cycle_spent_usd) VALUES('zero-active','legacy-zero','legacy-zero',123456789,2);
                    INSERT INTO api_keys(scope,plan_id) VALUES('zero-idle','legacy-zero');`
				if version == 11 {
					old += `UPDATE api_keys SET route_bindings_json='[]' WHERE scope IN ('zero-active','zero-idle');`
				}

				if conflict == "reference-prices" {
					// Fail after the earlier migration steps have transformed legacy
					// bindings and periods, proving the entire chain rolls back.
					old += `CREATE TABLE reference_prices_metadata(marker TEXT); INSERT INTO reference_prices_metadata VALUES('keep');`
				}
				if conflict == "credentials" {
					old += `ALTER TABLE credentials ADD COLUMN unknown_field TEXT;`
				}
				old += fmt.Sprintf("PRAGMA user_version=%d;", version)
				if _, err := raw.Exec(old); err != nil {
					t.Fatal(err)
				}
				d, err := Open(path)
				if conflict != "" {
					if err == nil {
						d.Close()
						t.Fatal("incompatible legacy schema accepted")
					}
					var preservedVersion int
					if err := raw.QueryRow("PRAGMA user_version").Scan(&preservedVersion); err != nil || preservedVersion != version {
						t.Fatal("version was not rolled back", preservedVersion, err)
					}
					var id, kind string
					if err := raw.QueryRow("SELECT pattern FROM prices").Scan(&id); err != nil || id != "model-a" {
						t.Fatal("legacy price schema was not restored", id, err)
					}
					if err := raw.QueryRow("SELECT period_kind FROM plans").Scan(&kind); err != nil || kind != "monthly" {
						t.Fatal("legacy period schema was not restored", kind, err)
					}
					if version == 10 {
						if err := raw.QueryRow("SELECT id FROM model_groups").Scan(&id); err != nil || id != "group" {
							t.Fatal("legacy table was lost", id, err)
						}
					} else if err := raw.QueryRow("SELECT route_bindings_json FROM api_keys").Scan(&id); err != nil || !strings.HasPrefix(id, "[") {
						t.Fatal("legacy JSON was not restored", id, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				assertMigratedReferencePriceSchema(t, d)
				var accounts int
				if err := raw.QueryRow("SELECT count(*) FROM request_events WHERE provider='codex' AND account='user@example.com'").Scan(&accounts); err != nil || accounts != 2 {
					t.Fatal("event accounts were not migrated", accounts, err)
				}
				snapshot, err := d.Load(time.Time{}, time.Time{})
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.RequestEventCount != 2 || len(snapshot.State.Prices) != 1 || snapshot.State.Prices["model-a"].ModelID != "model-a" || snapshot.State.Prices["model-a"].InputPer1M != 0 || snapshot.State.Plans[0].Windows[0].PeriodSeconds != 2592000 {
					t.Fatal("history or prices changed", snapshot)
				}
				converted, _ := snapshot.State.FindPlan("legacy-zero")
				cycle := snapshot.State.Keys["zero-active"].Cycles["default"]
				if converted.Windows[0].PeriodSeconds != 365*24*3600 || cycle.SpentUSD != 2 ||
					!cycle.EndAt.Equal(time.Unix(0, 123456789).Add(365*24*time.Hour)) || len(snapshot.State.Keys["zero-idle"].Cycles) != 0 {
					t.Fatal("zero-period migration changed consumption or started an idle window")
				}
				key := snapshot.State.Keys["legacy-scope"]
				if key == nil || key.Cycles["default"].SpentUSD != 4.5 || (len(key.RouteBindings.Models) == 0 && len(key.RouteBindings.RouteIDs) == 0) {
					t.Fatal("legacy key state was lost", key)
				}
				var reason string
				if err := raw.QueryRow("SELECT reason FROM request_errors WHERE request_event_id=1").Scan(&reason); err != nil || reason != "preserved failure" {
					t.Fatal("failed event details were lost", reason, err)
				}
			})
		}
	}
}

// Each migration must produce the same reference-price tables and index as a
// fresh database, including the absence of any derived alias table.
func assertMigratedReferencePriceSchema(t *testing.T, migrated *DB) {
	t.Helper()
	fresh := openTestDB(t)
	readSchema := func(database *DB) string {
		rows, err := database.db.Query(`
            SELECT sql FROM sqlite_master
            WHERE name LIKE 'reference_price%' AND sql IS NOT NULL ORDER BY name
        `)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var statements []string
		for rows.Next() {
			var statement string
			if err := rows.Scan(&statement); err != nil {
				t.Fatal(err)
			}
			statements = append(statements, strings.Join(strings.Fields(statement), " "))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return strings.Join(statements, "\n")
	}
	if got, want := readSchema(migrated), readSchema(fresh); got != want {
		t.Fatalf("migrated reference schema differs from fresh schema:\ngot: %s\nwant: %s", got, want)
	}
}

// Fixed v12 schema: later schema changes must not alter migration input.
const legacyPricingSchemaSQL = `
CREATE TABLE api_keys (
	scope                 TEXT    PRIMARY KEY,
	preview               TEXT    NOT NULL DEFAULT '',
	label                 TEXT    NOT NULL DEFAULT '',
	in_config             INTEGER NOT NULL DEFAULT 0,
	deleted_at            INTEGER NOT NULL DEFAULT 0,
	plan_id               TEXT    NOT NULL DEFAULT '',
	concurrency_limit      INTEGER NOT NULL DEFAULT 0,
	cycle_plan_id         TEXT    NOT NULL DEFAULT '',
	cycle_start_at        INTEGER NOT NULL DEFAULT 0,
	cycle_end_at          INTEGER NOT NULL DEFAULT 0,
	cycle_spent_usd       REAL    NOT NULL DEFAULT 0,
	route_bindings_json   TEXT    NOT NULL DEFAULT '{}'
);

CREATE TABLE routes (
	position INTEGER PRIMARY KEY,
	id       TEXT    NOT NULL UNIQUE,
	name     TEXT    NOT NULL DEFAULT '',
	rule_json TEXT   NOT NULL DEFAULT '{}'
);

CREATE TABLE plans (
	position       INTEGER PRIMARY KEY,
	id             TEXT    NOT NULL UNIQUE,
	name           TEXT    NOT NULL DEFAULT '',
	amount_usd     REAL    NOT NULL DEFAULT 0,
	period_seconds INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE prices (
	position                        INTEGER PRIMARY KEY,
	pattern                         TEXT    NOT NULL,
	input_per_1m                    REAL    NOT NULL DEFAULT 0,
	output_per_1m                   REAL    NOT NULL DEFAULT 0,
	cache_read_per_1m               REAL,
	cache_write_per_1m              REAL,
	long_context_threshold          INTEGER,
	long_context_input_per_1m       REAL,
	long_context_output_per_1m      REAL,
	long_context_cache_read_per_1m  REAL,
	long_context_cache_write_per_1m REAL
);

CREATE TABLE credentials (
	auth_index TEXT PRIMARY KEY,
	provider   TEXT NOT NULL DEFAULT '',
	account    TEXT NOT NULL DEFAULT '',
	name       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE request_events (
	id                          INTEGER PRIMARY KEY AUTOINCREMENT,
	at                          INTEGER NOT NULL,
	scope                       TEXT    NOT NULL,
	auth_index                  TEXT    NOT NULL DEFAULT '',
	provider                    TEXT    NOT NULL DEFAULT '',
	executor_type               TEXT    NOT NULL DEFAULT '',
	reasoning_effort            TEXT    NOT NULL DEFAULT '',
	service_tier                TEXT    NOT NULL DEFAULT '',
	upstream_model              TEXT    NOT NULL DEFAULT '',
	billing_model               TEXT    NOT NULL DEFAULT '',
	failed                      INTEGER NOT NULL DEFAULT 0,
	latency_ms                  INTEGER NOT NULL DEFAULT 0,
	ttft_ms                     INTEGER NOT NULL DEFAULT 0,
	accounting_quality          TEXT    NOT NULL DEFAULT '',
	price_source                TEXT    NOT NULL DEFAULT '',
	reasoning_tokens            INTEGER NOT NULL DEFAULT 0,
	total_usd                   REAL    NOT NULL DEFAULT 0,
	uncached_input_usd          REAL    NOT NULL DEFAULT 0,
	cache_read_usd              REAL    NOT NULL DEFAULT 0,
	cache_write_usd             REAL    NOT NULL DEFAULT 0,
	output_usd                  REAL    NOT NULL DEFAULT 0,
	uncached_input_tokens       INTEGER NOT NULL DEFAULT 0,
	cache_read_tokens           INTEGER NOT NULL DEFAULT 0,
	cache_write_tokens          INTEGER NOT NULL DEFAULT 0,
	billed_output_tokens        INTEGER NOT NULL DEFAULT 0,
	tiered                      INTEGER NOT NULL DEFAULT 0,
	long_context                INTEGER NOT NULL DEFAULT 0,
	threshold_input_tokens      INTEGER NOT NULL DEFAULT 0,
	applied_input_per_1m        REAL    NOT NULL DEFAULT 0,
	applied_output_per_1m       REAL    NOT NULL DEFAULT 0,
	applied_cache_read_per_1m   REAL    NOT NULL DEFAULT 0,
	applied_cache_write_per_1m  REAL    NOT NULL DEFAULT 0
);

CREATE INDEX request_events_at ON request_events(at);
CREATE INDEX request_events_scope_at ON request_events(scope, at);
CREATE INDEX request_events_model_at ON request_events(billing_model, at);
CREATE INDEX request_events_auth_at ON request_events(auth_index, at);

CREATE TABLE request_errors (
	request_event_id INTEGER PRIMARY KEY REFERENCES request_events(id) ON DELETE CASCADE,
	status_code      INTEGER NOT NULL DEFAULT 0,
	error_type       TEXT    NOT NULL DEFAULT '',
	reason           TEXT    NOT NULL DEFAULT '',
	body             TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX request_errors_status ON request_errors(status_code);
CREATE INDEX request_errors_type ON request_errors(error_type);

CREATE TABLE plugin_logs (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	at      INTEGER NOT NULL,
	level   TEXT    NOT NULL DEFAULT '',
	message TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX plugin_logs_at ON plugin_logs(at);
`

func TestV13CredentialMigrationAndRollback(t *testing.T) {
	for _, scenario := range []struct{ name, sql string }{
		{"migrate", ""},
		{"incompatible-table", "ALTER TABLE credentials ADD COLUMN unknown_field TEXT"},
		{"update-failure", "CREATE TRIGGER reject_update BEFORE UPDATE ON request_events BEGIN SELECT RAISE(ABORT, 'dummy failure'); END"},
		{"config-table-conflict", "CREATE TABLE config_credentials(marker TEXT); INSERT INTO config_credentials VALUES('keep')"},
		{"index-name-conflict", "DROP INDEX request_events_at; CREATE TABLE request_events_at(marker TEXT)"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "v13.db")
			raw, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.Exec(legacyPricingSchemaSQL); err != nil {
				t.Fatal(err)
			}
			fixture := &DB{db: raw}
			if err := fixture.transact(func(tx *sql.Tx) error {
				if err := migrateModelPricing(tx); err != nil {
					return err
				}
				return migrateQuotaWindows(tx)
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`PRAGMA user_version=13;
				INSERT INTO credentials VALUES('auth','codex','user@example.com','old format');
				INSERT INTO request_events(id,at,scope,auth_index,provider,failed,total_usd) VALUES
					(1,1,'s','auth','',0,2.5),(2,2,'s','auth','historical-provider',1,0),(3,3,'s','','',0,0);
				INSERT INTO request_errors(request_event_id,status_code,body) VALUES(2,502,'preserved failure');
			` + scenario.sql); err != nil {
				t.Fatal(err)
			}
			database, err := Open(path)
			if scenario.sql != "" {
				if err == nil {
					database.Close()
					t.Fatal("incompatible migration succeeded")
				}
				var version, columns, events int
				var provider, account string
				if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 13 {
					t.Fatal("schema version was not rolled back", version, err)
				}
				if err := raw.QueryRow("SELECT count(*) FROM pragma_table_info('request_events') WHERE name='account'").Scan(&columns); err != nil || columns != 0 {
					t.Fatal("account column was not rolled back", columns, err)
				}
				if err := raw.QueryRow("SELECT provider FROM request_events WHERE id=1").Scan(&provider); err != nil || provider != "" {
					t.Fatal("event provider was not rolled back", provider, err)
				}
				if err := raw.QueryRow("SELECT account FROM credentials WHERE auth_index='auth'").Scan(&account); err != nil || account != "user@example.com" {
					t.Fatal("old credential was lost", account, err)
				}
				if err := raw.QueryRow("SELECT count(*) FROM request_events").Scan(&events); err != nil || events != 3 {
					t.Fatal("history was lost", events, err)
				}
				if scenario.name == "config-table-conflict" {
					var marker string
					if err := raw.QueryRow("SELECT marker FROM config_credentials").Scan(&marker); err != nil || marker != "keep" {
						t.Fatal("incompatible config table was lost", marker, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			database = openDatabase(t, path)
			var version, credentials int
			if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
				t.Fatal("incorrect schema version", version, err)
			}
			if err := raw.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='credentials'").Scan(&credentials); err != nil || credentials != 0 {
				t.Fatal("old credentials table remains", credentials, err)
			}
			if err := raw.QueryRow("SELECT count(*) FROM config_credentials").Scan(&credentials); err != nil || credentials != 0 {
				t.Fatal("config snapshot must start empty", credentials, err)
			}
			view, err := database.RequestEvents(billing.RequestEventQuery{}, time.Time{})
			if err != nil || len(view.Entries) != 3 || view.Total != 3 || view.Statuses.Failed != 1 {
				t.Fatalf("migrated events = %+v, err = %v", view, err)
			}
			for i, want := range []struct{ provider, account string }{
				{"", ""}, {"historical-provider", "user@example.com"}, {"codex", "user@example.com"},
			} {
				if row := view.Entries[i]; row.ID != int64(3-i) || row.Provider != want.provider || row.Account != want.account {
					t.Fatalf("migrated event = %+v", row)
				}
			}
			if view.Entries[2].Cost.TotalUSD != 2.5 || view.Entries[1].Cost.TotalUSD != 0 || view.Entries[0].Cost.TotalUSD != 0 {
				t.Fatal("historical costs changed")
			}
			errors, err := database.RequestErrors(billing.RequestErrorQuery{}, time.Time{})
			if err != nil || len(errors.Entries) != 1 || errors.Entries[0].ID != 2 ||
				errors.Entries[0].StatusCode != 502 || errors.Entries[0].Body != "preserved failure" {
				t.Fatalf("migrated errors = %+v, err = %v", errors, err)
			}
		})
	}
}

func TestV12QuotaWindowsMigrationAndRollback(t *testing.T) {
	for _, scenario := range []string{"", "zero period", "missing cycle time", "mixed format"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "old.db")
			raw, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.Exec(legacyPricingSchemaSQL); err != nil {
				t.Fatal(err)
			}
			start := time.Date(2026, 8, 1, 2, 3, 4, 567890123, time.UTC)
			if _, err := raw.Exec(`INSERT INTO plans(position,id,name,amount_usd,period_seconds) VALUES(0,'p','Team',5,3600);
                    INSERT INTO request_events(at,scope,failed) VALUES(1,'s',1),(2,'s',0);`); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`INSERT INTO api_keys(scope,preview,deleted_at,plan_id,cycle_plan_id,cycle_start_at,cycle_end_at,cycle_spent_usd)
                    VALUES('s','unknown',1,'p','p',?,?,7.5)`, start.UnixNano(), start.Add(time.Hour).UnixNano()); err != nil {
				t.Fatal(err)
			}
			if scenario == "mixed format" {
				_, err = raw.Exec("ALTER TABLE plans ADD COLUMN windows_json TEXT")
			}
			if scenario == "zero period" {
				_, err = raw.Exec("UPDATE plans SET period_seconds=0; UPDATE api_keys SET cycle_end_at=0;")
			}
			if scenario == "missing cycle time" {
				_, err = raw.Exec("UPDATE api_keys SET cycle_start_at=0")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec("PRAGMA user_version=12"); err != nil {
				t.Fatal(err)
			}
			database, err := Open(path)
			if scenario == "missing cycle time" || scenario == "mixed format" {
				if err == nil {
					database.Close()
					t.Fatal("corrupt legacy data accepted")
				}
				var spent float64
				var currentVersion int
				if err := raw.QueryRow("SELECT cycle_spent_usd FROM api_keys").Scan(&spent); err != nil || spent != 7.5 {
					t.Fatal("rollback lost data", err)
				}
				if err := raw.QueryRow("PRAGMA user_version").Scan(&currentVersion); err != nil || currentVersion != 12 {
					t.Fatal("version was not rolled back", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			snapshot := mustLoad(t, database)
			cycle := snapshot.State.Keys["s"].Cycles["default"]
			expectedPeriod := int64(3600)
			if scenario == "zero period" {
				expectedPeriod = 365 * 24 * 3600
			}
			if snapshot.State.Plans[0].Windows[0].PeriodSeconds != expectedPeriod {
				t.Fatal("incorrect migrated period")
			}
			if !cycle.StartAt.Equal(start) || !cycle.EndAt.Equal(start.Add(time.Duration(expectedPeriod)*time.Second)) || cycle.SpentUSD != 7.5 || snapshot.RequestEventCount != 2 || snapshot.State.Keys["s"].DeletedAt.IsZero() {
				t.Fatalf("migration lost state: %+v", snapshot)
			}
			database.Close()
			reopened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			reopened.Close()
		})
	}
}

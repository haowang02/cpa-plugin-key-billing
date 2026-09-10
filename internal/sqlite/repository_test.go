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
	start := time.Date(2026, 8, 12, 9, 30, 0, 123456789, time.UTC)
	state := billing.NewState()
	state.Plans = []billing.Plan{{ID: "weekly", Name: "Weekly 10", Windows: []billing.QuotaWindow{{ID: "default", Name: "额度", AmountUSD: 10, PeriodSeconds: 604800}, {ID: "long", Name: "预算", AmountUSD: 50, PeriodSeconds: 1209600}}}}
	state.Prices = map[string]billing.CustomPrice{"gpt-5.5": {ModelID: "gpt-5.5", PriceRates: billing.PriceRates{InputPer1M: 1, OutputPer1M: 2, CacheReadPer1M: price(.1)}}}
	state.Routes = []billing.Route{{ID: "fast", Name: "Fast", Rule: billing.RouteRule{Models: []string{"gpt-5.5"}, CredentialIDs: []string{}, CredentialProviders: []billing.CredentialProviderSelector{}}}}
	state.ConfigCredentials[billing.CredentialFingerprint("dummy-config")] = billing.ConfigCredential{
		Provider: "codex", KeyPreview: "sk-tes…0001", Disabled: true,
	}
	state.Keys["scope-a"] = &billing.KeyState{Preview: "sk-tes…0001", Label: "Alice", InConfig: true,
		PlanID: "weekly", ConcurrencyLimit: 7, RouteBindings: billing.RouteBindings{
			RouteIDs: []string{"fast"}, Models: []string{"other"}, CredentialIDs: []string{},
			CredentialProviders: []billing.CredentialProviderSelector{},
		},
		Cycles: map[string]billing.QuotaCycle{"default": {PlanID: "weekly", StartAt: start, EndAt: start.Add(7 * 24 * time.Hour), SpentUSD: 1.5}, "long": {PlanID: "weekly", StartAt: start, EndAt: start.Add(14 * 24 * time.Hour), SpentUSD: 9}}}

	database := openDatabase(t, path)
	for _, price := range state.Prices {
		if err := database.UpsertPrice(price); err != nil {
			t.Fatal(err)
		}
	}
	mustSave(t, database, state, billing.Changes{AllKeys: true, Plans: true, Routes: true, ConfigCredentials: true,
		RequestErrorEvents: []billing.RequestErrorEvent{{Event: billing.RequestEvent{At: start, Scope: "scope-a", AuthIndex: "auth-1", Provider: "codex", Account: "ops@example.com", BillingModel: "gpt-5.5"},
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
	if err != nil || len(errors.Entries) != 1 || errors.Entries[0].StatusCode != 429 || errors.Entries[0].Source != "codex · ops@example.com" {
		t.Fatalf("request errors = %+v, err = %v", errors.Entries, err)
	}
}

func TestSaveWritesOnlyNamedKeys(t *testing.T) {
	database := openTestDB(t)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Preview: "sk-tes…0001", Label: "A"}
	state.Keys["scope-b"] = &billing.KeyState{Preview: "sk-tes…0002", Label: "B"}
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
	state.Routes = []billing.Route{{ID: "fast", Name: "Fast", Rule: billing.RouteRule{Models: []string{"gpt-5.5"}, CredentialIDs: []string{}, CredentialProviders: []billing.CredentialProviderSelector{}}}}
	state.Keys["scope-a"] = &billing.KeyState{Preview: "sk-tes…0001", RouteBindings: billing.RouteBindings{RouteIDs: []string{"fast"}, Models: []string{"claude"}}}
	state.Keys["scope-b"] = &billing.KeyState{Preview: "sk-tes…0002", RouteBindings: billing.RouteBindings{RouteIDs: []string{"fast"}}}
	mustSave(t, database, state, billing.Changes{AllKeys: true, Routes: true})
	state.Keys["scope-a"].RouteBindings = billing.RouteBindings{Models: []string{"other"}}
	mustSave(t, database, state, billing.Changes{Keys: []string{"scope-a"}})
	stored := mustLoad(t, database).State
	if !reflect.DeepEqual(stored.Keys["scope-a"].RouteBindings.Models, []string{"other"}) || len(stored.Keys["scope-b"].RouteBindings.RouteIDs) != 1 {
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
		"api_keys": true, "routes": true, "plans": true,
		"prices": true, "request_events": true, "config_credentials": true,
		"request_errors": true, "plugin_logs": true, "reference_prices_metadata": true, "reference_prices": true,
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
			} else if !strings.Contains(err.Error(), "文件格式不受支持") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestQuotaDimensionsExtendExistingJSON(t *testing.T) {
	const tokenLimit = int64(1<<53 - 1)
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)
	// Persist the original amount-only JSON, with no fields for new dimensions.
	if _, err := database.db.Exec(`INSERT INTO plans (position, id, name, windows_json)
		VALUES (0, 'p', '团队', '[{"id":"w","name":"额度","period_seconds":3600,"amount_usd":10}]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(insertKey, "dummy-scope", "sk-dum…0001", "", true, 0, "p", 0,
		`{"w":{"plan_id":"p","start_at":"2026-09-08T12:00:00Z","end_at":"2026-09-08T13:00:00Z","spent_usd":3.5}}`, `{}`, 0); err != nil {
		t.Fatal(err)
	}
	state := mustLoad(t, database).State
	cycle := state.Keys["dummy-scope"].Cycles["w"]
	start := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	if cycle.SpentUSD != 3.5 || !cycle.StartAt.Equal(start) || cycle.UsedTokens != 0 || cycle.UsedRequests != 0 {
		t.Fatalf("legacy cycle changed: %+v", cycle)
	}
	state.Plans[0].Windows[0].TokenLimit = tokenLimit
	state.Plans[0].Windows[0].RequestLimit = 100
	cycle.UsedTokens, cycle.UsedRequests = tokenLimit, 7
	state.Keys["dummy-scope"].Cycles["w"] = cycle
	state.Plans[0].Windows = append(state.Plans[0].Windows,
		billing.QuotaWindow{ID: "tokens", Name: "Token", PeriodSeconds: 7200, TokenLimit: 1000},
		billing.QuotaWindow{ID: "requests", Name: "请求", PeriodSeconds: 86400, RequestLimit: 100})
	mustSave(t, database, state, billing.Changes{Plans: true, AllKeys: true})
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openDatabase(t, path)
	if loaded := mustLoad(t, reopened).State; !reflect.DeepEqual(loaded, state) {
		t.Fatalf("quota round trip lost data: got %+v, want %+v", loaded, state)
	}
}

func BenchmarkHistoryQueries(b *testing.B) {
	d, err := Open(filepath.Join(b.TempDir(), "history.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = d.Close() })
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(30 * 24 * time.Hour)
	_, err = d.db.Exec(`WITH RECURSIVE seq(n) AS (
		VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n < 200000
	) INSERT INTO request_events (at, scope, billing_model, provider, account,
		executor_type, failed, uncached_input_tokens, billed_output_tokens, total_usd)
	SELECT ? + ((n * 7919) % 2592000) * 1000000000, 'scope-' || (n % 100),
		'model-' || (n % 8), 'provider-' || (n % 3), 'dummy@example.com',
		'DummyExecutor', n % 5 = 0, 100, 50, 0.125 FROM seq;
	INSERT INTO request_errors SELECT id, 429, 'rate_limit_error', 'dummy failure',
		printf('%02048d', 0) FROM request_events WHERE failed != 0;
	INSERT INTO plugin_logs (at, level, message)
		SELECT at, CASE WHEN id % 5 = 0 THEN 'error' ELSE 'info' END,
		printf('%01024d', 0) FROM request_events;`, nanos(from))
	if err != nil {
		b.Fatal(err)
	}
	for _, bench := range []struct {
		name string
		run  func() error
	}{
		{"keys", func() error { _, err := d.EventKeys(from, to, from); return err }},
		{"events", func() error {
			_, err := d.RequestEvents(billing.RequestEventQuery{From: from, To: to, Limit: 50}, from)
			return err
		}},
		{"event_filters", func() error {
			_, err := d.requestEventFilterValues(billing.RequestEventQuery{From: from, To: to}, from)
			return err
		}},
		{"errors", func() error {
			_, err := d.RequestErrors(billing.RequestErrorQuery{From: from, To: to, Limit: 50, IncludeFilters: true}, from)
			return err
		}},
		{"logs", func() error {
			_, err := d.PluginLogsPage(billing.PluginLogQuery{Since: from, Limit: 50})
			return err
		}},
		{"analysis", func() error {
			_, err := d.Analysis(billing.RequestEventQuery{From: from, To: to}, from)
			return err
		}},
	} {
		b.Run(bench.name, func(b *testing.B) {
			for b.Loop() {
				if err := bench.run(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

func migrateSchema(tx *sql.Tx, version int, storedHeaders StoredQuotaHeaders) error {
	var steps []func(*sql.Tx) error
	switch version {
	case 10:
		steps = append(steps, migrateModelGroups, migrateSubscriptionPeriods)
	case 11:
		steps = append(steps, migrateLegacyRouteBindings, migrateSubscriptionPeriods)
	}
	if version <= 12 {
		steps = append(steps, migrateModelPricing, migrateQuotaWindows)
	}
	if version <= 13 {
		steps = append(steps, migrateCredentials)
	}
	if version <= 14 {
		steps = append(steps, migrateResponseHeaders)
	}
	if version <= 15 {
		steps = append(steps, migrateRequestErrorReason)
	}
	if version <= 16 {
		steps = append(steps, migrateUpstreamResponseReports)
	}
	if version <= 17 {
		steps = append(steps, migrateRequestInfo)
	}
	if version <= 18 {
		steps = append(steps, migrateExecutionMetadata)
	}
	if version <= 19 {
		steps = append(steps, func(tx *sql.Tx) error { return migrateQuotaReports(tx, storedHeaders) })
	}
	if version <= 20 {
		steps = append(steps, migrateClientHeaders)
	}
	if version <= 21 {
		steps = append(steps, migrateConfiguredProviders)
	}
	if version <= 22 {
		steps = append(steps, migrateCredentialNotes)
	}
	steps = append(steps, migrateCurrencies)
	for _, step := range steps {
		if err := step(tx); err != nil {
			return err
		}
	}
	return nil
}

// Schema 24 denominates prices, plans and request costs in a currency. Every
// earlier amount was in USD.
func migrateCurrencies(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		ALTER TABLE prices ADD COLUMN currency TEXT NOT NULL DEFAULT 'USD';
		ALTER TABLE plans ADD COLUMN currency TEXT NOT NULL DEFAULT 'USD';
		ALTER TABLE request_events ADD COLUMN currency TEXT NOT NULL DEFAULT 'USD';
		ALTER TABLE request_events RENAME COLUMN total_usd TO total_cost;
		ALTER TABLE request_events RENAME COLUMN uncached_input_usd TO uncached_input_cost;
		ALTER TABLE request_events RENAME COLUMN cache_read_usd TO cache_read_cost;
		ALTER TABLE request_events RENAME COLUMN cache_write_usd TO cache_write_cost;
		ALTER TABLE request_events RENAME COLUMN output_usd TO output_cost;`); err != nil {
		return fmt.Errorf("Add currencies: %w", err)
	}
	rows, err := tx.Query("SELECT id, windows_json FROM plans")
	if err != nil {
		return fmt.Errorf("Read subscription plans: %w", err)
	}
	windows := make(map[string]string)
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return fmt.Errorf("Read subscription plans: %w", err)
		}
		var fields []map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			rows.Close()
			return fmt.Errorf("Read subscription plan %s: %w", id, err)
		}
		// Windows converted from schema 12 in this run already name their amount.
		changed := false
		for _, window := range fields {
			if amount, ok := window["amount_usd"]; ok {
				window["amount"], changed = amount, true
				delete(window, "amount_usd")
			}
		}
		if changed {
			converted, _ := json.Marshal(fields)
			windows[id] = string(converted)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("Read subscription plans: %w", err)
	}
	for id, raw := range windows {
		if _, err := tx.Exec("UPDATE plans SET windows_json=? WHERE id=?", raw, id); err != nil {
			return fmt.Errorf("Migrate subscription plan %s: %w", id, err)
		}
	}
	return nil
}

func migrateCredentialNotes(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		ALTER TABLE config_credentials ADD COLUMN config_ref TEXT NOT NULL DEFAULT '';
		ALTER TABLE config_credentials ADD COLUMN note TEXT NOT NULL DEFAULT '';
		UPDATE config_credentials SET config_ref = ref;`); err != nil {
		return fmt.Errorf("Add configured credential notes: %w", err)
	}
	return nil
}

func migrateConfiguredProviders(tx *sql.Tx) error {
	_, err := tx.Exec(`
		ALTER TABLE config_credentials ADD COLUMN auth_index TEXT NOT NULL DEFAULT '';
		ALTER TABLE config_credentials ADD COLUMN type TEXT NOT NULL DEFAULT '';
		ALTER TABLE config_credentials ADD COLUMN name TEXT NOT NULL DEFAULT '';
		ALTER TABLE config_credentials ADD COLUMN base_url TEXT NOT NULL DEFAULT '';
		ALTER TABLE config_credentials ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE config_credentials ADD COLUMN weight INTEGER NOT NULL DEFAULT 1;
		ALTER TABLE config_credentials ADD COLUMN balance_json TEXT NOT NULL DEFAULT 'null';`)
	if err != nil {
		return fmt.Errorf("Add configured provider snapshots: %w", err)
	}
	return nil
}

// Schema 21 records the client-reported headers of new requests; historical
// rows stay unknown.
func migrateClientHeaders(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		ALTER TABLE request_events ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';
		ALTER TABLE request_events ADD COLUMN x_forwarded_for TEXT NOT NULL DEFAULT '';`); err != nil {
		return fmt.Errorf("Add request event client headers: %w", err)
	}
	return nil
}

// Schema 20 moves the quota headers stored with each request into the reports
// they make, which are read without decoding headers. Headers that report
// nothing under the current rules leave no report.
func migrateQuotaReports(tx *sql.Tx, storedHeaders StoredQuotaHeaders) error {
	if _, err := tx.Exec(quotaReportTables); err != nil {
		return fmt.Errorf("Create quota report tables: %w", err)
	}
	rows, err := tx.Query(`SELECT id, auth_index, provider, at, response_headers_json FROM request_events
		WHERE response_headers_json != '{}' ORDER BY id`)
	if err != nil {
		return fmt.Errorf("Read stored quota headers: %w", err)
	}
	type stored struct {
		id        int64
		authIndex string
		at        time.Time
		report    *billing.QuotaReport
	}
	var reports []stored
	for rows.Next() {
		if storedHeaders == nil {
			rows.Close()
			return fmt.Errorf("Converting stored quota headers needs a header reader")
		}
		var row stored
		var provider, raw string
		var at int64
		if err := rows.Scan(&row.id, &row.authIndex, &provider, &at, &raw); err != nil {
			rows.Close()
			return fmt.Errorf("Read stored quota headers: %w", err)
		}
		var values map[string]any
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			rows.Close()
			return fmt.Errorf("Request event %d has invalid stored quota headers: %w", row.id, err)
		}
		// Headers are stored as value lists; read plain text values the same way.
		headers := make(http.Header, len(values))
		for key, value := range values {
			if list, ok := value.([]any); ok && len(list) > 0 {
				value = list[0]
			}
			if text, ok := value.(string); ok {
				headers[key] = []string{text}
			}
		}
		row.at = timeAt(at)
		if row.report = storedHeaders(provider, headers, row.at); row.report != nil {
			reports = append(reports, row)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("Read stored quota headers: %w", err)
	}
	for _, row := range reports {
		if err := insertQuotaReport(tx, row.id, row.authIndex, row.at, *row.report); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("ALTER TABLE request_events DROP COLUMN response_headers_json"); err != nil {
		return fmt.Errorf("Drop stored quota headers: %w", err)
	}
	return nil
}

// Schema 19 adds only metadata reported by usage.handle; historical rows stay unknown.
func migrateExecutionMetadata(tx *sql.Tx) error {
	_, err := tx.Exec("ALTER TABLE request_events ADD COLUMN execution_json TEXT NOT NULL DEFAULT 'null'")
	if err != nil {
		return fmt.Errorf("Add execution metadata: %w", err)
	}
	return nil
}

// Historical events have no client origin or reported streaming mode.
func migrateRequestInfo(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		ALTER TABLE request_events ADD COLUMN endpoint TEXT NOT NULL DEFAULT '';
		ALTER TABLE request_events ADD COLUMN transport TEXT NOT NULL DEFAULT '';
		ALTER TABLE request_events ADD COLUMN stream INTEGER;`); err != nil {
		return fmt.Errorf("Add request event client metadata: %w", err)
	}
	return nil
}

// Rows recorded before schema 17 carry no upstream response report.
func migrateUpstreamResponseReports(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		ALTER TABLE request_events ADD COLUMN response_service_tier TEXT NOT NULL DEFAULT '';
		ALTER TABLE request_events ADD COLUMN response_model TEXT NOT NULL DEFAULT '';`); err != nil {
		return fmt.Errorf("Add request event upstream response reports: %w", err)
	}
	return nil
}

func migrateRequestErrorReason(tx *sql.Tx) error {
	if _, err := tx.Exec("ALTER TABLE request_errors DROP COLUMN reason"); err != nil {
		return fmt.Errorf("Drop request error reason: %w", err)
	}
	return nil
}

func migrateResponseHeaders(tx *sql.Tx) error {
	if _, err := tx.Exec(
		"ALTER TABLE request_events ADD COLUMN response_headers_json TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return fmt.Errorf("Add request event response headers: %w", err)
	}
	return nil
}

func migrateCredentials(tx *sql.Tx) error {
	// Do not discard unknown fields or ambiguous identities when dropping the table.
	var compatible, invalid bool
	if err := tx.QueryRow(`SELECT
		EXISTS(SELECT 1 FROM sqlite_master WHERE name = 'credentials' AND type = 'table')
		AND count(*) = 4 AND sum(upper(type) = 'TEXT' AND hidden = 0 AND (
			(name = 'auth_index' AND pk = 1) OR
			(name IN ('provider', 'account', 'name') AND pk = 0)
		)) = 4 FROM pragma_table_xinfo('credentials')`).Scan(&compatible); err != nil {
		return fmt.Errorf("Read legacy credential table schema: %w", err)
	}
	if !compatible {
		return fmt.Errorf("The legacy credential table schema is incompatible")
	}
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM credentials
		WHERE typeof(auth_index) != 'text' OR typeof(provider) != 'text'
		OR typeof(account) != 'text' OR typeof(name) != 'text')`).Scan(&invalid); err != nil {
		return fmt.Errorf("Migrate request event accounts: %w", err)
	}
	if invalid {
		return fmt.Errorf("The legacy credential table contains incompatible data")
	}
	_, err := tx.Exec(`
		ALTER TABLE request_events ADD COLUMN account TEXT NOT NULL DEFAULT '';
		UPDATE request_events AS r SET account = c.account,
			provider = coalesce(NULLIF(r.provider, ''), c.provider)
		FROM credentials c WHERE c.auth_index = r.auth_index;
		DROP TABLE credentials;
		CREATE TABLE config_credentials (
			ref         TEXT PRIMARY KEY,
			provider    TEXT NOT NULL,
			key_preview TEXT NOT NULL DEFAULT '',
			disabled    INTEGER NOT NULL DEFAULT 0
		);
	`)
	if err != nil {
		return fmt.Errorf("Migrate credential data: %w", err)
	}
	return nil
}

func migrateModelPricing(tx *sql.Tx) error {
	_, err := tx.Exec(`
        ALTER TABLE prices RENAME COLUMN pattern TO model_id;
        CREATE UNIQUE INDEX prices_model_id ON prices(model_id COLLATE NOCASE);

        CREATE TABLE reference_prices_metadata (
            id                   INTEGER PRIMARY KEY CHECK (id = 1),
            source_url           TEXT    NOT NULL,
            content_hash         TEXT    NOT NULL DEFAULT '',
            version              INTEGER NOT NULL DEFAULT 0,
            fetched_at           INTEGER NOT NULL DEFAULT 0,
            model_count          INTEGER NOT NULL DEFAULT 0,
            last_attempt_at      INTEGER NOT NULL DEFAULT 0,
            retry_after          INTEGER NOT NULL DEFAULT 0,
            last_error           TEXT    NOT NULL DEFAULT '',
            consecutive_failures INTEGER NOT NULL DEFAULT 0
        );

        CREATE TABLE reference_prices (
            provider_id  TEXT    NOT NULL,
            model_id     TEXT    NOT NULL,
            match_key    TEXT    NOT NULL,
            is_canonical INTEGER NOT NULL,
            rates_json   TEXT,
            PRIMARY KEY (provider_id, model_id)
        );
    `)
	return err
}

// migrateModelGroups converts the v10 model-access tables into route bindings.
func migrateModelGroups(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TABLE routes (
			position INTEGER PRIMARY KEY,
			id TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL DEFAULT '',
			rule_json TEXT NOT NULL DEFAULT '{}'
		)`); err != nil {
		return fmt.Errorf("Create routing rules table: %w", err)
	}
	if _, err := tx.Exec("ALTER TABLE api_keys ADD COLUMN route_bindings_json TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return fmt.Errorf("Add API key routing bindings: %w", err)
	}
	type legacyGroup struct {
		oldID, id, name string
		models          []string
	}
	groups := []legacyGroup{}
	usedRouteIDs := map[string]struct{}{}
	rows, err := tx.Query("SELECT id,name FROM model_groups ORDER BY position")
	if err != nil {
		return fmt.Errorf("Read legacy model groups: %w", err)
	}
	for rows.Next() {
		var group legacyGroup
		if err := rows.Scan(&group.oldID, &group.name); err != nil {
			rows.Close()
			return fmt.Errorf("Read legacy model groups: %w", err)
		}
		group.id = strings.TrimSpace(group.oldID)
		if _, exists := usedRouteIDs[group.id]; exists {
			return fmt.Errorf("Legacy model group ID %q conflicts after migration", group.oldID)
		}
		usedRouteIDs[group.id] = struct{}{}
		groups = append(groups, group)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("Read legacy model groups: %w", err)
	}
	byOld := make(map[string]int, len(groups))
	for i := range groups {
		byOld[groups[i].oldID] = i
	}
	members, err := tx.Query("SELECT group_id,model FROM model_group_models ORDER BY group_id,position")
	if err != nil {
		return fmt.Errorf("Read legacy model group members: %w", err)
	}
	for members.Next() {
		var id, model string
		if err := members.Scan(&id, &model); err != nil {
			members.Close()
			return fmt.Errorf("Read legacy model group members: %w", err)
		}
		if i, ok := byOld[id]; ok {
			groups[i].models = append(groups[i].models, model)
		}
	}
	if err := members.Close(); err != nil {
		return err
	}
	if err := members.Err(); err != nil {
		return fmt.Errorf("Read legacy model group members: %w", err)
	}
	for i, group := range groups {
		route, err := billing.NormalizeRoute(billing.Route{ID: group.id, Name: group.name, Rule: billing.RouteRule{Models: group.models}})
		if err != nil {
			return fmt.Errorf("Validate legacy model group %s: %w", group.oldID, err)
		}
		groups[i].id = route.ID
		groups[i].models = route.Rule.Models
		raw, _ := json.Marshal(route.Rule)
		if _, err = tx.Exec("INSERT INTO routes(position,id,name,rule_json) VALUES(?,?,?,?)", i, route.ID, route.Name, string(raw)); err != nil {
			return fmt.Errorf("Migrate model group %s: %w", group.oldID, err)
		}
	}

	bindingsByScope := make(map[string]billing.RouteBindings)
	groupRows, err := tx.Query("SELECT scope,group_id FROM key_model_groups ORDER BY scope,position")
	if err != nil {
		return fmt.Errorf("Read legacy model group bindings: %w", err)
	}
	for groupRows.Next() {
		var scope, id string
		if err := groupRows.Scan(&scope, &id); err != nil {
			groupRows.Close()
			return fmt.Errorf("Read legacy model group bindings: %w", err)
		}
		if i, ok := byOld[id]; ok && len(groups[i].models) > 0 {
			bindings := bindingsByScope[scope]
			bindings.RouteIDs = append(bindings.RouteIDs, groups[i].id)
			bindingsByScope[scope] = bindings
		}
	}
	if err := groupRows.Close(); err != nil {
		return err
	}
	if err := groupRows.Err(); err != nil {
		return fmt.Errorf("Read legacy model group bindings: %w", err)
	}

	modelRows, err := tx.Query("SELECT scope,model FROM key_allowed_models ORDER BY scope,position")
	if err != nil {
		return fmt.Errorf("Read legacy model bindings: %w", err)
	}
	for modelRows.Next() {
		var scope, model string
		if err := modelRows.Scan(&scope, &model); err != nil {
			modelRows.Close()
			return fmt.Errorf("Read legacy model bindings: %w", err)
		}
		bindings := bindingsByScope[scope]
		bindings.Models = append(bindings.Models, model)
		bindingsByScope[scope] = bindings
	}
	if err := modelRows.Close(); err != nil {
		return err
	}
	if err := modelRows.Err(); err != nil {
		return fmt.Errorf("Read legacy model bindings: %w", err)
	}

	for scope, bindings := range bindingsByScope {
		bindings, err = billing.NormalizeRouteBindings(bindings)
		if err != nil {
			return fmt.Errorf("Validate migrated bindings for API key %s: %w", scope, err)
		}
		raw, _ := json.Marshal(bindings)
		if _, err = tx.Exec("UPDATE api_keys SET route_bindings_json=? WHERE scope=?", string(raw), scope); err != nil {
			return fmt.Errorf("Migrate API key %s: %w", scope, err)
		}
	}
	for _, table := range []string{"key_model_groups", "key_allowed_models", "model_group_models", "model_groups"} {
		if _, err := tx.Exec("DROP TABLE " + table); err != nil {
			return fmt.Errorf("Drop legacy table %s: %w", table, err)
		}
	}
	return nil
}

func migrateLegacyRouteBindings(tx *sql.Tx) error {
	updates := map[string]string{}
	rows, err := tx.Query("SELECT scope,route_bindings_json FROM api_keys")
	if err != nil {
		return fmt.Errorf("Read legacy API key routing bindings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var scope, raw string
		if err = rows.Scan(&scope, &raw); err != nil {
			return fmt.Errorf("Read legacy API key routing bindings: %w", err)
		}
		var legacy []struct {
			Kind  string `json:"kind"`
			Value string `json:"value"`
		}
		if err = json.Unmarshal([]byte(raw), &legacy); err != nil {
			return fmt.Errorf("Read legacy routing bindings for API key %s: %w", scope, err)
		}
		var bindings billing.RouteBindings
		for _, binding := range legacy {
			switch binding.Kind {
			case "route":
				bindings.RouteIDs = append(bindings.RouteIDs, binding.Value)
			case "model":
				bindings.Models = append(bindings.Models, binding.Value)
			case "credential":
				bindings.CredentialIDs = append(bindings.CredentialIDs, binding.Value)
			case "credential_provider":
				source, provider, ok := strings.Cut(binding.Value, "\x00")
				if !ok {
					return fmt.Errorf("API key %s has an invalid legacy credential category", scope)
				}
				bindings.CredentialProviders = append(bindings.CredentialProviders,
					billing.CredentialProviderSelector{Source: source, Provider: provider})
			default:
				return fmt.Errorf("API key %s has an invalid legacy routing binding type %q", scope, binding.Kind)
			}
		}
		bindings, err = billing.NormalizeRouteBindings(bindings)
		if err != nil {
			return fmt.Errorf("Validate legacy routing bindings for API key %s: %w", scope, err)
		}
		converted, _ := json.Marshal(bindings)
		updates[scope] = string(converted)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("Read legacy API key routing bindings: %w", err)
	}
	for scope, raw := range updates {
		if _, err = tx.Exec("UPDATE api_keys SET route_bindings_json=? WHERE scope=?", raw, scope); err != nil {
			return fmt.Errorf("Migrate routing bindings for API key %s: %w", scope, err)
		}
	}
	if _, err = tx.Exec("DELETE FROM routes WHERE id = 'system:all'"); err != nil {
		return fmt.Errorf("Clean up legacy routing rules: %w", err)
	}

	return nil
}

func migrateSubscriptionPeriods(tx *sql.Tx) error {
	var id, kind string
	var seconds int64
	err := tx.QueryRow(`
		SELECT id, period_kind, period_seconds FROM plans
		WHERE period_kind NOT IN ('daily', 'weekly', 'monthly', 'custom', 'never')
		   OR (period_kind = 'custom' AND (period_seconds <= 0 OR period_seconds > ?))
		LIMIT 1`, int64(math.MaxInt64)/int64(time.Second)).Scan(&id, &kind, &seconds)
	if err == nil {
		return fmt.Errorf("Subscription plan %s has an invalid legacy cycle: %s (%d seconds)", id, kind, seconds)
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("Check legacy subscription plans: %w", err)
	}
	if _, err = tx.Exec(`
		UPDATE plans SET period_seconds =
			CASE period_kind
				WHEN 'daily' THEN 86400
				WHEN 'weekly' THEN 604800
				WHEN 'monthly' THEN 2592000
				WHEN 'custom' THEN period_seconds
				WHEN 'never' THEN 0
			END
	`); err != nil {
		return fmt.Errorf("Migrate subscription plans: %w", err)
	}
	if _, err = tx.Exec("ALTER TABLE plans DROP COLUMN period_kind"); err != nil {
		return fmt.Errorf("Remove legacy subscription cycle types: %w", err)
	}
	return nil
}

func migrateQuotaWindows(tx *sql.Tx) error {
	rows, err := tx.Query("SELECT id, name, amount_usd, period_seconds FROM plans")
	if err != nil {
		return err
	}
	plans := make(map[string]billing.Plan)
	convertedPeriods := make(map[string]bool)
	for rows.Next() {
		plan := billing.Plan{Currency: billing.USD}
		window := billing.QuotaWindow{ID: "default", Name: "默认额度"}
		if err := rows.Scan(&plan.ID, &plan.Name, &window.Amount, &window.PeriodSeconds); err != nil {
			rows.Close()
			return err
		}
		if window.PeriodSeconds == 0 {
			window.PeriodSeconds = 365 * 24 * 60 * 60
			convertedPeriods[plan.ID] = true
		}
		plan.Windows = []billing.QuotaWindow{window}
		if err := plan.Validate(); err != nil {
			rows.Close()
			return err
		}
		plans[plan.ID] = plan
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = tx.Query("SELECT scope, plan_id, cycle_plan_id, cycle_start_at, cycle_end_at, cycle_spent_usd FROM api_keys")
	if err != nil {
		return err
	}
	cycles := make(map[string]string)
	for rows.Next() {
		var scope string
		var key billing.KeyState
		var cycle billing.QuotaCycle
		var start, end int64
		if err := rows.Scan(&scope, &key.PlanID, &cycle.PlanID, &start, &end, &cycle.Spending.USD); err != nil {
			rows.Close()
			return err
		}
		cycle.StartAt, cycle.EndAt = timeAt(start), timeAt(end)
		if convertedPeriods[key.PlanID] && !cycle.StartAt.IsZero() && cycle.EndAt.IsZero() {
			cycle.EndAt = cycle.StartAt.Add(365 * 24 * time.Hour)
		}
		key.Cycles = map[string]billing.QuotaCycle{}
		if cycle != (billing.QuotaCycle{}) {
			key.Cycles["default"] = cycle
		}
		if err := key.ValidateCycles(plans[key.PlanID]); err != nil {
			rows.Close()
			return fmt.Errorf("Migrate quota cycles: %w", err)
		}
		raw, err := json.Marshal(key.Cycles)
		if err != nil {
			rows.Close()
			return err
		}
		cycles[scope] = string(raw)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE plans ADD COLUMN windows_json TEXT NOT NULL DEFAULT '[]';
        ALTER TABLE api_keys ADD COLUMN cycles_json TEXT NOT NULL DEFAULT '{}';`); err != nil {
		return err
	}
	for id, plan := range plans {
		raw, err := json.Marshal(plan.Windows)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE plans SET windows_json=? WHERE id=?", string(raw), id); err != nil {
			return err
		}
	}
	for scope, raw := range cycles {
		if _, err := tx.Exec("UPDATE api_keys SET cycles_json=? WHERE scope=?", raw, scope); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`ALTER TABLE plans DROP COLUMN amount_usd;
        ALTER TABLE plans DROP COLUMN period_seconds;
        ALTER TABLE api_keys DROP COLUMN cycle_plan_id;
        ALTER TABLE api_keys DROP COLUMN cycle_start_at;
        ALTER TABLE api_keys DROP COLUMN cycle_end_at;
        ALTER TABLE api_keys DROP COLUMN cycle_spent_usd;`)
	return err
}

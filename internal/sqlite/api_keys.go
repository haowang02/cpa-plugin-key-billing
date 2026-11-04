package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"cpa-key-billing/internal/billing"
)

const insertKey = `
INSERT INTO api_keys (
	scope, preview, label, in_config, deleted_at, plan_id, concurrency_limit,
	cycles_json, route_bindings_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(scope) DO UPDATE SET
	preview = excluded.preview, label = excluded.label, in_config = excluded.in_config,
	deleted_at = excluded.deleted_at, plan_id = excluded.plan_id,
	concurrency_limit = excluded.concurrency_limit,
	cycles_json = excluded.cycles_json,
	route_bindings_json = excluded.route_bindings_json`

func saveKey(tx *sql.Tx, scope string, key *billing.KeyState) error {
	if key == nil {
		return fmt.Errorf("API key record is required")
	}
	if strings.TrimSpace(scope) == "" || strings.TrimSpace(key.Preview) == "" {
		return fmt.Errorf("API key identifier and masked preview are required")
	}
	bindings, _ := json.Marshal(key.RouteBindings)
	cycles := key.Cycles
	if cycles == nil {
		cycles = map[string]billing.QuotaCycle{}
	}
	rawCycles, err := json.Marshal(cycles)
	if err != nil {
		return err
	}
	_, errKey := tx.Exec(insertKey,
		scope, key.Preview, key.Label, key.InConfig, nanos(key.DeletedAt), key.PlanID, key.ConcurrencyLimit,
		string(rawCycles), string(bindings))
	if errKey != nil {
		return fmt.Errorf("Save API key %s: %w", scope, errKey)
	}
	return nil
}

func (d *DB) loadKeys(state *billing.State) error {
	// Older traffic-created keys have no recoverable mask. Keep their identity
	// and bindings until usage or a key-list sync supplies the real preview.
	if _, err := d.db.Exec("UPDATE api_keys SET preview = ? WHERE trim(preview) = ''", billing.UnknownKeyPreview); err != nil {
		return fmt.Errorf("Fill API key display names: %w", err)
	}
	rows, errQuery := d.db.Query(`
		SELECT scope, preview, label, in_config, deleted_at, plan_id, concurrency_limit,
			cycles_json, route_bindings_json
		FROM api_keys`)
	if errQuery != nil {
		return fmt.Errorf("Read API key list: %w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			scope        string
			key          billing.KeyState
			deletedAt    int64
			cyclesJSON   string
			bindingsJSON string
		)
		if errScan := rows.Scan(&scope, &key.Preview, &key.Label, &key.InConfig, &deletedAt, &key.PlanID, &key.ConcurrencyLimit,
			&cyclesJSON, &bindingsJSON); errScan != nil {
			return fmt.Errorf("Read API key list: %w", errScan)
		}
		if strings.TrimSpace(scope) == "" || strings.TrimSpace(key.Preview) == "" {
			return fmt.Errorf("API key identifier and masked preview are required")
		}
		key.DeletedAt = timeAt(deletedAt)
		if err := json.Unmarshal([]byte(cyclesJSON), &key.Cycles); err != nil {
			return fmt.Errorf("Read quota cycles: %w", err)
		}
		if key.Cycles == nil {
			return fmt.Errorf("Quota cycles must be a JSON object")
		}
		plan := state.FindPlan(key.PlanID)
		if err := key.ValidateCycles(plan); err != nil {
			return err
		}
		if errDecode := json.Unmarshal([]byte(bindingsJSON), &key.RouteBindings); errDecode != nil {
			return fmt.Errorf("Read routing bindings for API key %s: %w", scope, errDecode)
		}
		normalizedBindings, errBindings := billing.NormalizeRouteBindings(key.RouteBindings)
		if errBindings == nil {
			errBindings = state.ValidateRouteBindings(normalizedBindings)
		}
		if errBindings != nil {
			return fmt.Errorf("Validate routing bindings for API key %s: %w", scope, errBindings)
		}
		key.RouteBindings = normalizedBindings
		state.Keys[scope] = &key
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("Read API key list: %w", errRows)
	}
	return nil
}

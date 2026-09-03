package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"cpa-key-billing/internal/billing"
)

const insertKey = `
INSERT INTO api_keys (
	scope, preview, label, in_config, deleted_at, plan_id, concurrency_limit,
	cycle_plan_id, cycle_start_at, cycle_end_at, cycle_spent_usd, route_bindings_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(scope) DO UPDATE SET
	preview = excluded.preview, label = excluded.label, in_config = excluded.in_config,
	deleted_at = excluded.deleted_at, plan_id = excluded.plan_id,
	concurrency_limit = excluded.concurrency_limit,
	cycle_plan_id = excluded.cycle_plan_id, cycle_start_at = excluded.cycle_start_at,
	cycle_end_at = excluded.cycle_end_at, cycle_spent_usd = excluded.cycle_spent_usd,
	route_bindings_json = excluded.route_bindings_json`

func saveKey(tx *sql.Tx, scope string, key *billing.KeyState) error {
	if key == nil {
		if _, errDelete := tx.Exec("DELETE FROM api_keys WHERE scope = ?", scope); errDelete != nil {
			return fmt.Errorf("删除 API Key %s：%w", scope, errDelete)
		}
		return nil
	}
	bindings, errJSON := json.Marshal(key.RouteBindings)
	if errJSON != nil {
		return fmt.Errorf("保存 API Key %s 的路由绑定：%w", scope, errJSON)
	}
	_, errKey := tx.Exec(insertKey,
		scope, key.Preview, key.Label, key.InConfig, nanos(key.DeletedAt), key.PlanID, key.ConcurrencyLimit,
		key.Cycle.PlanID, nanos(key.Cycle.StartAt), nanos(key.Cycle.EndAt), key.Cycle.SpentUSD, string(bindings))
	if errKey != nil {
		return fmt.Errorf("保存 API Key %s：%w", scope, errKey)
	}
	return nil
}

func replaceKeys(tx *sql.Tx, state *billing.State) error {
	if _, errClear := tx.Exec("DELETE FROM api_keys"); errClear != nil {
		return fmt.Errorf("保存 API Key 列表：%w", errClear)
	}
	for scope, key := range state.Keys {
		if errKey := saveKey(tx, scope, key); errKey != nil {
			return errKey
		}
	}
	return nil
}

func (d *DB) loadKeys(state *billing.State) error {
	rows, errQuery := d.db.Query(`
		SELECT scope, preview, label, in_config, deleted_at, plan_id, concurrency_limit,
			cycle_plan_id, cycle_start_at, cycle_end_at, cycle_spent_usd, route_bindings_json
		FROM api_keys`)
	if errQuery != nil {
		return fmt.Errorf("读取 API Key 列表：%w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			scope                           string
			key                             billing.KeyState
			deletedAt, cycleStart, cycleEnd int64
			bindingsJSON                    string
		)
		if errScan := rows.Scan(&scope, &key.Preview, &key.Label, &key.InConfig, &deletedAt, &key.PlanID, &key.ConcurrencyLimit,
			&key.Cycle.PlanID, &cycleStart, &cycleEnd, &key.Cycle.SpentUSD, &bindingsJSON); errScan != nil {
			return fmt.Errorf("读取 API Key 列表：%w", errScan)
		}
		key.DeletedAt = timeAt(deletedAt)
		key.Cycle.StartAt = timeAt(cycleStart)
		key.Cycle.EndAt = timeAt(cycleEnd)
		if errDecode := json.Unmarshal([]byte(bindingsJSON), &key.RouteBindings); errDecode != nil {
			return fmt.Errorf("读取 API Key %s 的路由绑定：%w", scope, errDecode)
		}
		normalizedBindings, errBindings := billing.NormalizeRouteBindings(key.RouteBindings)
		if errBindings != nil {
			return fmt.Errorf("校验 API Key %s 的路由绑定：%w", scope, errBindings)
		}
		key.RouteBindings = normalizedBindings
		state.Keys[scope] = &key
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("读取 API Key 列表：%w", errRows)
	}
	return nil
}

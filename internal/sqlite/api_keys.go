package sqlite

import (
	"database/sql"
	"fmt"

	"cpa-key-billing/internal/billing"
)

const insertKey = `
INSERT INTO api_keys (
	scope, preview, label, in_config, deleted_at, plan_id, concurrency_limit,
	cycle_plan_id, cycle_start_at, cycle_end_at, cycle_spent_usd
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(scope) DO UPDATE SET
	preview = excluded.preview, label = excluded.label, in_config = excluded.in_config,
	deleted_at = excluded.deleted_at, plan_id = excluded.plan_id,
	concurrency_limit = excluded.concurrency_limit,
	cycle_plan_id = excluded.cycle_plan_id, cycle_start_at = excluded.cycle_start_at,
	cycle_end_at = excluded.cycle_end_at, cycle_spent_usd = excluded.cycle_spent_usd`

func saveKey(tx *sql.Tx, scope string, key *billing.KeyState) error {
	if key == nil {
		if _, errDelete := tx.Exec("DELETE FROM api_keys WHERE scope = ?", scope); errDelete != nil {
			return fmt.Errorf("删除 API Key %s：%w", scope, errDelete)
		}
		return nil
	}
	_, errKey := tx.Exec(insertKey,
		scope, key.Preview, key.Label, key.InConfig, nanos(key.DeletedAt), key.PlanID, key.ConcurrencyLimit,
		key.Cycle.PlanID, nanos(key.Cycle.StartAt), nanos(key.Cycle.EndAt), key.Cycle.SpentUSD)
	if errKey != nil {
		return fmt.Errorf("保存 API Key %s：%w", scope, errKey)
	}
	return saveKeyModelAccess(tx, scope, key)
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
			cycle_plan_id, cycle_start_at, cycle_end_at, cycle_spent_usd
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
		)
		if errScan := rows.Scan(&scope, &key.Preview, &key.Label, &key.InConfig, &deletedAt, &key.PlanID, &key.ConcurrencyLimit,
			&key.Cycle.PlanID, &cycleStart, &cycleEnd, &key.Cycle.SpentUSD); errScan != nil {
			return fmt.Errorf("读取 API Key 列表：%w", errScan)
		}
		key.DeletedAt = timeAt(deletedAt)
		key.Cycle.StartAt = timeAt(cycleStart)
		key.Cycle.EndAt = timeAt(cycleEnd)
		state.Keys[scope] = &key
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("读取 API Key 列表：%w", errRows)
	}
	return d.loadKeyModelAccess(state)
}

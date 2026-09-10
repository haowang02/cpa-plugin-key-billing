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
	cycles_json, route_bindings_json, billing_since
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(scope) DO UPDATE SET
	preview = excluded.preview, label = excluded.label, in_config = excluded.in_config,
	deleted_at = excluded.deleted_at, plan_id = excluded.plan_id,
	concurrency_limit = excluded.concurrency_limit,
	cycles_json = excluded.cycles_json,
	route_bindings_json = excluded.route_bindings_json, billing_since = excluded.billing_since`

func saveKey(tx *sql.Tx, scope string, key *billing.KeyState) error {
	if key == nil {
		return fmt.Errorf("API Key 记录不能为空")
	}
	if strings.TrimSpace(scope) == "" || strings.TrimSpace(key.Preview) == "" {
		return fmt.Errorf("API Key 的标识和掩码不能为空")
	}
	bindings, errJSON := json.Marshal(key.RouteBindings)
	if errJSON != nil {
		return fmt.Errorf("保存 API Key %s 的路由绑定：%w", scope, errJSON)
	}
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
		string(rawCycles), string(bindings), nanos(key.BillingSince))
	if errKey != nil {
		return fmt.Errorf("保存 API Key %s：%w", scope, errKey)
	}
	return nil
}

func (d *DB) loadKeys(state *billing.State) error {
	// Older traffic-created keys have no recoverable mask. Keep their identity
	// and bindings until usage or a key-list sync supplies the real preview.
	if _, err := d.db.Exec("UPDATE api_keys SET preview = ? WHERE trim(preview) = ''", billing.UnknownKeyPreview); err != nil {
		return fmt.Errorf("补齐 API Key 显示名称：%w", err)
	}
	rows, errQuery := d.db.Query(`
		SELECT scope, preview, label, in_config, deleted_at, plan_id, concurrency_limit,
			cycles_json, route_bindings_json, billing_since
		FROM api_keys`)
	if errQuery != nil {
		return fmt.Errorf("读取 API Key 列表：%w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			scope        string
			key          billing.KeyState
			deletedAt    int64
			billingSince int64
			cyclesJSON   string
			bindingsJSON string
		)
		if errScan := rows.Scan(&scope, &key.Preview, &key.Label, &key.InConfig, &deletedAt, &key.PlanID, &key.ConcurrencyLimit,
			&cyclesJSON, &bindingsJSON, &billingSince); errScan != nil {
			return fmt.Errorf("读取 API Key 列表：%w", errScan)
		}
		if strings.TrimSpace(scope) == "" || strings.TrimSpace(key.Preview) == "" {
			return fmt.Errorf("API Key 的标识和掩码不能为空")
		}
		key.DeletedAt = timeAt(deletedAt)
		key.BillingSince = timeAt(billingSince)
		if err := json.Unmarshal([]byte(cyclesJSON), &key.Cycles); err != nil {
			return fmt.Errorf("读取额度周期：%w", err)
		}
		if key.Cycles == nil {
			return fmt.Errorf("额度周期必须为 JSON 对象")
		}
		plan, _ := state.FindPlan(key.PlanID)
		if err := key.ValidateCycles(plan); err != nil {
			return err
		}
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

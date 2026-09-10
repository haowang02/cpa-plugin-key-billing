package sqlite

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

	"cpa-key-billing/internal/billing"
)

func replacePlans(tx *sql.Tx, state *billing.State) error {
	if _, errClear := tx.Exec("DELETE FROM plans"); errClear != nil {
		return fmt.Errorf("保存订阅计划：%w", errClear)
	}
	for position, plan := range state.Plans {
		if err := plan.Validate(); err != nil {
			return err
		}
		raw, err := json.Marshal(plan.Windows)
		if err != nil {
			return err
		}
		_, errPlan := tx.Exec(`
			INSERT INTO plans (position, id, name, windows_json, started_at, cycle_scope)
			VALUES (?, ?, ?, ?, ?, ?)`,
			position, plan.ID, plan.Name, string(raw), nanos(plan.StartedAt), plan.CycleScope)
		if errPlan != nil {
			return fmt.Errorf("保存订阅计划 %s：%w", plan.ID, errPlan)
		}
	}
	return nil
}

func (d *DB) loadPlans(state *billing.State) error {
	rows, errQuery := d.db.Query(`
		SELECT id, name, windows_json, started_at, cycle_scope FROM plans ORDER BY position`)
	if errQuery != nil {
		return fmt.Errorf("读取订阅计划：%w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var plan billing.Plan
		var raw string
		var startedAt int64
		if errScan := rows.Scan(&plan.ID, &plan.Name, &raw, &startedAt, &plan.CycleScope); errScan != nil {
			return fmt.Errorf("读取订阅计划：%w", errScan)
		}
		plan.StartedAt = timeAt(startedAt)
		if err := json.Unmarshal([]byte(raw), &plan.Windows); err != nil {
			return fmt.Errorf("读取订阅计划 %s：%w", plan.ID, err)
		}
		if err := plan.Validate(); err != nil {
			return err
		}
		slices.SortFunc(plan.Windows, func(a, b billing.QuotaWindow) int {
			return cmp.Compare(a.PeriodSeconds, b.PeriodSeconds)
		})
		state.Plans = append(state.Plans, plan)
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("读取订阅计划：%w", errRows)
	}
	return nil
}

package sqlite

import (
	"database/sql"
	"fmt"

	"cpa-key-billing/internal/billing"
)

func replacePlans(tx *sql.Tx, state *billing.State) error {
	if _, errClear := tx.Exec("DELETE FROM plans"); errClear != nil {
		return fmt.Errorf("保存订阅计划：%w", errClear)
	}
	for position, plan := range state.Plans {
		_, errPlan := tx.Exec(`
			INSERT INTO plans (position, id, name, amount_usd, period_kind, period_seconds)
			VALUES (?, ?, ?, ?, ?, ?)`,
			position, plan.ID, plan.Name, plan.AmountUSD, string(plan.Period.Kind), plan.Period.Seconds)
		if errPlan != nil {
			return fmt.Errorf("保存订阅计划 %s：%w", plan.ID, errPlan)
		}
	}
	return nil
}

func (d *DB) loadPlans(state *billing.State) error {
	rows, errQuery := d.db.Query(`
		SELECT id, name, amount_usd, period_kind, period_seconds FROM plans ORDER BY position`)
	if errQuery != nil {
		return fmt.Errorf("读取订阅计划：%w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			plan billing.Plan
			kind string
		)
		if errScan := rows.Scan(&plan.ID, &plan.Name, &plan.AmountUSD, &kind, &plan.Period.Seconds); errScan != nil {
			return fmt.Errorf("读取订阅计划：%w", errScan)
		}
		plan.Period.Kind = billing.PeriodKind(kind)
		state.Plans = append(state.Plans, plan)
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("读取订阅计划：%w", errRows)
	}
	return nil
}

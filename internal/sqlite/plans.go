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
		return fmt.Errorf("Save subscription plans: %w", errClear)
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
			INSERT INTO plans (position, id, name, currency, windows_json)
			VALUES (?, ?, ?, ?, ?)`,
			position, plan.ID, plan.Name, string(plan.Currency), string(raw))
		if errPlan != nil {
			return fmt.Errorf("Save subscription plan %s: %w", plan.ID, errPlan)
		}
	}
	return nil
}

func (d *DB) loadPlans(state *billing.State) error {
	rows, errQuery := d.db.Query(`
		SELECT id, name, currency, windows_json FROM plans ORDER BY position`)
	if errQuery != nil {
		return fmt.Errorf("Read subscription plans: %w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var plan billing.Plan
		var raw string
		if errScan := rows.Scan(&plan.ID, &plan.Name, &plan.Currency, &raw); errScan != nil {
			return fmt.Errorf("Read subscription plans: %w", errScan)
		}
		if err := json.Unmarshal([]byte(raw), &plan.Windows); err != nil {
			return fmt.Errorf("Read subscription plan %s: %w", plan.ID, err)
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
		return fmt.Errorf("Read subscription plans: %w", errRows)
	}
	return nil
}

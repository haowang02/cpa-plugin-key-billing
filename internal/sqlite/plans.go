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
		cycles, err := planCyclesJSON(&plan)
		if err != nil {
			return err
		}
		_, errPlan := tx.Exec(`
			INSERT INTO plans (position, id, name, currency, windows_json, shared_quota, cycles_json)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			position, plan.ID, plan.Name, string(plan.Currency), string(raw), plan.SharedQuota, string(cycles))
		if errPlan != nil {
			return fmt.Errorf("Save subscription plan %s: %w", plan.ID, errPlan)
		}
	}
	return nil
}

func (d *DB) loadPlans(state *billing.State) error {
	rows, errQuery := d.db.Query(`
		SELECT id, name, currency, windows_json, shared_quota, cycles_json FROM plans ORDER BY position`)
	if errQuery != nil {
		return fmt.Errorf("Read subscription plans: %w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var plan billing.Plan
		var raw, cycles string
		if errScan := rows.Scan(&plan.ID, &plan.Name, &plan.Currency, &raw, &plan.SharedQuota, &cycles); errScan != nil {
			return fmt.Errorf("Read subscription plans: %w", errScan)
		}
		if err := json.Unmarshal([]byte(raw), &plan.Windows); err != nil {
			return fmt.Errorf("Read subscription plan %s: %w", plan.ID, err)
		}
		if err := plan.Validate(); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(cycles), &plan.Cycles); err != nil {
			return fmt.Errorf("Read shared quota cycles: %w", err)
		}
		if plan.Cycles == nil {
			return fmt.Errorf("Quota cycles must be a JSON object")
		}
		if err := plan.ValidateCycles(); err != nil {
			return err
		}
		if len(plan.Cycles) == 0 {
			plan.Cycles = nil
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

func planCyclesJSON(plan *billing.Plan) ([]byte, error) {
	if err := plan.ValidateCycles(); err != nil {
		return nil, err
	}
	cycles := plan.Cycles
	if cycles == nil {
		cycles = billing.QuotaCycles{}
	}
	return json.Marshal(cycles)
}

func savePlanCycles(tx *sql.Tx, plan *billing.Plan) error {
	raw, err := planCyclesJSON(plan)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE plans SET cycles_json=? WHERE id=?", string(raw), plan.ID); err != nil {
		return fmt.Errorf("Save shared quota cycles: %w", err)
	}
	return nil
}

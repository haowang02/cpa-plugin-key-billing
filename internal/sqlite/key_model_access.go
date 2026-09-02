package sqlite

import (
	"database/sql"
	"fmt"

	"cpa-key-billing/internal/billing"
)

func saveKeyModelAccess(tx *sql.Tx, scope string, key *billing.KeyState) error {
	for _, table := range []string{"key_model_groups", "key_allowed_models"} {
		if _, errClear := tx.Exec("DELETE FROM "+table+" WHERE scope = ?", scope); errClear != nil {
			return fmt.Errorf("保存 API Key %s 的可用模型：%w", scope, errClear)
		}
	}
	for position, group := range key.ModelGroupIDs {
		_, errGroup := tx.Exec(`
			INSERT INTO key_model_groups (scope, position, group_id) VALUES (?, ?, ?)`,
			scope, position, group)
		if errGroup != nil {
			return fmt.Errorf("保存 API Key %s 的模型分组：%w", scope, errGroup)
		}
	}
	for position, model := range key.Models {
		_, errModel := tx.Exec(`
			INSERT INTO key_allowed_models (scope, position, model) VALUES (?, ?, ?)`,
			scope, position, model)
		if errModel != nil {
			return fmt.Errorf("保存 API Key %s 的可用模型：%w", scope, errModel)
		}
	}
	return nil
}

func (d *DB) loadKeyModelAccess(state *billing.State) error {
	groups, errGroups := d.loadKeyStrings("key_model_groups", "group_id")
	if errGroups != nil {
		return errGroups
	}
	models, errModels := d.loadKeyStrings("key_allowed_models", "model")
	if errModels != nil {
		return errModels
	}
	for scope, key := range state.Keys {
		key.ModelGroupIDs = groups[scope]
		key.Models = models[scope]
	}
	return nil
}

func (d *DB) loadKeyStrings(table, column string) (map[string][]string, error) {
	rows, errQuery := d.db.Query("SELECT scope, " + column + " FROM " + table + " ORDER BY scope, position")
	if errQuery != nil {
		return nil, fmt.Errorf("读取 API Key 的可用模型：%w", errQuery)
	}
	defer rows.Close()
	values := make(map[string][]string)
	for rows.Next() {
		var scope, value string
		if errScan := rows.Scan(&scope, &value); errScan != nil {
			return nil, fmt.Errorf("读取 API Key 的可用模型：%w", errScan)
		}
		values[scope] = append(values[scope], value)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("读取 API Key 的可用模型：%w", errRows)
	}
	return values, nil
}

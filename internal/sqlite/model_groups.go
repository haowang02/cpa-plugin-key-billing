package sqlite

import (
	"database/sql"
	"fmt"

	"cpa-key-billing/internal/billing"
)

func replaceModelGroups(tx *sql.Tx, state *billing.State) error {
	for _, table := range []string{"model_groups", "model_group_models"} {
		if _, errClear := tx.Exec("DELETE FROM " + table); errClear != nil {
			return fmt.Errorf("保存模型分组：%w", errClear)
		}
	}
	for position, group := range state.ModelGroups {
		_, errGroup := tx.Exec(`
			INSERT INTO model_groups (position, id, name) VALUES (?, ?, ?)`,
			position, group.ID, group.Name)
		if errGroup != nil {
			return fmt.Errorf("保存模型分组 %s：%w", group.ID, errGroup)
		}
		for index, model := range group.Models {
			_, errModel := tx.Exec(`
				INSERT INTO model_group_models (group_id, position, model) VALUES (?, ?, ?)`,
				group.ID, index, model)
			if errModel != nil {
				return fmt.Errorf("保存模型分组 %s 的模型：%w", group.ID, errModel)
			}
		}
	}
	return nil
}

func (d *DB) loadModelGroups(state *billing.State) error {
	members, errMembers := d.loadModelGroupMembers()
	if errMembers != nil {
		return errMembers
	}
	rows, errQuery := d.db.Query("SELECT id, name FROM model_groups ORDER BY position")
	if errQuery != nil {
		return fmt.Errorf("读取模型分组：%w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var group billing.ModelGroup
		if errScan := rows.Scan(&group.ID, &group.Name); errScan != nil {
			return fmt.Errorf("读取模型分组：%w", errScan)
		}
		group.Models = members[group.ID]
		state.ModelGroups = append(state.ModelGroups, group)
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("读取模型分组：%w", errRows)
	}
	return nil
}

func (d *DB) loadModelGroupMembers() (map[string][]string, error) {
	rows, errQuery := d.db.Query("SELECT group_id, model FROM model_group_models ORDER BY group_id, position")
	if errQuery != nil {
		return nil, fmt.Errorf("读取模型分组的模型：%w", errQuery)
	}
	defer rows.Close()
	members := make(map[string][]string)
	for rows.Next() {
		var group, model string
		if errScan := rows.Scan(&group, &model); errScan != nil {
			return nil, fmt.Errorf("读取模型分组的模型：%w", errScan)
		}
		members[group] = append(members[group], model)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("读取模型分组的模型：%w", errRows)
	}
	return members, nil
}

package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
)

func (d *DB) SetCredentialTestModel(authIndex, model string) error {
	query, args := "INSERT OR REPLACE INTO credential_test_models (auth_index, model) VALUES (?, ?)", []any{authIndex, model}
	if model == "" {
		query, args = "DELETE FROM credential_test_models WHERE auth_index = ?", []any{authIndex}
	}
	if _, err := d.db.Exec(query, args...); err != nil {
		return fmt.Errorf("Save credential test model: %w", err)
	}
	return nil
}

func (d *DB) CredentialTestModel(authIndex string) (string, error) {
	var model string
	err := d.db.QueryRow("SELECT model FROM credential_test_models WHERE auth_index = ?", authIndex).Scan(&model)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("Read credential test model: %w", err)
	}
	return model, nil
}

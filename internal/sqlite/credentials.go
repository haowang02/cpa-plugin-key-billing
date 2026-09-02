package sqlite

import (
	"database/sql"
	"fmt"

	"cpa-key-billing/internal/billing"
)

func replaceCredentials(tx *sql.Tx, state *billing.State) error {
	if _, errClear := tx.Exec("DELETE FROM credentials"); errClear != nil {
		return fmt.Errorf("保存上游凭据：%w", errClear)
	}
	for index, credential := range state.Credentials {
		_, errCredential := tx.Exec(`
			INSERT INTO credentials (auth_index, provider, account, name) VALUES (?, ?, ?, ?)`,
			index, credential.Provider, credential.Account, credential.Name())
		if errCredential != nil {
			return fmt.Errorf("保存上游凭据 %s：%w", index, errCredential)
		}
	}
	return nil
}

func (d *DB) loadCredentials(state *billing.State) error {
	rows, errQuery := d.db.Query("SELECT auth_index, provider, account FROM credentials")
	if errQuery != nil {
		return fmt.Errorf("读取上游凭据：%w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			index      string
			credential billing.Credential
		)
		if errScan := rows.Scan(&index, &credential.Provider, &credential.Account); errScan != nil {
			return fmt.Errorf("读取上游凭据：%w", errScan)
		}
		state.Credentials[index] = credential
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("读取上游凭据：%w", errRows)
	}
	return nil
}

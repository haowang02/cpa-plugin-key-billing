package sqlite

import "database/sql"

// Preserve all existing plans in the original per-key mode, without changing
// cycle JSON or historical events. Switching modes is an explicit plan edit.
func migratePlanClocks(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE plans ADD COLUMN started_at INTEGER NOT NULL DEFAULT 0;
 ALTER TABLE plans ADD COLUMN cycle_scope TEXT NOT NULL DEFAULT '';
 ALTER TABLE api_keys ADD COLUMN billing_since INTEGER NOT NULL DEFAULT 0;`)
	return err
}

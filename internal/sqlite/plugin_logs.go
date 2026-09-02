package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	"cpa-key-billing/internal/billing"
)

func (d *DB) AppendPluginLog(entry billing.PluginLog, cutoff time.Time) error {
	return d.transact(func(tx *sql.Tx) error {
		if _, errInsert := tx.Exec(
			"INSERT INTO plugin_logs (at, level, message) VALUES (?, ?, ?)",
			nanos(entry.At), string(entry.Level), entry.Message); errInsert != nil {
			return fmt.Errorf("写入插件日志：%w", errInsert)
		}
		return prunePluginLogs(tx.Exec, cutoff)
	})
}

func prunePluginLogs(exec execer, cutoff time.Time) error {
	if cutoff.IsZero() {
		return nil
	}
	if _, errPrune := exec("DELETE FROM plugin_logs WHERE at < ?", nanos(cutoff)); errPrune != nil {
		return fmt.Errorf("清理过期插件日志：%w", errPrune)
	}
	return nil
}

// PluginLogs answers newest first, ordered by the id each line was
// committed under, so two stamped in the same nanosecond still read in order.
func (d *DB) PluginLogs(since time.Time) ([]billing.PluginLog, error) {
	rows, errQuery := d.db.Query(
		"SELECT at, level, message FROM plugin_logs WHERE at >= ? ORDER BY id DESC", nanos(since))
	if errQuery != nil {
		return nil, fmt.Errorf("读取插件日志：%w", errQuery)
	}
	defer rows.Close()
	entries := []billing.PluginLog{}
	for rows.Next() {
		var (
			entry billing.PluginLog
			at    int64
			level string
		)
		if errScan := rows.Scan(&at, &level, &entry.Message); errScan != nil {
			return nil, fmt.Errorf("读取插件日志：%w", errScan)
		}
		entry.At = timeAt(at)
		entry.Level = billing.PluginLogLevel(level)
		entries = append(entries, entry)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("读取插件日志：%w", errRows)
	}
	return entries, nil
}

func (d *DB) ClearPluginLogs() (int, error) {
	result, err := d.db.Exec("DELETE FROM plugin_logs")
	if err != nil {
		return 0, fmt.Errorf("清空插件日志：%w", err)
	}
	cleared, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("清空插件日志：%w", err)
	}
	return int(cleared), nil
}

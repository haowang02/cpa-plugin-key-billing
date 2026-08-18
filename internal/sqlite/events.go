package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	"cpa-key-billing/internal/billing"
)

func (d *DB) AppendEvent(event billing.Event, cutoff time.Time) error {
	return d.transact(func(tx *sql.Tx) error {
		if _, errInsert := tx.Exec(
			"INSERT INTO plugin_log (at, level, message) VALUES (?, ?, ?)",
			nanos(event.At), string(event.Level), event.Message); errInsert != nil {
			return fmt.Errorf("写入插件日志：%w", errInsert)
		}
		return pruneEvents(tx.Exec, cutoff)
	})
}

func pruneEvents(exec execer, cutoff time.Time) error {
	if cutoff.IsZero() {
		return nil
	}
	if _, errPrune := exec("DELETE FROM plugin_log WHERE at < ?", nanos(cutoff)); errPrune != nil {
		return fmt.Errorf("清理过期插件日志：%w", errPrune)
	}
	return nil
}

// Events answers the log newest first, ordered by the id each line was
// committed under, so two stamped in the same nanosecond still read in order.
func (d *DB) Events(since time.Time) ([]billing.Event, error) {
	rows, errQuery := d.db.Query(
		"SELECT at, level, message FROM plugin_log WHERE at >= ? ORDER BY id DESC", nanos(since))
	if errQuery != nil {
		return nil, fmt.Errorf("读取插件日志：%w", errQuery)
	}
	defer rows.Close()
	events := []billing.Event{}
	for rows.Next() {
		var (
			event billing.Event
			at    int64
			level string
		)
		if errScan := rows.Scan(&at, &level, &event.Message); errScan != nil {
			return nil, fmt.Errorf("读取插件日志：%w", errScan)
		}
		event.At = timeAt(at)
		event.Level = billing.EventLevel(level)
		events = append(events, event)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("读取插件日志：%w", errRows)
	}
	return events, nil
}

func (d *DB) ClearEvents() (int, error) {
	return d.clear("DELETE FROM plugin_log", "插件日志")
}

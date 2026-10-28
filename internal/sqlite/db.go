// Package sqlite implements billing.Repository with SQLite.
package sqlite

import (
	"database/sql"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"cpa-key-billing/internal/billing"
)

const schemaVersion = 24

type DB struct {
	db   *sql.DB
	path string
}

// StoredQuotaHeaders reads the quota report out of the headers a request stored
// before schema 20; nil means the headers report nothing.
type StoredQuotaHeaders func(provider string, headers http.Header, at time.Time) *billing.QuotaReport

// Open opens the billing database at path, migrating older formats;
// storedHeaders reads the quota headers that formats before schema 20 stored.
func Open(path string, storedHeaders StoredQuotaHeaders) (*DB, error) {
	database, err := connect(path)
	if err != nil {
		return nil, err
	}
	if err := database.transact(func(tx *sql.Tx) error {
		if err := database.initSchema(tx, storedHeaders); err != nil {
			return err
		}
		for _, table := range slices.Sorted(maps.Keys(indexes)) {
			if err := syncTableIndexes(tx, table, indexes[table]); err != nil {
				return fmt.Errorf("Sync %s index: %w", table, err)
			}
		}
		return nil
	}); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

// connect opens the database at path as it is.
func connect(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("Create billing database directory %s: %w", dir, err)
	}
	if err := secureFiles(path); err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: path, OmitHost: true}).String() +
		"?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate"
	handle, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("Open billing database %s: %w", path, err)
	}
	handle.SetMaxOpenConns(1)
	handle.SetMaxIdleConns(1)
	handle.SetConnMaxLifetime(0)
	return &DB{db: handle, path: path}, nil
}

func (d *DB) initSchema(tx *sql.Tx, storedHeaders StoredQuotaHeaders) error {
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("Read billing database %s: %w", d.path, err)
	}
	switch version {
	case 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23:
		if err := migrateSchema(tx, version, storedHeaders); err != nil {
			return err
		}
	case schemaVersion:
		return nil
	case 0:
		var existingTables int
		if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master
			WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&existingTables); err != nil {
			return fmt.Errorf("Check billing database %s: %w", d.path, err)
		}
		if existingTables != 0 {
			return fmt.Errorf("Billing database %s uses an unsupported format; select another data file with state_file", d.path)
		}
		if _, err := tx.Exec(schema); err != nil {
			return fmt.Errorf("Initialize billing database %s: %w", d.path, err)
		}
	default:
		return fmt.Errorf("Billing database %s uses an unsupported format; select another data file with state_file", d.path)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("Set billing database %s format version: %w", d.path, err)
	}
	return nil
}

func syncTableIndexes(tx *sql.Tx, table string, definitions []index) error {
	wanted := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		name := table + "_" + definition.name
		wanted[name] = "CREATE INDEX " + name + " ON " + table + "(" + definition.columns + ")"
	}
	rows, err := tx.Query(`SELECT m.name, m.sql FROM sqlite_master m
		JOIN pragma_index_list(?) i ON i.name = m.name
		WHERE m.type = 'index' AND i.origin = 'c' AND i."unique" = 0`, table)
	if err != nil {
		return err
	}
	obsolete := []string{}
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			rows.Close()
			return err
		}
		if !strings.HasPrefix(name, table+"_") {
			continue
		}
		if wanted[name] == definition {
			delete(wanted, name)
		} else {
			obsolete = append(obsolete, name)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range obsolete {
		if _, err := tx.Exec(`DROP INDEX "` + strings.ReplaceAll(name, `"`, `""`) + `"`); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(wanted)) {
		if _, err := tx.Exec(wanted[name]); err != nil {
			return err
		}
	}
	return nil
}

func secureFiles(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("Create billing database %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("Create billing database %s: %w", path, err)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(name, 0o600); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("Set billing database %s permissions: %w", name, err)
		}
	}
	return nil
}

func (d *DB) Close() error { return d.db.Close() }

type execer func(string, ...any) (sql.Result, error)

func (d *DB) transact(fn func(*sql.Tx) error) error {
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("Begin billing database transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("Commit billing database transaction: %w", err)
	}
	return nil
}

func nanos(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.UTC().UnixNano()
}

func timeAt(stored int64) time.Time {
	if stored == 0 {
		return time.Time{}
	}
	return time.Unix(0, stored).UTC()
}

func optionalPrice(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func priceOrNil(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}

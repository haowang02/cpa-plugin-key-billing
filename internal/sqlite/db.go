// Package sqlite implements billing.Repository with SQLite.
package sqlite

import (
	"database/sql"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const schemaVersion = 15

type DB struct {
	db   *sql.DB
	path string
}

func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建计费数据库目录 %s：%w", dir, err)
	}
	if err := secureFiles(path); err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: path, OmitHost: true}).String() +
		"?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate"
	handle, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开计费数据库 %s：%w", path, err)
	}
	handle.SetMaxOpenConns(1)
	handle.SetMaxIdleConns(1)
	handle.SetConnMaxLifetime(0)
	database := &DB{db: handle, path: path}
	if err := database.transact(func(tx *sql.Tx) error {
		if err := database.initSchema(tx); err != nil {
			return err
		}
		for _, table := range slices.Sorted(maps.Keys(indexes)) {
			if err := syncTableIndexes(tx, table, indexes[table]); err != nil {
				return fmt.Errorf("同步 %s 索引：%w", table, err)
			}
		}
		return nil
	}); err != nil {
		_ = handle.Close()
		return nil, err
	}
	return database, nil
}

func (d *DB) initSchema(tx *sql.Tx) error {
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("读取计费数据库 %s：%w", d.path, err)
	}
	switch version {
	case 10, 11, 12, 13:
		if err := migrateToV14(tx, version); err != nil {
			return err
		}
		fallthrough
	case 14:
		if err := migratePlanClocks(tx); err != nil {
			return err
		}
	case schemaVersion:
		return nil
	case 0:
		var existingTables int
		if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master
			WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&existingTables); err != nil {
			return fmt.Errorf("检查计费数据库 %s：%w", d.path, err)
		}
		if existingTables != 0 {
			return fmt.Errorf("计费数据库 %s 的文件格式不受支持，请通过 state_file 配置使用其他数据文件", d.path)
		}
		if _, err := tx.Exec(schema); err != nil {
			return fmt.Errorf("初始化计费数据库 %s：%w", d.path, err)
		}
	default:
		return fmt.Errorf("计费数据库 %s 的文件格式不受支持，请通过 state_file 配置使用其他数据文件", d.path)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("标记计费数据库 %s 的格式版本：%w", d.path, err)
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
		return fmt.Errorf("创建计费数据库 %s：%w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("创建计费数据库 %s：%w", path, err)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(name, 0o600); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("设置计费数据库 %s 权限：%w", name, err)
		}
	}
	return nil
}

func (d *DB) Close() error { return d.db.Close() }

type execer func(string, ...any) (sql.Result, error)

func (d *DB) transact(fn func(*sql.Tx) error) error {
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("开始计费数据库事务：%w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交计费数据库事务：%w", err)
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

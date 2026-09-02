// Package sqlite implements billing.Repository with SQLite.
package sqlite

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Earlier schemas are rejected rather than migrated.
const schemaVersion = 10

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
	if err := database.init(); err != nil {
		_ = handle.Close()
		return nil, err
	}
	return database, nil
}

func (d *DB) init() error {
	var version int
	if err := d.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("读取计费数据库 %s：%w", d.path, err)
	}
	if version != 0 && version != schemaVersion {
		return fmt.Errorf("计费数据库 %s 的格式版本为 %d；当前版本不迁移旧数据，请更换 state 文件", d.path, version)
	}
	if version == schemaVersion {
		return nil
	}
	var existingTables int
	if err := d.db.QueryRow(`SELECT count(*) FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&existingTables); err != nil {
		return fmt.Errorf("检查计费数据库 %s：%w", d.path, err)
	}
	if existingTables != 0 {
		return fmt.Errorf("计费数据库 %s 包含旧表；当前版本不迁移旧数据，请更换 state 文件", d.path)
	}
	return d.transact(func(tx *sql.Tx) error {
		if _, err := tx.Exec(schema); err != nil {
			return fmt.Errorf("初始化计费数据库 %s：%w", d.path, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return fmt.Errorf("标记计费数据库 %s 的格式版本：%w", d.path, err)
		}
		return nil
	})
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

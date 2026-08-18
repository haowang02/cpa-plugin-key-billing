// Package sqlite persists the billing domain. It is the only package in the
// plugin that speaks SQL, and it depends on internal/billing rather than the
// other way round, so the domain stays free of storage concerns.
package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	// The driver is cgo SQLite. The plugin is built as a c-shared library, so
	// cgo is available anyway, and a C implementation starts no goroutines of
	// its own — which a Go runtime living inside CLIProxyAPI's process cannot
	// afford. See the note on billing.Store.
	"github.com/mattn/go-sqlite3"

	"cpa-key-billing/internal/billing"
)

// schemaVersion is kept in PRAGMA user_version. A database written by a newer
// plugin is refused rather than misread — including one an operator downgrading
// meets, which is the point: version 2 holds the models each API Key may call,
// and a version 1 plugin would serve that database while silently granting every
// key every model. Version 3 holds the plugin log, which a version 2 plugin
// would keep writing to memory while the persisted lines went unread.
const schemaVersion = 3

// driverName is this package's own registration of the SQLite driver. It exists
// for ulower(): the built-in lower() folds ASCII only, so a key labelled in any
// other alphabet would not match a search typed in the other case.
const driverName = "sqlite3_cpa_billing"

func init() {
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return conn.RegisterFunc("ulower", strings.ToLower, true)
		},
	})
}

type DB struct {
	db   *sql.DB
	path string
}

// Open connects to the database at path, creating it and its directory when
// they do not exist.
func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
		return nil, fmt.Errorf("创建计费数据库目录 %s：%w", dir, errMkdir)
	}
	if errSecure := secureFiles(path); errSecure != nil {
		return nil, errSecure
	}
	// A single connection: this process is the only writer, and serializing on
	// it is what keeps SQLITE_BUSY out of the request path. WAL with NORMAL
	// synchronization commits without an fsync per request, which is what makes
	// writing through on every completed request affordable.
	handle, errOpen := sql.Open(driverName,
		path+"?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate")
	if errOpen != nil {
		return nil, fmt.Errorf("打开计费数据库 %s：%w", path, errOpen)
	}
	handle.SetMaxOpenConns(1)
	handle.SetMaxIdleConns(1)
	handle.SetConnMaxLifetime(0)

	database := &DB{db: handle, path: path}
	if errInit := database.init(); errInit != nil {
		_ = handle.Close()
		return nil, errInit
	}
	return database, nil
}

func (d *DB) init() error {
	var version int
	if errVersion := d.db.QueryRow("PRAGMA user_version").Scan(&version); errVersion != nil {
		return fmt.Errorf("读取计费数据库 %s：%w", d.path, errVersion)
	}
	if version > schemaVersion {
		return fmt.Errorf("计费数据库 %s 的格式版本为 %d，当前插件需要版本 %d", d.path, version, schemaVersion)
	}
	if _, errSchema := d.db.Exec(schema); errSchema != nil {
		return fmt.Errorf("初始化计费数据库 %s：%w", d.path, errSchema)
	}
	// Only a database this call created is seeded from the JSON document, and
	// the version that says so is stamped by the seed itself. Stamping it here
	// would declare a database that failed to import ready, and the document
	// beside it would never be read again.
	if version == 0 {
		return d.seed()
	}
	if version < schemaVersion {
		// Every version so far has only added tables, which the statements above
		// created. Recording that is what stops a plugin from before them from
		// opening this database and reading it as complete.
		return d.stampVersion()
	}
	return nil
}

func (d *DB) stampVersion() error {
	if _, errVersion := d.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); errVersion != nil {
		return fmt.Errorf("标记计费数据库 %s 的格式版本：%w", d.path, errVersion)
	}
	return nil
}

// Billing history names masked keys, operator remarks and upstream credentials.
// SQLite gives the write-ahead log and shared-memory files the mode of the
// database, so the database has to carry the restricted one before the driver
// opens it — a mode set afterwards leaves the sidecars this process writes
// through readable by anyone. A database from an earlier version, and any
// sidecar a crash left behind, is narrowed here too.
func secureFiles(path string) error {
	file, errCreate := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if errCreate != nil {
		return fmt.Errorf("创建计费数据库 %s：%w", path, errCreate)
	}
	if errClose := file.Close(); errClose != nil {
		return fmt.Errorf("创建计费数据库 %s：%w", path, errClose)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if errChmod := os.Chmod(name, 0o600); errChmod != nil && !os.IsNotExist(errChmod) {
			return fmt.Errorf("设置计费数据库 %s 权限：%w", name, errChmod)
		}
	}
	return nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

// execer is satisfied by both *sql.DB and *sql.Tx, so pruning runs either
// inside the transaction that appended or on its own when a log is opened.
type execer func(string, ...any) (sql.Result, error)

// clear empties one log and reports how many entries went.
func (d *DB) clear(statement, name string) (int, error) {
	result, errDelete := d.db.Exec(statement)
	if errDelete != nil {
		return 0, fmt.Errorf("清空%s：%w", name, errDelete)
	}
	cleared, errCount := result.RowsAffected()
	if errCount != nil {
		return 0, fmt.Errorf("清空%s：%w", name, errCount)
	}
	return int(cleared), nil
}

func (d *DB) transact(fn func(*sql.Tx) error) error {
	tx, errBegin := d.db.Begin()
	if errBegin != nil {
		return fmt.Errorf("开始计费数据库事务：%w", errBegin)
	}
	if errApply := fn(tx); errApply != nil {
		_ = tx.Rollback()
		return errApply
	}
	if errCommit := tx.Commit(); errCommit != nil {
		return fmt.Errorf("提交计费数据库事务：%w", errCommit)
	}
	return nil
}

// Times are stored as Unix nanoseconds, with 0 reserved for "not set". The zero
// Time has to survive the round trip exactly: an inactive subscription cycle is
// recognized by comparing against the zero value.
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

// A cache price is absent rather than zero when it was never specified, which
// is what makes "explicitly free" expressible.
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
	amount := value.Float64
	return &amount
}

var _ billing.Repository = (*DB)(nil)

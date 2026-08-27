// Package sqlite persists the billing domain. It is the only package in the
// plugin that speaks SQL, and it depends on internal/billing rather than the
// other way round, so the domain stays free of storage concerns.
package sqlite

import (
	"database/sql"
	"fmt"
	"net/url"
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

const schemaVersion = 6

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
	dsn := (&url.URL{Scheme: "file", Path: path, OmitHost: true}).String() +
		"?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate"
	handle, errOpen := sql.Open(driverName, dsn)
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
	if version == 0 {
		if _, errSchema := d.db.Exec(schema); errSchema != nil {
			return fmt.Errorf("初始化计费数据库 %s：%w", d.path, errSchema)
		}
		return d.seed()
	}
	return d.transact(func(tx *sql.Tx) error {
		if _, errSchema := tx.Exec(schema); errSchema != nil {
			return fmt.Errorf("初始化计费数据库 %s：%w", d.path, errSchema)
		}
		if errMigrate := migrateBillingLog(tx); errMigrate != nil {
			return fmt.Errorf("迁移计费数据库 %s 的旧日志：%w", d.path, errMigrate)
		}
		if errMigrate := migrateConcurrencyLimit(tx); errMigrate != nil {
			return fmt.Errorf("迁移计费数据库 %s 的 API Key 并发上限：%w", d.path, errMigrate)
		}
		if errMigrate := migrateUsageLogExecutorType(tx); errMigrate != nil {
			return fmt.Errorf("迁移计费数据库 %s 的日志执行器字段：%w", d.path, errMigrate)
		}
		if version < schemaVersion {
			if _, errVersion := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); errVersion != nil {
				return fmt.Errorf("标记计费数据库 %s 的格式版本：%w", d.path, errVersion)
			}
		}
		return nil
	})
}

func migrateUsageLogExecutorType(tx *sql.Tx) error {
	columns, errColumns := tableColumns(tx, "usage_log")
	if errColumns != nil {
		return errColumns
	}
	if _, exists := columns["executor_type"]; exists {
		return nil
	}
	_, errAlter := tx.Exec("ALTER TABLE usage_log ADD COLUMN executor_type TEXT NOT NULL DEFAULT ''")
	return errAlter
}

func migrateConcurrencyLimit(tx *sql.Tx) error {
	columns, errColumns := tableColumns(tx, "api_keys")
	if errColumns != nil {
		return errColumns
	}
	if _, exists := columns["concurrency_limit"]; exists {
		return nil
	}
	_, errAlter := tx.Exec("ALTER TABLE api_keys ADD COLUMN concurrency_limit INTEGER NOT NULL DEFAULT 0")
	return errAlter
}

func migrateBillingLog(tx *sql.Tx) error {
	columns, errColumns := tableColumns(tx, "billing_log")
	if errColumns != nil {
		return errColumns
	}

	required := []string{
		"at", "scope", "auth_index", "upstream_model", "billing_model", "outcome",
		"accounting_quality", "price_source", "reasoning_tokens", "total_usd",
		"uncached_input_usd", "cache_read_usd", "cache_write_usd", "output_usd",
		"uncached_input_tokens", "cache_read_tokens", "cache_write_tokens", "billed_output_tokens",
		"tiered", "long_context", "threshold_input_tokens", "applied_input_per_1m",
		"applied_output_per_1m", "applied_cache_read_per_1m", "applied_cache_write_per_1m",
	}
	if len(columns) == 0 {
		return nil
	}
	for _, column := range required {
		if _, exists := columns[column]; !exists {
			return fmt.Errorf("旧 billing_log 缺少字段 %s", column)
		}
	}
	if _, errInsert := tx.Exec(`
			INSERT INTO usage_log (
				at, scope, auth_index, upstream_model, billing_model, failed, latency_ms, ttft_ms,
				accounting_quality, price_source, reasoning_tokens,
				total_usd, uncached_input_usd, cache_read_usd, cache_write_usd, output_usd,
				uncached_input_tokens, cache_read_tokens, cache_write_tokens, billed_output_tokens,
				tiered, long_context, threshold_input_tokens,
				applied_input_per_1m, applied_output_per_1m,
				applied_cache_read_per_1m, applied_cache_write_per_1m
			)
			SELECT
				at, scope, auth_index, upstream_model, billing_model,
				CASE WHEN lower(trim(outcome)) IN ('failed', 'canceled') THEN 1 ELSE 0 END, 0, 0,
				accounting_quality, price_source, reasoning_tokens,
				total_usd, uncached_input_usd, cache_read_usd, cache_write_usd, output_usd,
				uncached_input_tokens, cache_read_tokens, cache_write_tokens, billed_output_tokens,
				tiered, long_context, threshold_input_tokens,
				applied_input_per_1m, applied_output_per_1m,
				applied_cache_read_per_1m, applied_cache_write_per_1m
			FROM billing_log`); errInsert != nil {
		return errInsert
	}
	if _, errDrop := tx.Exec("DROP INDEX IF EXISTS billing_log_at; DROP INDEX IF EXISTS billing_log_scope; DROP TABLE IF EXISTS billing_log"); errDrop != nil {
		return errDrop
	}
	return nil
}

func tableColumns(tx *sql.Tx, table string) (map[string]struct{}, error) {
	rows, errQuery := tx.Query("PRAGMA table_info(" + table + ")")
	if errQuery != nil {
		return nil, errQuery
	}
	defer rows.Close()

	columns := make(map[string]struct{})
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if errScan := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); errScan != nil {
			return nil, errScan
		}
		columns[name] = struct{}{}
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, errRows
	}
	if errClose := rows.Close(); errClose != nil {
		return nil, errClose
	}
	return columns, nil
}

// SQLite gives WAL and shared-memory files the mode of the database, so the
// database must be restricted before the driver opens it.
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

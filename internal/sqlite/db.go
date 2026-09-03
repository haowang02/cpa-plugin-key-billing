// Package sqlite implements billing.Repository with SQLite.
package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"

	_ "github.com/mattn/go-sqlite3"
)

const schemaVersion = 11

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
	if version == 10 {
		return d.migrateV10ToV11()
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

// migrateV10ToV11 replaces the four model-access relation tables with the
// compact JSON route representation. Every step is one transaction: an
// incompatible legacy document leaves the version-10 database untouched.
func (d *DB) migrateV10ToV11() error {
	return d.transact(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`CREATE TABLE routes (
			position INTEGER PRIMARY KEY,
			id TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL DEFAULT '',
			rule_json TEXT NOT NULL DEFAULT '{}'
		)`); err != nil {
			return fmt.Errorf("创建路由规则表：%w", err)
		}
		if _, err := tx.Exec("ALTER TABLE api_keys ADD COLUMN route_bindings_json TEXT NOT NULL DEFAULT '[]'"); err != nil {
			return fmt.Errorf("添加 API Key 路由绑定：%w", err)
		}
		systemRule, _ := json.Marshal(billing.SystemAllRoute().Rule)
		if _, err := tx.Exec("INSERT INTO routes(position,id,name,rule_json) VALUES(0,?,?,?)", billing.SystemAllRouteID, billing.SystemAllRouteName, string(systemRule)); err != nil {
			return fmt.Errorf("创建默认路由规则：%w", err)
		}

		type legacyGroup struct {
			oldID, id, name string
			models          []string
		}
		groups := []legacyGroup{}
		usedRouteIDs := map[string]struct{}{billing.SystemAllRouteID: {}}
		rows, err := tx.Query("SELECT id,name FROM model_groups ORDER BY position")
		if err != nil {
			return fmt.Errorf("读取旧模型分组：%w", err)
		}
		for rows.Next() {
			var group legacyGroup
			if err := rows.Scan(&group.oldID, &group.name); err != nil {
				rows.Close()
				return fmt.Errorf("读取旧模型分组：%w", err)
			}
			group.id = strings.TrimSpace(group.oldID)
			if strings.HasPrefix(group.id, "system:") {
				base := "migrated-" + billing.CredentialFingerprint(group.id)[len("sha256:"):len("sha256:")+16]
				group.id = base
				for suffix := 2; ; suffix++ {
					if _, exists := usedRouteIDs[group.id]; !exists {
						break
					}
					group.id = fmt.Sprintf("%s-%d", base, suffix)
				}
			}
			if _, exists := usedRouteIDs[group.id]; exists {
				return fmt.Errorf("旧模型分组 ID %q 迁移后冲突", group.oldID)
			}
			usedRouteIDs[group.id] = struct{}{}
			groups = append(groups, group)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("读取旧模型分组：%w", err)
		}
		byOld := make(map[string]int, len(groups))
		for i := range groups {
			byOld[groups[i].oldID] = i
		}
		members, err := tx.Query("SELECT group_id,model FROM model_group_models ORDER BY group_id,position")
		if err != nil {
			return fmt.Errorf("读取旧模型分组成员：%w", err)
		}
		for members.Next() {
			var id, model string
			if err := members.Scan(&id, &model); err != nil {
				members.Close()
				return fmt.Errorf("读取旧模型分组成员：%w", err)
			}
			if i, ok := byOld[id]; ok {
				groups[i].models = append(groups[i].models, model)
			}
		}
		if err := members.Close(); err != nil {
			return err
		}
		if err := members.Err(); err != nil {
			return fmt.Errorf("读取旧模型分组成员：%w", err)
		}
		for i, group := range groups {
			route, err := billing.NormalizeStoredRoute(billing.Route{ID: group.id, Name: group.name, Rule: billing.RouteRule{Models: group.models}})
			if err != nil {
				return fmt.Errorf("校验旧模型分组 %s：%w", group.oldID, err)
			}
			groups[i].id = route.ID
			groups[i].models = route.Rule.Models
			raw, err := json.Marshal(route.Rule)
			if err != nil {
				return err
			}
			if _, err = tx.Exec("INSERT INTO routes(position,id,name,rule_json) VALUES(?,?,?,?)", i+1, route.ID, route.Name, string(raw)); err != nil {
				return fmt.Errorf("迁移模型分组 %s：%w", group.oldID, err)
			}
		}

		bindingsByScope := make(map[string][]billing.RouteBinding)
		groupRows, err := tx.Query("SELECT scope,group_id FROM key_model_groups ORDER BY scope,position")
		if err != nil {
			return fmt.Errorf("读取旧模型分组绑定：%w", err)
		}
		for groupRows.Next() {
			var scope, id string
			if err := groupRows.Scan(&scope, &id); err != nil {
				groupRows.Close()
				return fmt.Errorf("读取旧模型分组绑定：%w", err)
			}
			if i, ok := byOld[id]; ok && len(groups[i].models) > 0 {
				bindingsByScope[scope] = append(bindingsByScope[scope], billing.RouteBinding{Kind: billing.RouteBindingRoute, Value: groups[i].id})
			}
		}
		if err := groupRows.Close(); err != nil {
			return err
		}
		if err := groupRows.Err(); err != nil {
			return fmt.Errorf("读取旧模型分组绑定：%w", err)
		}

		modelRows, err := tx.Query("SELECT scope,model FROM key_allowed_models ORDER BY scope,position")
		if err != nil {
			return fmt.Errorf("读取旧模型绑定：%w", err)
		}
		for modelRows.Next() {
			var scope, model string
			if err := modelRows.Scan(&scope, &model); err != nil {
				modelRows.Close()
				return fmt.Errorf("读取旧模型绑定：%w", err)
			}
			bindingsByScope[scope] = append(bindingsByScope[scope], billing.RouteBinding{Kind: billing.RouteBindingModel, Value: model})
		}
		if err := modelRows.Close(); err != nil {
			return err
		}
		if err := modelRows.Err(); err != nil {
			return fmt.Errorf("读取旧模型绑定：%w", err)
		}

		for scope, bindings := range bindingsByScope {
			bindings, err = billing.NormalizeRouteBindings(bindings)
			if err != nil {
				return fmt.Errorf("校验 API Key %s 的迁移绑定：%w", scope, err)
			}
			raw, err := json.Marshal(bindings)
			if err != nil {
				return err
			}
			if _, err = tx.Exec("UPDATE api_keys SET route_bindings_json=? WHERE scope=?", string(raw), scope); err != nil {
				return fmt.Errorf("迁移 API Key %s：%w", scope, err)
			}
		}
		for _, table := range []string{"key_model_groups", "key_allowed_models", "model_group_models", "model_groups"} {
			if _, err := tx.Exec("DROP TABLE " + table); err != nil {
				return fmt.Errorf("删除旧表 %s：%w", table, err)
			}
		}
		if _, err := tx.Exec("PRAGMA user_version = 11"); err != nil {
			return fmt.Errorf("标记数据库格式版本：%w", err)
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

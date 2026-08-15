package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cpa-key-billing/internal/billing"
)

// jsonStateVersion is the format of the JSON document the plugin reads a new
// database from. Only this version was ever written, so a document claiming
// another one is not a billing state.
const jsonStateVersion = 6

type jsonState struct {
	Version     int                           `json:"version"`
	Prices      []billing.PriceRule           `json:"prices"`
	Plans       []billing.Plan                `json:"plans"`
	Keys        map[string]*billing.KeyState  `json:"keys"`
	Credentials map[string]billing.Credential `json:"credentials"`
	Log         []billing.LogEntry            `json:"log"`
}

// seed fills a database created for the first time from the JSON document
// beside it. A deployment that has one keeps its billing history when it moves
// to this database; afterwards the document is never read again.
//
// The schema version is stamped in the same transaction, so a database is only
// ever declared current together with the record it was seeded from: an import
// that fails, or a process killed halfway through one, leaves the version at 0
// and the next start reads the document again.
func (d *DB) seed() error {
	path := strings.TrimSuffix(d.path, filepath.Ext(d.path)) + ".json"
	document, errRead := readJSONState(path)
	if errRead != nil {
		return errRead
	}
	return d.transact(func(tx *sql.Tx) error {
		if document != nil {
			if errImport := importJSONState(tx, document); errImport != nil {
				return fmt.Errorf("导入状态文件 %s：%w", path, errImport)
			}
		}
		if _, errVersion := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); errVersion != nil {
			return fmt.Errorf("标记计费数据库 %s 的格式版本：%w", d.path, errVersion)
		}
		return nil
	})
}

// readJSONState answers nil for a document that is not there, which is the
// ordinary case of a deployment starting without a history to carry over.
func readJSONState(path string) (*jsonState, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取状态文件 %s：%w", path, errRead)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}

	var document jsonState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&document); errDecode != nil {
		return nil, fmt.Errorf("解析状态文件 %s：%w", path, errDecode)
	}
	// Anything after the first object means an interrupted writer left the
	// document half-rewritten. Seeding from the first object alone would drop
	// the rest silently, and a database is seeded exactly once.
	if decoder.More() {
		return nil, fmt.Errorf("解析状态文件 %s：文档包含多余内容", path)
	}
	if document.Version != jsonStateVersion {
		return nil, fmt.Errorf("状态文件 %s 的格式版本为 %d，当前插件只能读取版本 %d",
			path, document.Version, jsonStateVersion)
	}
	return &document, nil
}

// Entries older than the retention window are imported along with the rest and
// dropped by the load that follows, which is also what makes the count reported
// at startup the number the log will return.
func importJSONState(tx *sql.Tx, document *jsonState) error {
	state := billing.NewState()
	state.Prices = document.Prices
	state.Plans = document.Plans
	for scope, key := range document.Keys {
		if key == nil {
			continue
		}
		if key.ByModel == nil {
			key.ByModel = make(map[string]*billing.Totals)
		}
		state.Keys[scope] = key
	}
	for index, credential := range document.Credentials {
		state.Credentials[index] = credential
	}

	if errKeys := replaceKeys(tx, state); errKeys != nil {
		return errKeys
	}
	if errPlans := replacePlans(tx, state); errPlans != nil {
		return errPlans
	}
	if errPrices := replacePrices(tx, state); errPrices != nil {
		return errPrices
	}
	if errCredentials := replaceCredentials(tx, state); errCredentials != nil {
		return errCredentials
	}
	return appendLog(tx, billing.Changes{Log: document.Log})
}

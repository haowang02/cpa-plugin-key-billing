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

// importJSONState seeds a database created for the first time from the JSON
// document beside it. A deployment that has one keeps its billing history when
// it moves to this database; afterwards the document is never read again.
func (d *DB) importJSONState() error {
	path := strings.TrimSuffix(d.path, filepath.Ext(d.path)) + ".json"
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return nil
		}
		return fmt.Errorf("读取状态文件 %s：%w", path, errRead)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	var document jsonState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&document); errDecode != nil {
		return fmt.Errorf("解析状态文件 %s：%w", path, errDecode)
	}
	if document.Version != jsonStateVersion {
		return fmt.Errorf("状态文件 %s 的格式版本为 %d，当前插件只能读取版本 %d",
			path, document.Version, jsonStateVersion)
	}

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

	errImport := d.transact(func(tx *sql.Tx) error {
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
	})
	if errImport != nil {
		return fmt.Errorf("导入状态文件 %s：%w", path, errImport)
	}
	return nil
}

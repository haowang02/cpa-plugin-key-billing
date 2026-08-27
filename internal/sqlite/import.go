package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

const jsonStateVersion = 6

type jsonState struct {
	Version     int                           `json:"version"`
	Prices      []billing.PriceRule           `json:"prices"`
	Plans       []billing.Plan                `json:"plans"`
	Keys        map[string]*billing.KeyState  `json:"keys"`
	Credentials map[string]billing.Credential `json:"credentials"`
	Log         []jsonLogEntry                `json:"log"`
}

type jsonLogEntry struct {
	At                time.Time                      `json:"at"`
	Scope             string                         `json:"scope"`
	RequestID         string                         `json:"request_id,omitempty"`
	Endpoint          string                         `json:"endpoint,omitempty"`
	AuthIndex         string                         `json:"auth_index,omitempty"`
	UpstreamModel     string                         `json:"upstream_model,omitempty"`
	BillingModel      string                         `json:"billing_model,omitempty"`
	Outcome           string                         `json:"outcome,omitempty"`
	Failed            bool                           `json:"failed,omitempty"`
	AccountingQuality billing.TokenAccountingQuality `json:"accounting_quality,omitempty"`
	PriceSource       billing.PriceSource            `json:"price_source,omitempty"`
	Cost              billing.Cost                   `json:"cost"`
	ReasoningTokens   int64                          `json:"reasoning_tokens,omitempty"`
}

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
	if errTrailing := decoder.Decode(&struct{}{}); errTrailing != io.EOF {
		return nil, fmt.Errorf("解析状态文件 %s：文档包含多余内容", path)
	}
	if document.Version != jsonStateVersion {
		return nil, fmt.Errorf("状态文件 %s 的格式版本为 %d，当前插件只能读取版本 %d",
			path, document.Version, jsonStateVersion)
	}
	return &document, nil
}

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
	entries := make([]billing.LogEntry, 0, len(document.Log))
	for _, entry := range document.Log {
		entries = append(entries, entry.usageEntry())
	}
	return appendLog(tx, billing.Changes{Log: entries})
}

func (e jsonLogEntry) usageEntry() billing.LogEntry {
	return billing.LogEntry{
		At:                e.At,
		Scope:             e.Scope,
		AuthIndex:         e.AuthIndex,
		UpstreamModel:     e.UpstreamModel,
		BillingModel:      e.BillingModel,
		Failed:            e.Failed || strings.EqualFold(e.Outcome, "failed") || strings.EqualFold(e.Outcome, "canceled"),
		AccountingQuality: e.AccountingQuality,
		PriceSource:       e.PriceSource,
		Cost:              e.Cost,
		ReasoningTokens:   e.ReasoningTokens,
	}
}

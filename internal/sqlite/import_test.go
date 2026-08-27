package sqlite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func writeJSONState(t *testing.T, path string, document any) {
	t.Helper()
	raw, errMarshal := json.Marshal(document)
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
}

func TestOpenImportsJSONStateOnce(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 12, 9, 30, 0, 0, time.UTC)
	documentPath := filepath.Join(dir, "state.json")
	writeJSONState(t, documentPath, map[string]any{
		"version": jsonStateVersion,
		"prices":  []billing.PriceRule{{Pattern: "gpt-5.5", InputPer1M: 1, OutputPer1M: 2}},
		"plans":   []billing.Plan{{ID: "weekly", Name: "Weekly", AmountUSD: 10, Period: billing.Period{Kind: billing.PeriodWeekly}}},
		"keys": map[string]*billing.KeyState{
			"scope-a": {Preview: "sk-tes…0001", Label: "Alice", InConfig: true, PlanID: "weekly", Lifetime: billing.Totals{CostUSD: 1.5, Requests: 3}},
		},
		"credentials": map[string]billing.Credential{"auth-1": {Provider: "codex", Account: "ops@example.com"}},
		"log": []map[string]any{
			{
				"at": at, "scope": "scope-a", "request_id": "req-1", "endpoint": "/v1/responses",
				"auth_index": "auth-1", "upstream_model": "gpt-5.5", "billing_model": "gpt-5.5",
				"accounting_quality": billing.TokenAccountingComplete, "price_source": billing.PriceSourceOverride,
				"cost": billing.Cost{TotalUSD: 0.5, UncachedInputTokens: 100, BilledOutputTokens: 20},
			},
			{"at": at, "scope": "scope-a", "outcome": "failed", "cost": billing.Cost{}},
		},
	})

	path := filepath.Join(dir, "state.db")
	snapshot := mustLoad(t, openDatabase(t, path))
	if key := snapshot.State.Keys["scope-a"]; key == nil || key.Label != "Alice" || key.Lifetime.CostUSD != 1.5 {
		t.Fatalf("key = %+v", key)
	}
	if len(snapshot.State.Plans) != 1 || len(snapshot.State.Prices) != 1 || snapshot.LogEntries != 2 {
		t.Fatalf("snapshot = %+v, log entries = %d", snapshot.State, snapshot.LogEntries)
	}

	writeJSONState(t, documentPath, map[string]any{
		"version": jsonStateVersion,
		"keys":    map[string]*billing.KeyState{"scope-b": {Label: "Bob"}},
	})
	if keys := mustLoad(t, openDatabase(t, path)).State.Keys; keys["scope-a"] == nil || keys["scope-b"] != nil {
		t.Fatalf("keys = %+v", keys)
	}
}

func TestOpenRejectsInvalidJSONState(t *testing.T) {
	for name, raw := range map[string]string{
		"malformed": "{not json",
		"version":   `{"version":999}`,
		"trailing":  `{"version":6}{"version":6}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if errWrite := os.WriteFile(filepath.Join(dir, "state.json"), []byte(raw), 0o600); errWrite != nil {
				t.Fatalf("write: %v", errWrite)
			}
			if _, errOpen := Open(filepath.Join(dir, "state.db")); errOpen == nil {
				t.Fatal("Open accepted invalid JSON state")
			}
		})
	}
}

func TestJSONImportClassifiesCanceledUsageAsFailed(t *testing.T) {
	entry := (jsonLogEntry{
		Outcome: "canceled",
	}).usageEntry()
	if !entry.Failed {
		t.Fatalf("entry = %+v", entry)
	}
}

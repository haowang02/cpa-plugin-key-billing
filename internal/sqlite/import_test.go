package sqlite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func writeJSONState(t *testing.T, path string, document map[string]any) {
	t.Helper()
	raw, errMarshal := json.Marshal(document)
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
}

// Upgrading a running deployment must not lose what it has already billed.
func TestOpenImportsTheDocumentItReplaces(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 12, 9, 30, 0, 0, time.UTC)
	writeJSONState(t, filepath.Join(dir, "state.json"), map[string]any{
		"version": jsonStateVersion,
		"prices":  []billing.PriceRule{{Pattern: "gpt-5.5", InputPer1M: 1, OutputPer1M: 2}},
		"plans":   []billing.Plan{{ID: "weekly", Name: "Weekly", AmountUSD: 10, Period: billing.Period{Kind: billing.PeriodWeekly}}},
		"keys": map[string]*billing.KeyState{
			"scope-a": {Preview: "sk-tes…0001", Label: "Alice", InConfig: true, PlanID: "weekly",
				Cycle:    billing.Cycle{PlanID: "weekly", StartAt: at, EndAt: at.Add(7 * 24 * time.Hour), SpentUSD: 1.5},
				Lifetime: billing.Totals{CostUSD: 1.5, Requests: 3}},
		},
		"credentials": map[string]billing.Credential{
			"auth-1": {Provider: "codex", Account: "ops@example.com"},
		},
		"log": []billing.LogEntry{{At: at, Scope: "scope-a", BillingModel: "gpt-5.5"}},
	})

	path := filepath.Join(dir, "state.db")
	snapshot := mustLoad(t, openDatabase(t, path))
	key := snapshot.State.Keys["scope-a"]
	if key == nil || key.Label != "Alice" || key.Cycle.SpentUSD != 1.5 || !key.Cycle.EndAt.Equal(at.Add(7*24*time.Hour)) {
		t.Fatalf("key = %+v", key)
	}
	if len(snapshot.State.Plans) != 1 || len(snapshot.State.Prices) != 1 || snapshot.LogEntries != 1 {
		t.Fatalf("snapshot = %+v", snapshot.State)
	}

	// The import happens once. A second open reads the database it produced,
	// and further changes are not undone by the document still lying there.
	snapshot.State.Keys["scope-a"].Label = "Alice Cooper"
	second := openDatabase(t, path)
	mustSave(t, second, snapshot.State, billing.Changes{Keys: []string{"scope-a"}})
	if errClose := second.Close(); errClose != nil {
		t.Fatalf("Close error = %v", errClose)
	}
	if reopened := mustLoad(t, openDatabase(t, path)); reopened.State.Keys["scope-a"].Label != "Alice Cooper" {
		t.Fatalf("key = %+v, want the database to have kept the change", reopened.State.Keys["scope-a"])
	}
}

// A document that cannot be read is not the same as no document: starting empty
// there would silently discard a real billing record.
func TestOpenRefusesAnUnreadableDocument(t *testing.T) {
	dir := t.TempDir()
	if errWrite := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{not json"), 0o600); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	if _, errOpen := Open(filepath.Join(dir, "state.db")); errOpen == nil {
		t.Fatal("Open accepted a corrupt document, want an error rather than silent data loss")
	}
}

func TestOpenRefusesAnUnsupportedDocumentVersion(t *testing.T) {
	dir := t.TempDir()
	writeJSONState(t, filepath.Join(dir, "state.json"), map[string]any{"version": jsonStateVersion + 1})
	if _, errOpen := Open(filepath.Join(dir, "state.db")); errOpen == nil {
		t.Fatal("Open accepted a document from an unknown format version")
	}
}

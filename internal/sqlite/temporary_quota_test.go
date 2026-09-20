package sqlite

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func TestTemporaryQuotaRoundTripAndLegacyCycles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)
	now := time.Now().UTC().Truncate(time.Second)
	state := billing.NewState()
	state.Plans = []billing.Plan{{ID: "p", Windows: []billing.QuotaWindow{{ID: "w", Name: "周限", PeriodSeconds: 604800, AmountUSD: 600, TokenLimit: 1000, RequestLimit: 100}}}}
	cycle := billing.QuotaCycle{PlanID: "p", StartAt: now, EndAt: now.Add(7 * 24 * time.Hour), SpentUSD: 107.18,
		TemporaryQuota: billing.TemporaryQuota{AmountUSD: 100, TokenLimit: 2000, RequestLimit: 300}}
	state.Keys["s"] = &billing.KeyState{Preview: "sk-tes…0001", PlanID: "p", Cycles: map[string]billing.QuotaCycle{"w": cycle}}
	mustSave(t, database, state, billing.Changes{Plans: true, AllKeys: true})
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = openDatabase(t, path)
	loaded := mustLoad(t, database).State.Keys["s"].Cycles["w"]
	if !reflect.DeepEqual(cycle, loaded) {
		t.Fatalf("restart lost credit or accounting: %+v", loaded)
	}
	// Existing cycles lack the new JSON member; no schema migration is needed.
	var raw map[string]map[string]json.RawMessage
	data, err := json.Marshal(state.Keys["s"].Cycles)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw["w"], "temporary_quota")
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec("UPDATE api_keys SET cycles_json = ? WHERE scope = 's'", string(data)); err != nil {
		t.Fatal(err)
	}
	loaded = mustLoad(t, database).State.Keys["s"].Cycles["w"]
	if loaded.TemporaryQuota != (billing.TemporaryQuota{}) || loaded.SpentUSD != 107.18 {
		t.Fatalf("legacy cycle changed: %+v", loaded)
	}
	// Corrupt extra limits must fail to load rather than wrap into allowance.
	raw["w"]["temporary_quota"] = json.RawMessage(`{"amount_usd":-1}`)
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec("UPDATE api_keys SET cycles_json = ? WHERE scope = 's'", string(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Load(time.Time{}, time.Time{}); err == nil {
		t.Fatal("invalid persisted credit was accepted")
	}
}

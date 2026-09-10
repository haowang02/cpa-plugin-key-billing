package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func TestSavingKeysNeverDeletesExistingRows(t *testing.T) {
	database := openTestDB(t)
	state := billing.NewState()
	state.Keys["active"] = &billing.KeyState{Preview: "sk-tes…0001", InConfig: true}
	state.Keys["deleted"] = &billing.KeyState{Preview: "sk-tes…0002", DeletedAt: time.Unix(100, 0)}
	mustSave(t, database, state, billing.Changes{AllKeys: true})
	if _, err := database.db.Exec(`CREATE TRIGGER reject_key_deletion BEFORE DELETE ON api_keys
        BEGIN SELECT RAISE(ABORT, 'API Key records must be retained'); END`); err != nil {
		t.Fatal(err)
	}
	state.Keys["invalid"] = &billing.KeyState{}
	if err := database.Save(state, billing.Changes{AllKeys: true}); err == nil {
		t.Fatal("accepted a key without a preview")
	}
	delete(state.Keys, "invalid")
	delete(state.Keys, "deleted")
	state.Keys["active"].Label = "Updated"
	mustSave(t, database, state, billing.Changes{AllKeys: true})
	if err := database.Save(state, billing.Changes{Keys: []string{"deleted"}}); err == nil {
		t.Fatal("a missing key was accepted as a deletion")
	}
	mustSave(t, database, billing.NewState(), billing.Changes{AllKeys: true})
	keys := mustLoad(t, database).State.Keys
	if len(keys) != 2 || keys["active"].Label != "Updated" || keys["deleted"].Preview != "sk-tes…0002" || keys["deleted"].DeletedAt.IsZero() {
		t.Fatalf("saving keys deleted or replaced existing records: %+v", keys)
	}
}

func TestOldKeyPreviewRepairPreservesHistoryAndCanBeResolved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)
	defer database.Close()
	const apiKey = "sk-dummy-legacy-0001"
	scope := billing.CallerScope(apiKey)
	state := billing.NewState()
	state.Plans = []billing.Plan{{ID: "plan", Windows: []billing.QuotaWindow{{ID: "default", Name: "额度", AmountUSD: 10, PeriodSeconds: 3600}}}}
	state.Keys[scope] = &billing.KeyState{
		Preview: billing.PreviewKey(apiKey), Label: "Legacy", PlanID: "plan",
		ConcurrencyLimit: 3, DeletedAt: time.Unix(100, 0), Cycles: map[string]billing.QuotaCycle{"default": {PlanID: "plan", StartAt: time.Unix(1, 0), EndAt: time.Unix(3601, 0), SpentUSD: 2}},
		RouteBindings: billing.RouteBindings{RouteRule: billing.RouteRule{Models: []string{"gpt-5.5"}}},
	}
	mustSave(t, database, state, billing.Changes{AllKeys: true, Plans: true,
		NormalRequestEvents: []billing.RequestEvent{requestEvent(scope, time.Now())},
	})
	if _, err := database.db.Exec("UPDATE api_keys SET preview = '' WHERE scope = ?", scope); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		key := mustLoad(t, database).State.Keys[scope]
		if key == nil || key.Preview != billing.UnknownKeyPreview || key.Label != "Legacy" ||
			key.PlanID != "plan" || key.ConcurrencyLimit != 3 || key.Cycles["default"].SpentUSD != 2 ||
			!key.DeletedAt.Equal(time.Unix(100, 0)) || len(key.RouteBindings.Models) != 1 {
			t.Fatalf("preview repair changed key state: %+v", key)
		}
	}
	var preview string
	if err := database.db.QueryRow("SELECT preview FROM api_keys WHERE scope = ?", scope).Scan(&preview); err != nil || preview != billing.UnknownKeyPreview {
		t.Fatalf("repair was not persisted: %q, %v", preview, err)
	}
	store := billing.NewStore(func(path string) (billing.Repository, error) { return Open(path) }, nil)
	defer store.Close()
	cfg := billing.DefaultConfig()
	cfg.StateFile = path
	if err := store.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	store.RecordUsage(billing.UsageEvent{Scope: scope, KeyPreview: billing.PreviewKey(apiKey), UpstreamModel: "gpt-5.5"})
	key := mustLoad(t, database).State.Keys[scope]
	if key.Preview != billing.PreviewKey(apiKey) || key.DeletedAt.IsZero() {
		t.Fatalf("usage did not resolve the mask or resurrected the key: %+v", key)
	}
	view, err := store.RequestEvents(billing.RequestEventQuery{})
	if err != nil || len(view.Entries) != 2 || view.Entries[0].Preview != billing.PreviewKey(apiKey) || view.Entries[1].Preview != billing.PreviewKey(apiKey) {
		t.Fatalf("historical identity was lost: %+v, %v", view, err)
	}
}

package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func TestV14MigrationPreservesKeyCyclesAndHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v14.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := schema
	for _, column := range []string{" billing_since INTEGER NOT NULL DEFAULT 0,", " started_at INTEGER NOT NULL DEFAULT 0,", " cycle_scope TEXT NOT NULL DEFAULT '',"} {
		oldSchema = strings.ReplaceAll(oldSchema, column, "")
	}
	if _, err = raw.Exec(oldSchema + "PRAGMA user_version=14;"); err != nil {
		t.Fatal(err)
	}
	windows := `[{"id":"w","name":"Budget","amount_usd":10,"period_seconds":3600}]`
	cycles := `{"w":{"plan_id":"p","start_at":"2026-09-10T12:00:00Z","end_at":"2026-09-10T13:00:00Z","spent_usd":2}}`
	if _, err = raw.Exec("INSERT INTO plans(position,id,windows_json) VALUES(0,'p',?)", windows); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("INSERT INTO api_keys(scope,preview,plan_id,cycles_json) VALUES('a','masked','p',?)", cycles); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("INSERT INTO request_events(at,scope,failed) VALUES(1,'a',0),(2,'a',1)"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db := openDatabase(t, path)
	state := mustLoad(t, db).State
	plan := state.Plans[0]
	if plan.SharedCycles() || !plan.StartedAt.IsZero() || state.Keys["a"].Cycles["w"].SpentUSD != 2 {
		t.Fatal("legacy billing changed")
	}
	var stored string
	var count int
	if err = db.db.QueryRow("SELECT cycles_json FROM api_keys").Scan(&stored); err != nil || stored != cycles {
		t.Fatal("cycle JSON modified", err)
	}
	if err = db.db.QueryRow("SELECT count(*) FROM request_events").Scan(&count); err != nil || count != 2 {
		t.Fatal("history lost", err)
	}
}

func TestSharedClockAndResetBarrierSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	state := billing.NewState()
	state.Plans = []billing.Plan{{ID: "p", CycleScope: billing.CycleScopePlan, StartedAt: now, Windows: []billing.QuotaWindow{{ID: "w", Name: "Budget", AmountUSD: 10, PeriodSeconds: 3600}}}}
	state.Keys["a"] = &billing.KeyState{Preview: "masked", PlanID: "p", BillingSince: now.Add(-time.Minute), Cycles: map[string]billing.QuotaCycle{"w": {PlanID: "p", StartAt: now, EndAt: now.Add(time.Hour), SpentUSD: 2}}}
	db := openDatabase(t, path)
	mustSave(t, db, state, billing.Changes{Plans: true, AllKeys: true})
	db.Close()
	reopened := openDatabase(t, path)
	loaded := mustLoad(t, reopened).State
	if !loaded.Plans[0].SharedCycles() || !loaded.Plans[0].StartedAt.Equal(now) || !loaded.Keys["a"].BillingSince.Equal(now.Add(-time.Minute)) || loaded.Keys["a"].Cycles["w"].SpentUSD != 2 {
		t.Fatal("shared billing state lost")
	}
	loaded.Plans[0].StartedAt = time.Time{}
	loaded.Keys["a"].BillingSince = now.Add(time.Minute)
	loaded.Keys["a"].Cycles = nil
	mustSave(t, reopened, loaded, billing.Changes{Plans: true, AllKeys: true})
	reopened.Close()
	again := mustLoad(t, openDatabase(t, path)).State
	if !again.Plans[0].StartedAt.IsZero() || !again.Keys["a"].BillingSince.Equal(now.Add(time.Minute)) || len(again.Keys["a"].Cycles) != 0 {
		t.Fatal("reset state lost")
	}
}

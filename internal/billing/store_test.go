package billing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	return cfg
}

func TestStoreConfigureCreatesFreshStateWhenFileMissing(t *testing.T) {
	store := NewStore()
	cfg := testConfig(t)
	if errConfigure := store.Configure(cfg); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	defer store.Close()

	if !store.Enabled() {
		t.Fatal("Enabled = false, want true")
	}
	if _, errStat := os.Stat(cfg.StateFile); !os.IsNotExist(errStat) {
		t.Fatal("Configure wrote a state file for a clean document, want no write until there are changes")
	}
	store.Read(func(state *State) {
		if len(state.Keys) != 0 || len(state.Plans) != 0 || len(state.Prices) != 0 {
			t.Fatalf("fresh state is not empty: %+v", state)
		}
	})
}

func TestStoreFlushPersistsAndReloads(t *testing.T) {
	cfg := testConfig(t)
	cycleStart := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	store := NewStore()
	if errConfigure := store.Configure(cfg); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	store.Update(func(state *State) {
		state.Plans = append(state.Plans,
			Plan{ID: "monthly-20", Name: "Monthly 20 USD", AmountUSD: 20, Period: Period{Kind: PeriodMonthly}},
			Plan{ID: "legacy-zero", Name: "Legacy Unlimited", Period: Period{Kind: PeriodDaily}},
		)
		state.Keys["scope-a"] = &KeyState{
			Scope:    "scope-a",
			Preview:  "sk-tes…0001",
			PlanID:   "monthly-20",
			Cycle:    Cycle{SpentUSD: 1.5, Requests: 3},
			Lifetime: Totals{CostUSD: 1.5, Requests: 3, UncachedInputTokens: 100},
		}
		state.Keys["scope-legacy"] = &KeyState{
			Scope:    "scope-legacy",
			PlanID:   "legacy-zero",
			Cycle:    Cycle{PlanID: "legacy-zero", StartAt: cycleStart, EndAt: cycleStart.Add(time.Hour), SpentUSD: 2, Requests: 1},
			Lifetime: Totals{CostUSD: 2, Requests: 1},
		}
	})
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush error = %v", errFlush)
	}
	store.Close()

	reloaded := NewStore()
	if errConfigure := reloaded.Configure(cfg); errConfigure != nil {
		t.Fatalf("Configure (reload) error = %v", errConfigure)
	}
	defer reloaded.Close()

	reloaded.Read(func(state *State) {
		if len(state.Plans) != 1 || state.Plans[0].ID != "monthly-20" || state.Plans[0].AmountUSD != 20 {
			t.Fatalf("plans not restored: %+v", state.Plans)
		}
		key := state.Keys["scope-a"]
		if key == nil {
			t.Fatal("key scope-a not restored")
		}
		if key.Cycle.SpentUSD != 1.5 || key.Lifetime.Requests != 3 {
			t.Fatalf("key totals not restored: %+v", key)
		}
		if key.ByModel == nil {
			t.Fatal("ByModel is nil after reload, normalize must populate it")
		}
		legacy := state.Keys["scope-legacy"]
		if legacy == nil || legacy.PlanID != "" || !legacy.Cycle.StartAt.IsZero() || legacy.Lifetime.CostUSD != 2 {
			t.Fatalf("legacy zero plan was not migrated to an unbound key: %+v", legacy)
		}
		if len(legacy.RecentCycles) != 1 || legacy.RecentCycles[0].SpentUSD != 2 {
			t.Fatalf("legacy cycle was not preserved: %+v", legacy.RecentCycles)
		}
	})
}

func TestStoreFlushLeavesNoTempFiles(t *testing.T) {
	cfg := testConfig(t)
	store := NewStore()
	if errConfigure := store.Configure(cfg); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	defer store.Close()

	store.Update(func(state *State) { state.Keys["scope-a"] = &KeyState{Scope: "scope-a"} })
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush error = %v", errFlush)
	}
	entries, errRead := os.ReadDir(filepath.Dir(cfg.StateFile))
	if errRead != nil {
		t.Fatalf("read state dir: %v", errRead)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(cfg.StateFile) {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("state directory = %v, want only the state file", names)
	}
}

func TestStoreCreatesMissingParentDirectory(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.StateFile = filepath.Join(t.TempDir(), "nested", "deeper", "state.json")

	store := NewStore()
	if errConfigure := store.Configure(cfg); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	defer store.Close()

	store.Update(func(state *State) { state.Keys["scope-a"] = &KeyState{Scope: "scope-a"} })
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush error = %v", errFlush)
	}
	if _, errStat := os.Stat(cfg.StateFile); errStat != nil {
		t.Fatalf("state file not written into a fresh directory tree: %v", errStat)
	}
}

func TestStoreReconfigureToNewPathFlushesOldAndLoadsNew(t *testing.T) {
	dir := t.TempDir()
	first := DefaultConfig()
	first.Enabled = true
	first.StateFile = filepath.Join(dir, "first.json")

	store := NewStore()
	if errConfigure := store.Configure(first); errConfigure != nil {
		t.Fatalf("Configure(first) error = %v", errConfigure)
	}
	defer store.Close()
	store.Update(func(state *State) { state.Keys["scope-a"] = &KeyState{Scope: "scope-a", Lifetime: Totals{CostUSD: 7}} })

	second := first
	second.StateFile = filepath.Join(dir, "second.json")
	if errConfigure := store.Configure(second); errConfigure != nil {
		t.Fatalf("Configure(second) error = %v", errConfigure)
	}

	// The pending spend must have landed in the old file rather than moving
	// to the new one or being dropped.
	raw, errRead := os.ReadFile(first.StateFile)
	if errRead != nil {
		t.Fatalf("old state file not written: %v", errRead)
	}
	var persisted State
	if errUnmarshal := json.Unmarshal(raw, &persisted); errUnmarshal != nil {
		t.Fatalf("decode old state: %v", errUnmarshal)
	}
	if persisted.Keys["scope-a"] == nil || persisted.Keys["scope-a"].Lifetime.CostUSD != 7 {
		t.Fatalf("old state did not capture pending changes: %+v", persisted.Keys)
	}
	store.Read(func(state *State) {
		if len(state.Keys) != 0 {
			t.Fatalf("new state file should start empty, got %+v", state.Keys)
		}
	})
}

func TestStoreRejectsNewerStateVersion(t *testing.T) {
	cfg := testConfig(t)
	raw, errMarshal := json.Marshal(map[string]any{"version": StateVersion + 1})
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	if errWrite := os.WriteFile(cfg.StateFile, raw, 0o600); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	if errConfigure := NewStore().Configure(cfg); errConfigure == nil {
		t.Fatal("Configure accepted a state file from a newer plugin, want an error")
	}
}

func TestStoreMigratesCalendarCyclesToInactiveKeyCycles(t *testing.T) {
	cfg := testConfig(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	legacy := &State{
		Version: 2,
		Plans:   []Plan{{ID: "p", AmountUSD: 10, Period: Period{Kind: PeriodDaily}}},
		Keys: map[string]*KeyState{
			"scope-a": {
				Scope: "scope-a", PlanID: "p",
				Cycle:    Cycle{PlanID: "p", StartAt: start, EndAt: start.Add(24 * time.Hour), SpentUSD: 3, Requests: 2},
				Lifetime: Totals{CostUSD: 3, Requests: 2},
			},
		},
	}
	raw, errMarshal := json.Marshal(legacy)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errWrite := os.WriteFile(cfg.StateFile, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	store := NewStore()
	if errConfigure := store.Configure(cfg); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	t.Cleanup(store.Close)
	store.Read(func(state *State) {
		key := state.Keys["scope-a"]
		if state.Version != StateVersion || key == nil || key.PlanID != "p" || key.Cycle != (Cycle{}) || key.Lifetime.CostUSD != 3 {
			t.Fatalf("migrated state = %+v", state)
		}
		if len(key.RecentCycles) != 1 || key.RecentCycles[0].SpentUSD != 3 {
			t.Fatalf("recent cycles = %+v", key.RecentCycles)
		}
	})
}

func TestStoreRejectsCorruptStateFile(t *testing.T) {
	cfg := testConfig(t)
	if errWrite := os.WriteFile(cfg.StateFile, []byte("{not json"), 0o600); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	if errConfigure := NewStore().Configure(cfg); errConfigure == nil {
		t.Fatal("Configure accepted a corrupt state file, want an error rather than silent data loss")
	}
}

func TestStoreFlushIfDueDebouncesWrites(t *testing.T) {
	cfg := testConfig(t)
	clock := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := NewStore()
	store.now = func() time.Time { return clock }
	if errConfigure := store.Configure(cfg); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	t.Cleanup(store.Close)

	store.FlushIfDue()
	if _, errStat := os.Stat(cfg.StateFile); !os.IsNotExist(errStat) {
		t.Fatalf("a clean store wrote its document: %v", errStat)
	}

	store.Update(func(state *State) { state.Keys["scope-a"] = &KeyState{Scope: "scope-a", Lifetime: Totals{CostUSD: 1}} })
	store.FlushIfDue()
	if _, errStat := os.Stat(cfg.StateFile); errStat != nil {
		t.Fatalf("the first due flush did not write: %v", errStat)
	}

	clock = clock.Add(DefaultFlushInterval - time.Second)
	store.Update(func(state *State) { state.Keys["scope-a"].Lifetime.CostUSD = 2 })
	store.FlushIfDue()
	if !store.Status("p", "v").PendingWrite {
		t.Fatal("a write inside the debounce window was not held back")
	}

	clock = clock.Add(2 * time.Second)
	store.FlushIfDue()
	if store.Status("p", "v").PendingWrite {
		t.Fatal("the held-back write never landed")
	}
	raw, errRead := os.ReadFile(cfg.StateFile)
	if errRead != nil {
		t.Fatalf("read: %v", errRead)
	}
	var persisted State
	if errUnmarshal := json.Unmarshal(raw, &persisted); errUnmarshal != nil {
		t.Fatalf("decode: %v", errUnmarshal)
	}
	if persisted.Keys["scope-a"].Lifetime.CostUSD != 2 {
		t.Fatalf("persisted CostUSD = %v, want 2", persisted.Keys["scope-a"].Lifetime.CostUSD)
	}
}

func TestStoreKeepsPersistingAfterARejectedReconfigure(t *testing.T) {
	cfg := testConfig(t)
	store := NewStore()
	if errConfigure := store.Configure(cfg); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	t.Cleanup(store.Close)

	corrupt := cfg
	corrupt.StateFile = filepath.Join(t.TempDir(), "corrupt.json")
	if errWrite := os.WriteFile(corrupt.StateFile, []byte("{not json"), 0o600); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	if errConfigure := store.Configure(corrupt); errConfigure == nil {
		t.Fatal("Configure accepted a corrupt state file, want an error")
	}
	if got := store.path; got != cfg.StateFile {
		t.Fatalf("Path = %q, want the original %q to still be live", got, cfg.StateFile)
	}

	store.Update(func(state *State) { state.Keys["scope-a"] = &KeyState{Scope: "scope-a"} })
	store.FlushIfDue()
	if _, errStat := os.Stat(cfg.StateFile); errStat != nil {
		t.Fatalf("the rejected reconfigure stranded the original state file: %v", errStat)
	}
}

func TestStoreCloseFlushesPendingChanges(t *testing.T) {
	cfg := testConfig(t)
	store := NewStore()
	if errConfigure := store.Configure(cfg); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	store.Update(func(state *State) { state.Keys["scope-a"] = &KeyState{Scope: "scope-a", Lifetime: Totals{CostUSD: 3}} })
	store.Close()

	raw, errRead := os.ReadFile(cfg.StateFile)
	if errRead != nil {
		t.Fatalf("Close did not persist state: %v", errRead)
	}
	var persisted State
	if errUnmarshal := json.Unmarshal(raw, &persisted); errUnmarshal != nil {
		t.Fatalf("decode: %v", errUnmarshal)
	}
	if persisted.Keys["scope-a"] == nil || persisted.Keys["scope-a"].Lifetime.CostUSD != 3 {
		t.Fatalf("Close lost pending changes: %+v", persisted.Keys)
	}
}

func TestStoreConfigureFailsOnUnreadableStatePath(t *testing.T) {
	dir := t.TempDir()
	// A regular file where a directory is expected. Reading through it yields
	// ENOTDIR, which is not "missing file" and must not be mistaken for a
	// clean first run: starting empty there would silently discard the real
	// document once someone fixes the path.
	blocker := filepath.Join(dir, "blocker")
	if errWrite := os.WriteFile(blocker, []byte("x"), 0o600); errWrite != nil {
		t.Fatalf("write blocker: %v", errWrite)
	}
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.StateFile = filepath.Join(blocker, "state.json")

	if errConfigure := NewStore().Configure(cfg); errConfigure == nil {
		t.Fatal("Configure accepted an unreadable state path, want an error")
	}
}

func TestStoreFlushKeepsDocumentDirtyOnWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permissions")
	}
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.StateFile = filepath.Join(dir, "state.json")

	store := NewStore()
	if errConfigure := store.Configure(cfg); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	defer store.Close()

	// Revoke write permission after configuring so the temp-file creation in
	// writeFileAtomic fails.
	if errChmod := os.Chmod(dir, 0o500); errChmod != nil {
		t.Fatalf("chmod: %v", errChmod)
	}
	t.Cleanup(func() {
		if errChmod := os.Chmod(dir, 0o700); errChmod != nil {
			t.Fatalf("restore chmod: %v", errChmod)
		}
	})

	store.Update(func(state *State) { state.Keys["scope-a"] = &KeyState{Scope: "scope-a"} })
	if errFlush := store.Flush(); errFlush == nil {
		t.Fatal("Flush succeeded against an unwritable directory, want an error")
	}
	// Spend must not be dropped because the disk write failed; the next tick
	// has to retry it.
	if status := store.Status("p", "v"); !status.PendingWrite || status.LastError == "" {
		t.Fatalf("PendingWrite = %v, LastError = %q, want a retained dirty flag and a recorded error", status.PendingWrite, status.LastError)
	}
}

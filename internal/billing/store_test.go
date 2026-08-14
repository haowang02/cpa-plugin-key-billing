package billing

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestConfigureLoadsTheDocumentBehindTheNewPath(t *testing.T) {
	first := &memoryRepository{state: NewState()}
	first.state.Plans = []Plan{{ID: "monthly-20", AmountUSD: 20, Period: Period{Kind: PeriodMonthly}}}
	second := &memoryRepository{state: NewState()}

	opened := 0
	store := NewStore(func(string) (Repository, error) {
		opened++
		if opened == 1 {
			return first, nil
		}
		return second, nil
	})
	t.Cleanup(store.Close)

	cfg := testConfig(t)
	if errConfigure := store.Configure(cfg); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	store.Read(func(state *State) {
		if len(state.Plans) != 1 {
			t.Fatalf("plans = %+v, want the ones behind the configured path", state.Plans)
		}
	})

	// Reconfiguring to the same path must not reopen the database, or every
	// plugin.reconfigure would drop the working set and reload it.
	if errConfigure := store.Configure(cfg); errConfigure != nil {
		t.Fatalf("Configure (same path) error = %v", errConfigure)
	}
	if opened != 1 {
		t.Fatalf("opened %d repositories for one path, want 1", opened)
	}

	moved := cfg
	moved.StateFile = filepath.Join(t.TempDir(), "moved.db")
	if errConfigure := store.Configure(moved); errConfigure != nil {
		t.Fatalf("Configure (moved) error = %v", errConfigure)
	}
	store.Read(func(state *State) {
		if len(state.Plans) != 0 {
			t.Fatalf("plans = %+v, want the new path's own document", state.Plans)
		}
	})
}

// A path that cannot be opened must leave the live one alone: starting empty
// there would silently discard the real record once someone fixes the path.
func TestConfigureKeepsTheLiveDocumentWhenTheNewPathFails(t *testing.T) {
	repo := &memoryRepository{state: NewState()}
	store := NewStore(func(path string) (Repository, error) {
		if filepath.Base(path) == "broken.db" {
			return nil, errors.New("not a database")
		}
		return repo, nil
	})
	t.Cleanup(store.Close)

	cfg := testConfig(t)
	if errConfigure := store.Configure(cfg); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	store.Update(func(state *State) { state.Keys["scope-a"] = &KeyState{Lifetime: Totals{CostUSD: 7}} })

	broken := cfg
	broken.StateFile = filepath.Join(t.TempDir(), "broken.db")
	if errConfigure := store.Configure(broken); errConfigure == nil {
		t.Fatal("Configure accepted an unusable path, want an error")
	}

	store.Update(func(state *State) { state.Keys["scope-a"].Lifetime.CostUSD = 9 })
	if repo.state.Keys["scope-a"].Lifetime.CostUSD != 9 {
		t.Fatalf("the rejected reconfigure stranded the original document: %+v", repo.state.Keys)
	}
}

// A state_file naming the JSON document has to resolve to the database beside
// it, which is the one the document seeds.
func TestConfigureResolvesAJSONStateFileToItsDatabase(t *testing.T) {
	var opened string
	store := NewStore(func(path string) (Repository, error) {
		opened = path
		return &memoryRepository{state: NewState()}, nil
	})
	t.Cleanup(store.Close)

	dir := t.TempDir()
	if errConfigure := store.Configure(Config{Enabled: true, StateFile: filepath.Join(dir, "state.json")}); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	if want := filepath.Join(dir, "state.db"); opened != want {
		t.Fatalf("opened %q, want %q", opened, want)
	}
}

// The request path must write one key rather than the whole record, and the
// operations that reshape the key set must write all of it.
func TestMutationsWriteOnlyWhatTheyTouched(t *testing.T) {
	store, repo := newStoreWithRepository(t)
	store.Update(func(state *State) {
		state.Plans = []Plan{{ID: "daily", AmountUSD: 5, Period: Period{Kind: PeriodDaily}}}
		state.Keys["scope-a"] = &KeyState{}
		state.Keys["scope-b"] = &KeyState{}
	})

	repo.saves = nil
	if errLabel := store.SetLabel("scope-a", "Alice"); errLabel != nil {
		t.Fatalf("SetLabel error = %v", errLabel)
	}
	if len(repo.saves) != 1 || repo.saves[0].AllKeys ||
		len(repo.saves[0].Keys) != 1 || repo.saves[0].Keys[0] != "scope-a" {
		t.Fatalf("saves = %+v, want the one renamed key", repo.saves)
	}

	repo.saves = nil
	if _, errSync := store.SyncKeys([]string{"sk-live-000000001"}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	if len(repo.saves) != 1 || !repo.saves[0].AllKeys {
		t.Fatalf("saves = %+v, want the whole key set rewritten", repo.saves)
	}
}

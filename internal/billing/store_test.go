package billing

import (
	"errors"
	"path/filepath"
	"strings"
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
	store.ReplaceAll(func(state *State) { state.Keys["scope-a"] = &KeyState{Lifetime: Totals{CostUSD: 7}} })

	broken := cfg
	broken.StateFile = filepath.Join(t.TempDir(), "broken.db")
	if errConfigure := store.Configure(broken); errConfigure == nil {
		t.Fatal("Configure accepted an unusable path, want an error")
	}

	store.ReplaceAll(func(state *State) { state.Keys["scope-a"].Lifetime.CostUSD = 9 })
	if repo.state.Keys["scope-a"].Lifetime.CostUSD != 9 {
		t.Fatalf("the rejected reconfigure stranded the original document: %+v", repo.state.Keys)
	}
}

func TestConfigureResolvesJSONStatePathToDatabase(t *testing.T) {
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
	store.ReplaceAll(func(state *State) {
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

// The panel synchronizes keys and models on every session start, and moving
// nothing is the ordinary outcome. Recording that would be the largest write the
// plugin makes: every key, every per-model row and the whole price table.
func TestSyncsWriteNothingWhenNothingMoved(t *testing.T) {
	store, repo := newStoreWithRepository(t)
	if _, errKeys := store.SyncKeys([]string{"sk-live-000000001"}, false); errKeys != nil {
		t.Fatalf("SyncKeys error = %v", errKeys)
	}
	if _, errModels := store.SyncModels([]string{"gpt-5.5"}); errModels != nil {
		t.Fatalf("SyncModels error = %v", errModels)
	}

	repo.saves = nil
	if _, errKeys := store.SyncKeys([]string{"sk-live-000000001"}, false); errKeys != nil {
		t.Fatalf("SyncKeys error = %v", errKeys)
	}
	if _, errModels := store.SyncModels([]string{"gpt-5.5"}); errModels != nil {
		t.Fatalf("SyncModels error = %v", errModels)
	}
	if len(repo.saves) != 0 {
		t.Fatalf("saves = %+v, want a sync that moved nothing to write nothing", repo.saves)
	}
}

// A repository that answers no error owes a working set. Taking a nil one would
// defer the failure to the first request, which reports it as a panic rather
// than as the configuration error it is.
func TestConfigureRefusesARepositoryWithoutAWorkingSet(t *testing.T) {
	store := NewStore(func(string) (Repository, error) { return &statelessRepository{}, nil })
	t.Cleanup(store.Close)

	if errConfigure := store.Configure(testConfig(t)); errConfigure == nil {
		t.Fatal("Configure accepted a repository that answered no working set")
	}
}

// Closing is where the write-ahead log is folded back into the database, so a
// failure there is the operator's warning that the tail of the record may exist
// only beside it. A reconfigure has the incoming database to record that in.
func TestReconfigureReportsADatabaseThatFailsToClose(t *testing.T) {
	repos := []Repository{
		&memoryRepository{state: NewState(), closeFail: errors.New("磁盘已满")},
		&memoryRepository{state: NewState()},
	}
	store := NewStore(func(string) (Repository, error) {
		repo := repos[0]
		repos = repos[1:]
		return repo, nil
	})
	t.Cleanup(store.Close)
	for range 2 {
		if errConfigure := store.Configure(testConfig(t)); errConfigure != nil {
			t.Fatalf("Configure error = %v", errConfigure)
		}
	}

	events := mustEvents(t, store)
	reported := false
	for _, event := range events {
		if event.Level == EventError && strings.Contains(event.Message, "磁盘已满") {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("events = %+v, want the failing close reported", events)
	}
}

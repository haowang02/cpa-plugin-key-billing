package billing

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigureLoadsTheDocumentBehindTheNewPath(t *testing.T) {
	first := &memoryRepository{state: NewState()}
	first.state.Plans = []Plan{{ID: "monthly-20", AmountUSD: 20, PeriodSeconds: 2592000}}
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
	store.ReplaceAll(func(state *State) { state.Keys["scope-a"] = &KeyState{Label: "live"} })

	broken := cfg
	broken.StateFile = filepath.Join(t.TempDir(), "broken.db")
	if errConfigure := store.Configure(broken); errConfigure == nil {
		t.Fatal("Configure accepted an unusable path, want an error")
	}

	store.ReplaceAll(func(state *State) { state.Keys["scope-a"].Label = "still-live" })
	if repo.state.Keys["scope-a"].Label != "still-live" {
		t.Fatalf("the rejected reconfigure stranded the original document: %+v", repo.state.Keys)
	}
}

// The request path must write one key rather than the whole record, and the
// operations that reshape the key set must write all of it.
func TestMutationsWriteOnlyWhatTheyTouched(t *testing.T) {
	store, repo := newStoreWithRepository(t)
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{{ID: "daily", AmountUSD: 5, PeriodSeconds: 86400}}
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
	if _, errModels := store.SyncPriceCatalog([]string{"gpt-5.5"}); errModels != nil {
		t.Fatalf("SyncPriceCatalog error = %v", errModels)
	}

	repo.saves = nil
	if _, errKeys := store.SyncKeys([]string{"sk-live-000000001"}, false); errKeys != nil {
		t.Fatalf("SyncKeys error = %v", errKeys)
	}
	if _, errModels := store.SyncPriceCatalog([]string{"gpt-5.5"}); errModels != nil {
		t.Fatalf("SyncPriceCatalog error = %v", errModels)
	}
	if len(repo.saves) != 0 {
		t.Fatalf("saves = %+v, want a sync that moved nothing to write nothing", repo.saves)
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

	events := mustPluginLogs(t, store)
	reported := false
	for _, event := range events {
		if event.Level == PluginLogError && strings.Contains(event.Message, "磁盘已满") {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("events = %+v, want the failing close reported", events)
	}
}

func TestRecoveredWriteIncludesPendingRequestEvents(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	store, repo := newAccountStoreWithRepository(t, now)
	repo.fail = errors.New("disk full")
	store.RecordUsage(subsetEvent("scope-a", now))
	store.RecordUsage(subsetEvent("scope-a", now.Add(time.Minute)))
	if len(repo.requestEvents) != 0 {
		t.Fatalf("request events = %d while writes fail", len(repo.requestEvents))
	}

	repo.fail = nil
	if err := store.SetLabel("scope-a", "Alice"); err != nil {
		t.Fatal(err)
	}
	if len(repo.requestEvents) != 2 {
		t.Fatalf("recovered request events = %d, want 2", len(repo.requestEvents))
	}
}

func TestFailedWritesBoundPendingRequestEvents(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	store, repo := newAccountStoreWithRepository(t, now)
	repo.fail = errors.New("disk full")
	for i := range maxPendingRequestRecords + 25 {
		store.RecordUsage(subsetEvent("scope-a", now.Add(time.Duration(i)*time.Second)))
	}
	if len(store.dirty.NormalRequestEvents) != maxPendingRequestRecords {
		t.Fatalf("pending request events = %d, want %d", len(store.dirty.NormalRequestEvents), maxPendingRequestRecords)
	}
	if want := now.Add(25 * time.Second); !store.dirty.NormalRequestEvents[0].At.Equal(want) {
		t.Fatalf("oldest pending request event = %v, want %v", store.dirty.NormalRequestEvents[0].At, want)
	}
}

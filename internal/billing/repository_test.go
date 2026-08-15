package billing

import (
	"path/filepath"
	"testing"
	"time"
)

// memoryRepository stands in for storage while this package is under test: what
// billing charges is a different question from how SQL keeps it, and the SQL
// answer is exercised against a real database in internal/sqlite.
//
// It shares the state document with the store rather than copying it, so a
// mutation is visible here as soon as it is saved.
type memoryRepository struct {
	state *State
	log   []LogEntry
	// saves records the write set of every mutation, which is how a test asks
	// what a store operation actually persisted.
	saves []Changes
	// fail, when set, is returned by every save.
	fail error
	// closeFail, when set, is returned by Close.
	closeFail error
}

func (r *memoryRepository) Load(logCutoff time.Time) (Snapshot, error) {
	if r.state == nil {
		r.state = NewState()
	}
	if !logCutoff.IsZero() {
		kept := r.log[:0]
		for _, entry := range r.log {
			if !entry.At.Before(logCutoff) {
				kept = append(kept, entry)
			}
		}
		r.log = kept
	}
	return Snapshot{State: r.state, LogEntries: len(r.log)}, nil
}

func (r *memoryRepository) Save(_ *State, changes Changes) error {
	if r.fail != nil {
		return r.fail
	}
	r.saves = append(r.saves, changes)
	r.log = append(r.log, changes.Log...)
	if changes.LogCutoff.IsZero() {
		return nil
	}
	kept := r.log[:0]
	for _, entry := range r.log {
		if !entry.At.Before(changes.LogCutoff) {
			kept = append(kept, entry)
		}
	}
	r.log = kept
	return nil
}

// Searching, filtering and paging are the database's job and are tested there;
// what this package asks of the log is which entries a mutation produced and
// whose identity they carry.
func (r *memoryRepository) Logs(_ LogQuery, since time.Time) (LogView, error) {
	view := LogView{Entries: []LogRow{}}
	for i := len(r.log) - 1; i >= 0; i-- {
		entry := r.log[i]
		if entry.At.Before(since) {
			continue
		}
		row := LogRow{LogEntry: entry, Source: r.state.Credentials[entry.AuthIndex].Name()}
		if key := r.state.Keys[entry.Scope]; key != nil {
			row.Preview, row.Label = key.Preview, key.Label
		}
		view.Entries = append(view.Entries, row)
	}
	view.Total = len(view.Entries)
	return view, nil
}

func (r *memoryRepository) ClearLogs() (int, error) {
	cleared := len(r.log)
	r.log = nil
	return cleared, nil
}

func (r *memoryRepository) LoggedScopes(since time.Time) (map[string]struct{}, error) {
	scopes := make(map[string]struct{})
	for _, entry := range r.log {
		if !entry.At.Before(since) {
			scopes[entry.Scope] = struct{}{}
		}
	}
	return scopes, nil
}

func (r *memoryRepository) Close() error { return r.closeFail }

// statelessRepository answers no error and no working set, which the Repository
// contract forbids and a future backend could still do.
type statelessRepository struct{ memoryRepository }

func (r *statelessRepository) Load(time.Time) (Snapshot, error) { return Snapshot{}, nil }

func newStore(t *testing.T) *Store {
	store, _ := newStoreWithRepository(t)
	return store
}

func newStoreWithRepository(t *testing.T) (*Store, *memoryRepository) {
	t.Helper()
	repo := &memoryRepository{}
	store := NewStore(func(string) (Repository, error) { return repo, nil })
	if errConfigure := store.Configure(testConfig(t)); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	t.Cleanup(store.Close)
	return store, repo
}

func mustLogs(t *testing.T, store *Store, query LogQuery) LogView {
	t.Helper()
	view, errLogs := store.Logs(query)
	if errLogs != nil {
		t.Fatalf("Logs error = %v", errLogs)
	}
	return view
}

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.StateFile = filepath.Join(t.TempDir(), "state.db")
	return cfg
}

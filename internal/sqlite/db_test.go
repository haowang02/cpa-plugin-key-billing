package sqlite

import (
	"os"
	"path/filepath"
	"testing"

	"cpa-key-billing/internal/billing"
)

// The write-ahead log holds the most recently committed rows until a checkpoint,
// and those rows name masked keys, operator labels and upstream credentials. It
// is created with the mode of the database, so the database has to carry the
// restricted one before the driver ever opens it.
func TestOpenRestrictsTheDatabaseAndItsSidecars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	database := openDatabase(t, path)

	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Preview: "sk-tes…0001", Label: "Alice"}
	mustSave(t, database, state, billing.Changes{AllKeys: true})

	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		info, errStat := os.Stat(name)
		if errStat != nil {
			t.Fatalf("stat %s: %v", name, errStat)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("%s mode = %v, want 0600", filepath.Base(name), mode)
		}
	}
}

// A database left world-readable by an earlier version, and any sidecar a crash
// left beside it, is narrowed on the way in rather than staying exposed for as
// long as the file lives.
func TestOpenNarrowsAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	for _, name := range []string{path, path + "-wal"} {
		if errWrite := os.WriteFile(name, nil, 0o644); errWrite != nil {
			t.Fatalf("write %s: %v", name, errWrite)
		}
	}

	openDatabase(t, path)
	for _, name := range []string{path, path + "-wal"} {
		info, errStat := os.Stat(name)
		if errStat != nil {
			t.Fatalf("stat %s: %v", name, errStat)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("%s mode = %v, want 0600", filepath.Base(name), mode)
		}
	}
}

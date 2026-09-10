package sqlite

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
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

func TestOpenAcceptsRelativePathWithDSNCharacters(t *testing.T) {
	absolutePath := filepath.Join(t.TempDir(), "state?#.db")
	workingDir, errWorkingDir := os.Getwd()
	if errWorkingDir != nil {
		t.Fatalf("get working directory: %v", errWorkingDir)
	}
	path, errRelative := filepath.Rel(workingDir, absolutePath)
	if errRelative != nil {
		t.Fatalf("make relative path: %v", errRelative)
	}
	database := openDatabase(t, path)
	state := billing.NewState()
	state.Keys["scope-a"] = &billing.KeyState{Preview: "sk-tes…0001", Label: "Alice"}
	mustSave(t, database, state, billing.Changes{AllKeys: true})

	if _, errStat := os.Stat(path); errStat != nil {
		t.Fatalf("stat exact database path: %v", errStat)
	}
	if key := mustLoad(t, database).State.Keys["scope-a"]; key == nil || key.Label != "Alice" {
		t.Fatalf("key = %+v", key)
	}
}

func indexDefinitions(t *testing.T, d *DB) map[string]string {
	t.Helper()
	rows, err := d.db.Query("SELECT name, sql FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	definitions := map[string]string{}
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatal(err)
		}
		definitions[name] = definition
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return definitions
}

func ddlVersion(t *testing.T, d *DB) int {
	t.Helper()
	var version int
	if err := d.db.QueryRow("PRAGMA schema_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestOpenSynchronizesIndexes(t *testing.T) {
	d, _ := requestEventDatabase(t)
	expected := indexDefinitions(t, d)
	events := mustQueryRequestEvents(t, d, billing.RequestEventQuery{})
	if _, err := d.db.Exec(`DROP INDEX request_events_at;
		CREATE INDEX request_events_at ON request_events(at);
		DROP INDEX request_events_scope_at;
		CREATE INDEX request_events_auth_at ON request_events(auth_index, at);
		CREATE INDEX operator_message ON plugin_logs(message);
		CREATE UNIQUE INDEX operator_unique_message ON plugin_logs(message)`); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = openDatabase(t, d.path)
	actual := indexDefinitions(t, d)
	for _, name := range []string{"operator_message", "operator_unique_message"} {
		if actual[name] == "" {
			t.Fatalf("removed unmanaged index %s", name)
		}
		delete(actual, name)
	}
	if !maps.Equal(expected, actual) {
		t.Fatalf("indexes=%v, want %v", actual, expected)
	}
	if got := mustQueryRequestEvents(t, d, billing.RequestEventQuery{}); !reflect.DeepEqual(events, got) {
		t.Fatal("index synchronization changed history")
	}
	var format int
	if err := d.db.QueryRow("PRAGMA user_version").Scan(&format); err != nil || format != schemaVersion {
		t.Fatalf("format version=%d, err=%v", format, err)
	}
	version := ddlVersion(t, d)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = openDatabase(t, d.path)
	if ddlVersion(t, d) != version {
		t.Fatal("unchanged restart rebuilt indexes")
	}
}

func TestOpenRollsBackOnIndexNameCollision(t *testing.T) {
	for _, ddl := range []string{
		"CREATE UNIQUE INDEX request_events_at ON request_events(at)",
		"CREATE INDEX request_events_at ON plugin_logs(at)",
		"CREATE TABLE request_events_at (value TEXT)",
	} {
		t.Run(ddl, func(t *testing.T) {
			d, _ := requestEventDatabase(t)
			if _, err := d.db.Exec(`DROP INDEX plugin_logs_at;
				CREATE INDEX plugin_logs_at ON plugin_logs(at DESC);
				DROP INDEX request_events_at; ` + ddl); err != nil {
				t.Fatal(err)
			}
			before, version := indexDefinitions(t, d), ddlVersion(t, d)
			events := mustQueryRequestEvents(t, d, billing.RequestEventQuery{})
			if reopened, err := Open(d.path); err == nil {
				reopened.Close()
				t.Fatal("replaced an incompatible object")
			}
			if !maps.Equal(before, indexDefinitions(t, d)) || ddlVersion(t, d) != version {
				t.Fatal("failed synchronization did not restore the original indexes")
			}
			if got := mustQueryRequestEvents(t, d, billing.RequestEventQuery{}); !reflect.DeepEqual(events, got) {
				t.Fatal("failed synchronization changed history")
			}
		})
	}
}

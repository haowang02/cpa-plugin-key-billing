package billing

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigureLoadsTheDocumentBehindTheNewPath(t *testing.T) {
	first := &memoryRepository{state: NewState()}
	first.state.Plans = []Plan{{ID: "monthly-20", Windows: []QuotaWindow{{ID: "default", Name: "额度", AmountUSD: 20, PeriodSeconds: 2592000}}}}
	second := &memoryRepository{state: NewState()}

	opened := 0
	store := NewStore(func(string) (Repository, error) {
		opened++
		if opened == 1 {
			return first, nil
		}
		return second, nil
	}, nil)
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
	}, nil)
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
	}, nil)
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
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{{ID: "p", Windows: []QuotaWindow{
			{ID: "short", Name: "短时", AmountUSD: 5, PeriodSeconds: 3600},
			{ID: "long", Name: "预算", AmountUSD: 10, PeriodSeconds: 86400},
		}}}
		state.Keys["scope-a"] = &KeyState{PlanID: "p"}
	})
	store.Authorize("scope-a", now)
	repo.fail = errors.New("disk full")
	store.RecordUsage(subsetEvent("scope-a", now))
	store.RecordUsage(subsetEvent("scope-a", now.Add(time.Minute)))
	if len(repo.requestEvents) != 0 {
		t.Fatalf("request events = %d while writes fail", len(repo.requestEvents))
	}

	if err := store.SetConcurrencyLimit("scope-a", 1); err == nil {
		t.Fatal("failed management write returned success")
	}
	repo.fail = nil
	if err := store.SetLabel("scope-a", "Alice"); err != nil {
		t.Fatal(err)
	}
	if store.state.Keys["scope-a"].ConcurrencyLimit != 0 {
		t.Fatal("failed management edit was retried with usage")
	}
	if len(repo.requestEvents) != 2 {
		t.Fatalf("recovered request events = %d, want 2", len(repo.requestEvents))
	}
	for _, cycle := range repo.state.Keys["scope-a"].Cycles {
		assertClose(t, "persisted cycle cost", cycle.SpentUSD, 2*wantSubsetCost)
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

func TestConfigurationWriteFailureKeepsState(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Store) error
	}{
		{"concurrency", func(s *Store) error { return s.SetConcurrencyLimit("key", 1) }},
		{"sync", func(s *Store) error { _, err := s.SyncKeys([]string{"sk-test-sync-0001"}, false); return err }},
		{"edit windows", func(s *Store) error {
			windows := s.Plans()[0].Windows
			windows[0].PeriodSeconds = 7200
			_, err := s.UpdatePlanWithBindings(PlanPatch{ID: "p", Windows: &windows}, nil)
			return err
		}},
		{"reset", func(s *Store) error { _, err := s.ResetQuota(ResetRequest{Mode: "global"}); return err }},
		{"delete plan", func(s *Store) error { _, err := s.DeletePlan("p"); return err }},
		{"edit route", func(s *Store) error {
			rule := RouteRule{Models: []string{"gpt"}}
			_, err := s.UpdateRoute(RoutePatch{ID: "r", Rule: &rule}, nil)
			return err
		}},
		{"set routes", func(s *Store) error { return s.SetKeyRoutes("key", RouteBindings{}) }},
		{"delete route", func(s *Store) error { _, err := s.DeleteRoute("r"); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := newStoreWithRepository(t)
			now := time.Now().UTC().Truncate(time.Second)
			store.now = func() time.Time { return now }
			store.ReplaceAll(func(state *State) {
				window := QuotaWindow{ID: "default", Name: "额度", AmountUSD: 10, PeriodSeconds: 3600, CycleAnchorAt: now.Add(time.Hour)}
				state.Plans = []Plan{{ID: "p", Windows: []QuotaWindow{window}}}
				cycle := window.newCycle("p", now)
				cycle.SpentUSD = 5
				state.Routes = []Route{{ID: "r", Name: "r", Rule: RouteRule{DeniedModels: []string{"gpt"}, DeniedCredentialIDs: []string{CredentialFingerprint("dummy")}}}, {ID: "keep", Name: "keep"}}
				state.Keys["key"] = &KeyState{Preview: "unknown", InConfig: true, PlanID: "p",
					Cycles:        map[string]QuotaCycle{"default": cycle},
					RouteBindings: RouteBindings{RouteIDs: []string{"r", "keep"}}}
			})
			before, err := json.Marshal(store.state)
			if err != nil {
				t.Fatal(err)
			}
			repo.fail = errors.New("disk full")
			if err := test.edit(store); !errors.Is(err, repo.fail) {
				t.Fatalf("edit error = %v", err)
			}
			after, err := json.Marshal(store.state)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("failed edit changed the live state")
			}
			if !store.dirty.empty() {
				t.Fatal("failed edit entered the usage retry queue")
			}
		})
	}
}

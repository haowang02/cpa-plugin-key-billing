package billing

import (
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"
)

type memoryRepository struct {
	referencePrices *memoryReferencePriceRepository
	state           *State
	requestEvents   []RequestEvent
	requestErrors   []RequestErrorEvent
	pluginLogs      []PluginLog
	fail            error
	closeFail       error
}

func (r *memoryRepository) CredentialUsageStats([]string, string, time.Time, time.Time, ExchangeRate) (map[string]CredentialUsageStats, error) {
	return map[string]CredentialUsageStats{}, r.fail
}

func (r *memoryRepository) LatestQuotaReports([]string, time.Time) (map[string][]QuotaObservation, error) {
	return map[string][]QuotaObservation{}, r.fail
}

func (r *memoryRepository) QuotaWindowHistories(string, string, time.Time, time.Time) ([]QuotaWindowHistory, error) {
	return nil, r.fail
}

func (r *memoryRepository) UsageTotal(string, UsageQuery, ExchangeRate) (UsageTotal, error) {
	return UsageTotal{}, r.fail
}

func (r *memoryRepository) UsageByModel(string, UsageQuery, ExchangeRate) ([]ModelUsage, error) {
	return nil, r.fail
}

func (r *memoryRepository) UsageSources(string, UsageQuery) ([]string, error) {
	return nil, r.fail
}

func (r *memoryRepository) Load(requestCutoff, pluginCutoff time.Time) (Snapshot, error) {
	if r.state == nil {
		r.state = NewState()
	}
	if !requestCutoff.IsZero() {
		kept := r.requestEvents[:0]
		for _, event := range r.requestEvents {
			if !event.At.Before(requestCutoff) {
				kept = append(kept, event)
			}
		}
		r.requestEvents = kept
		keptErrors := r.requestErrors[:0]
		for _, event := range r.requestErrors {
			if !event.Event.At.Before(requestCutoff) {
				keptErrors = append(keptErrors, event)
			}
		}
		r.requestErrors = keptErrors
	}
	r.pluginLogs = keptPluginLogs(r.pluginLogs, pluginCutoff)
	return Snapshot{State: r.state, RequestEventCount: len(r.requestEvents) + len(r.requestErrors)}, nil
}

func (r *memoryRepository) AppendPluginLog(entry PluginLog, cutoff time.Time) error {
	r.pluginLogs = keptPluginLogs(append(r.pluginLogs, entry), cutoff)
	return nil
}

func (r *memoryRepository) PluginLogsPage(query PluginLogQuery) (PluginLogPage, error) {
	page := PluginLogPage{Entries: []PluginLog{}}
	for i := len(r.pluginLogs) - 1; i >= 0; i-- {
		entry := r.pluginLogs[i]
		if entry.At.Before(query.Since) || query.BeforeID > 0 && entry.ID >= query.BeforeID || len(query.Levels) > 0 && !slices.Contains(query.Levels, entry.Level) {
			continue
		}
		page.Entries = append(page.Entries, entry)
		if len(page.Entries) > query.Limit {
			page.Entries = page.Entries[:query.Limit]
			page.NextBeforeID = page.Entries[len(page.Entries)-1].ID
			break
		}
	}
	return page, nil
}

func (r *memoryRepository) ClearPluginLogs() (int, error) {
	cleared := len(r.pluginLogs)
	r.pluginLogs = nil
	return cleared, nil
}

func keptPluginLogs(entries []PluginLog, cutoff time.Time) []PluginLog {
	if cutoff.IsZero() {
		return entries
	}
	kept := entries[:0]
	for _, entry := range entries {
		if !entry.At.Before(cutoff) {
			kept = append(kept, entry)
		}
	}
	return kept
}

// Prices share the loaded state; Store publishes each change after these
// methods report whether the write succeeded.
func (r *memoryRepository) UpsertPrice(CustomPrice) error { return r.fail }

func (r *memoryRepository) DeletePrice(string) error { return r.fail }

func (r *memoryRepository) Save(state *State, changes Changes) error {
	if r.fail != nil {
		return r.fail
	}
	r.state = state
	r.requestEvents = append(r.requestEvents, changes.NormalRequestEvents...)
	r.requestErrors = append(r.requestErrors, changes.RequestErrorEvents...)
	if changes.RequestEventCutoff.IsZero() {
		return nil
	}
	kept := r.requestEvents[:0]
	for _, event := range r.requestEvents {
		if !event.At.Before(changes.RequestEventCutoff) {
			kept = append(kept, event)
		}
	}
	r.requestEvents = kept
	keptErrors := r.requestErrors[:0]
	for _, event := range r.requestErrors {
		if !event.Event.At.Before(changes.RequestEventCutoff) {
			keptErrors = append(keptErrors, event)
		}
	}
	r.requestErrors = keptErrors
	return nil
}

// Filtering and paging are covered by the SQLite repository tests.
func (r *memoryRepository) RequestEvents(query RequestEventQuery, since time.Time) (RequestEventView, error) {
	view := RequestEventView{Entries: []RequestEventRow{}}
	events := append([]RequestEvent(nil), r.requestEvents...)
	for _, failure := range r.requestErrors {
		events = append(events, failure.Event)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.After(events[j].At) })
	for _, entry := range events {
		if entry.At.Before(since) || (query.Scope != "" && entry.Scope != query.Scope) {
			continue
		}
		row := RequestEventRow{RequestEvent: entry}
		if key := r.state.Keys[entry.Scope]; key != nil {
			row.Preview, row.Label = key.Preview, key.Label
		}
		view.Entries = append(view.Entries, row)
	}
	view.Total = len(view.Entries)
	return view, nil
}

func (r *memoryRepository) RequestErrors(query RequestErrorQuery, since time.Time) (RequestErrorView, error) {
	view := RequestErrorView{Entries: []RequestErrorRow{}}
	for i := len(r.requestErrors) - 1; i >= 0; i-- {
		write := r.requestErrors[i]
		if write.Event.At.Before(since) || query.Scope != "" && write.Event.Scope != query.Scope {
			continue
		}
		row := RequestErrorRow{At: write.Event.At, Scope: write.Event.Scope, AuthIndex: write.Event.AuthIndex,
			Provider: write.Event.Provider, ExecutorType: write.Event.ExecutorType, UpstreamModel: write.Event.UpstreamModel,
			BillingModel: write.Event.BillingModel, LatencyMS: write.Event.LatencyMS, TTFTMS: write.Event.TTFTMS,
			RequestError: write.Error}
		if key := r.state.Keys[write.Event.Scope]; key != nil {
			row.Preview, row.Label = key.Preview, key.Label
		}
		view.Entries = append(view.Entries, row)
	}
	view.Total = len(view.Entries)
	return view, nil
}

func (r *memoryRepository) Analysis(query RequestEventQuery, rate ExchangeRate, since time.Time) (AnalysisView, error) {
	return AnalysisView{}, nil
}

func (r *memoryRepository) AnalysisSpeed(query RequestEventQuery, since time.Time) (AnalysisSpeedView, error) {
	return AnalysisSpeedView{}, nil
}

func (r *memoryRepository) Close() error { return r.closeFail }

func newStore(t *testing.T) *Store {
	store, _ := newStoreWithRepository(t)
	return store
}

func newStoreWithRepository(t *testing.T) (*Store, *memoryRepository) {
	t.Helper()
	repo := &memoryRepository{}
	store := NewStore(func(string) (Repository, error) { return repo, nil }, nil)
	if errConfigure := store.Configure(testConfig(t)); errConfigure != nil {
		t.Fatalf("Configure error = %v", errConfigure)
	}
	t.Cleanup(store.Close)
	return store, repo
}

func (s *Store) ReplaceAll(fn func(*State)) {
	updateResult(s, func(state *State) (struct{}, Changes) {
		fn(state)
		return struct{}{}, Changes{AllKeys: true, Plans: true, Routes: true, ConfigCredentials: true}
	})
}

func (s *Store) Read(fn func(*State)) {
	s.read(fn)
}

func mustRequestEvents(t *testing.T, store *Store, query RequestEventQuery) RequestEventView {
	t.Helper()
	view, err := store.RequestEvents(query)
	if err != nil {
		t.Fatalf("RequestEvents error = %v", err)
	}
	return view
}

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Debug = true
	cfg.StateFile = filepath.Join(t.TempDir(), "state.db")
	return cfg
}

func (r *memoryRepository) EventKeys(from, to, since time.Time) ([]EventKey, error) {
	if from.Before(since) {
		from = since
	}
	keys := []EventKey{}
	seen := map[string]bool{}
	events := append([]RequestEvent{}, r.requestEvents...)
	for _, failure := range r.requestErrors {
		events = append(events, failure.Event)
	}
	for _, event := range events {
		if seen[event.Scope] || event.At.Before(from) || !to.IsZero() && !event.At.Before(to) {
			continue
		}
		seen[event.Scope] = true
		if event.Scope == "" {
			continue
		}
		key := EventKey{Scope: event.Scope, Preview: UnknownKeyPreview}
		if stored := r.state.Keys[event.Scope]; stored != nil {
			key.Preview, key.Label, key.DeletedAt = stored.Preview, stored.Label, stored.DeletedAt
		}
		keys = append(keys, key)
	}
	if seen[""] {
		keys = append(keys, EventKey{})
	}
	return keys, nil
}

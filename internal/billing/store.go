package billing

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Locking: cfgMu serializes Configure/Close against each other; mu guards the
// working set, the active config and the repository. A save runs under mu so
// that memory and disk can never disagree about what was committed.
//
// This type deliberately runs no goroutines, and neither does the rest of the
// plugin. It is compiled into a c-shared library that CLIProxyAPI dlopens, so
// it carries its own Go runtime inside a process that already has one. Two Go
// runtimes in one address space only coexist while the second one is inert
// between host calls: a runtime that keeps its own timers, GC cycles, and
// preemption signals running concurrently with the host's took the whole proxy
// down with "fatal error: bad flushGen" in runtime.(*mcache).prepareForSweep.
// Every write therefore lands inside the host call that caused it. Do not
// reintroduce a background flusher.
type Store struct {
	cfgMu sync.Mutex

	mu    sync.RWMutex
	state *State
	cfg   Config
	repo  Repository
	path  string

	// pending tracks in-flight requests between interception and their
	// terminal event. It is runtime-only and never persisted.
	pending *pendingTable

	errMu     sync.Mutex
	lastError string

	events eventLog

	open OpenRepository

	now func() time.Time
}

func NewStore(open OpenRepository) *Store {
	return &Store{
		state:   NewState(),
		cfg:     DefaultConfig(),
		pending: &pendingTable{entries: make(map[string]PendingRequest)},
		open:    open,
		now:     time.Now,
	}
}

func (s *Store) Now() time.Time {
	return s.now()
}

// The host invokes Configure on every plugin.reconfigure, so repeated calls
// must be safe.
func (s *Store) Configure(cfg Config) error {
	normalized := cfg.normalized()
	path, errPath := resolveStatePath(normalized.StateFile)
	if errPath != nil {
		return errPath
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	s.mu.RLock()
	currentPath := s.path
	s.mu.RUnlock()

	if currentPath == path {
		s.mu.Lock()
		changed := s.cfg != normalized
		s.cfg = normalized
		s.mu.Unlock()
		if changed {
			s.Event(EventInfo, "配置已更新：%s。", normalized.describe())
		}
		return nil
	}

	// Open and read the incoming database before touching anything, so a switch
	// to an unusable path leaves the current one live. Nothing has to be written
	// on the way out: every change is already committed.
	repo, errOpen := s.open(path)
	if errOpen != nil {
		return errOpen
	}
	snapshot, errLoad := repo.Load()
	if errLoad != nil {
		_ = repo.Close()
		return errLoad
	}

	s.mu.Lock()
	previous := s.repo
	s.state = snapshot.State
	s.cfg = normalized
	s.repo = repo
	s.path = path
	s.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}

	s.Event(EventInfo, "已加载计费数据库 %s：%d 个 API Key、%d 个订阅计划、%d 条计费日志。%s。",
		path, len(snapshot.State.Keys), len(snapshot.State.Plans), snapshot.LogEntries, normalized.describe())
	return nil
}

// Close releases the database. The host calls it through the C ABI shutdown
// hook. There is nothing left to write by then.
func (s *Store) Close() {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.mu.Lock()
	repo := s.repo
	s.repo = nil
	s.path = ""
	s.mu.Unlock()
	if repo != nil {
		_ = repo.Close()
	}
}

// Enabled reports whether the plugin should act on requests. A disabled plugin
// still answers Management API calls so an operator can inspect it.
func (s *Store) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Enabled
}

func (s *Store) Read(fn func(*State)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(s.state)
}

func (s *Store) BillingModel(upstreamModel, routeModel string) string {
	model := ""
	s.Read(func(state *State) {
		model = state.ResolveBillingModel(upstreamModel, routeModel)
	})
	return model
}

// Update mutates the working set and persists all of it. Operations that know
// what they touched report it instead, so that billing one request rewrites one
// key rather than every record the plugin holds.
func (s *Store) Update(fn func(*State)) {
	updateResult(s, func(state *State) (struct{}, Changes) {
		fn(state)
		return struct{}{}, Changes{AllKeys: true, Plans: true, Prices: true, Credentials: true}
	})
}

// updateResult applies one mutation and writes the rows it reports touching.
// The save happens under the same lock as the mutation: a repository that
// refuses the write leaves the change live in memory, where the next mutation
// of the same rows carries it to disk again.
func updateResult[T any](s *Store, fn func(*State) (T, Changes)) T {
	s.mu.Lock()
	value, changes := fn(s.state)
	// A store the host has not configured yet keeps its working set in memory
	// and has nowhere to put it. That window closes at plugin.register, before
	// any traffic reaches the plugin.
	var errSave error
	written := s.repo != nil && !changes.empty()
	if written {
		errSave = s.repo.Save(s.state, changes)
	}
	s.mu.Unlock()

	switch {
	case errSave != nil:
		s.recordWriteError(errSave)
	case written:
		s.recordWriteSuccess()
	}
	return value
}

func (s *Store) recordWriteSuccess() {
	s.errMu.Lock()
	recovered := s.lastError != ""
	s.lastError = ""
	s.errMu.Unlock()
	if recovered {
		s.Event(EventInfo, "计费数据库恢复写入。")
	}
}

func (s *Store) recordWriteError(err error) {
	s.errMu.Lock()
	first := s.lastError == ""
	s.lastError = err.Error()
	s.errMu.Unlock()
	// A disk that refuses one write refuses the next one too, on every host call
	// that follows. Report the onset and then stay quiet until a write succeeds,
	// so the log still shows what happened before the failure.
	if first {
		s.Event(EventError, "保存计费数据失败：%v", err)
	}
}

func (s *Store) repository() Repository {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.repo
}

type Status struct {
	Enabled bool `json:"enabled"`
}

func (s *Store) Status() Status {
	return Status{Enabled: s.Enabled()}
}

// state_file names the database. A path ending in .json names the JSON document
// a new database is seeded from, and resolves to the database beside it.
func resolveStatePath(path string) (string, error) {
	if path == "" {
		path = DefaultStateFile
	}
	if extension := filepath.Ext(path); strings.EqualFold(extension, ".json") {
		path = strings.TrimSuffix(path, extension) + ".db"
	}
	absolute, errAbs := filepath.Abs(path)
	if errAbs != nil {
		return "", fmt.Errorf("解析计费数据库路径 %q：%w", path, errAbs)
	}
	return absolute, nil
}

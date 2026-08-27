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
	dirty Changes

	// activeRequests and activeByScope are the process-local concurrency slots.
	// Only the configured limit is persisted: active requests end with this host
	// process and are correlated exactly by the host-provided request ID.
	activeRequests map[string]string
	activeByScope  map[string]int

	// blocked remembers which keys have already had their exhausted quota
	// reported, so retries against one do not repeat it.
	blocked blockedKeys

	// denied does the same for the models a key may not call.
	denied deniedModels

	errMu     sync.Mutex
	lastError string

	open func(string) (Repository, error)

	now func() time.Time
}

func NewStore(open func(string) (Repository, error)) *Store {
	return &Store{
		state:          NewState(),
		cfg:            DefaultConfig(),
		activeRequests: make(map[string]string),
		activeByScope:  make(map[string]int),
		open:           open,
		now:            time.Now,
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
	snapshot, errLoad := repo.Load(s.Now().Add(-LogRetention))
	if errLoad != nil {
		s.closeRepository(repo)
		return errLoad
	}
	// A repository that answers no error owes a working set. Accepting a nil one
	// would defer the failure to the first host call, which reports it as a
	// panic rather than as the configuration error it is.
	if snapshot.State == nil {
		s.closeRepository(repo)
		return fmt.Errorf("读取计费数据库 %s：未返回计费状态", path)
	}

	s.mu.Lock()
	previous := s.repo
	s.state = snapshot.State
	s.cfg = normalized
	s.repo = repo
	s.path = path
	s.dirty = Changes{}
	s.mu.Unlock()
	if previous != nil {
		s.closeRepository(previous)
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
	s.dirty = Changes{}
	s.activeRequests = make(map[string]string)
	s.activeByScope = make(map[string]int)
	s.mu.Unlock()
	if repo != nil {
		s.closeRepository(repo)
	}
}

// Closing is where the database checkpoints its write-ahead log back into
// itself, so a failure there is the operator's warning that the tail of the
// record may exist only beside it. On a reconfigure the report lands in a plugin
// log that is still being read; at shutdown it is the last thing written.
func (s *Store) closeRepository(repo Repository) {
	if errClose := repo.Close(); errClose != nil {
		s.Event(EventError, "关闭计费数据库失败：%v", errClose)
	}
}

// Enabled reports whether the plugin should act on requests. A disabled plugin
// still answers Management API calls so an operator can inspect it.
func (s *Store) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Enabled
}

func (s *Store) read(fn func(*State)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(s.state)
}

// updateResult applies one mutation and writes the rows it reports touching.
// The save happens under the same lock as the mutation. A failed change stays
// live in memory and is merged into the next write, including append-only log
// entries. Holding the lock across the write
// also holds up the request path for as long as the write takes — the database
// serves a single connection, so a save already waits behind any query in
// flight, and _busy_timeout bounds what a writer outside this process can add.
// A mutation that panics still releases the lock, because the plugin log the
// panic is reported through is behind the same one.
func updateResult[T any](s *Store, fn func(*State) (T, Changes)) T {
	var (
		value   T
		written bool
		errSave error
	)
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		var current Changes
		value, current = fn(s.state)
		// A store the host has not configured yet keeps its working set in
		// memory and has nowhere to put it. That window closes at
		// plugin.register, before any traffic reaches the plugin.
		changes := s.dirty.merge(current)
		written = s.repo != nil && !changes.empty()
		if written {
			errSave = s.repo.Save(s.state, changes)
			if errSave == nil {
				s.dirty = Changes{}
			} else {
				s.dirty = changes
			}
		}
	}()

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

// withRepository runs fn against the active repository, holding the read lock
// for the whole call so that a reconfigure cannot close the database out from
// under a query in flight: Configure swaps the repository under the write lock
// and only closes the old one afterwards, which a reader holding the lock has
// already finished with. Costing the write path anything is unlikely — the
// database serves a single connection, so a save waits behind the query either
// way. A store the host has not configured yet has nothing to read.
func withRepository[T any](s *Store, fn func(Repository) (T, error)) (T, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.repo == nil {
		var zero T
		return zero, nil
	}
	return fn(s.repo)
}

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

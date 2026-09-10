package billing

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// cfgMu serializes Configure/Close. mu guards state, config and repository
// access, including mutations and their database writes.
//
// Work must finish synchronously within host calls: background activity in this
// embedded Go runtime can conflict with CLIProxyAPI's runtime.
type Store struct {
	cfgMu           sync.Mutex
	referencePrices atomic.Pointer[referencePriceManager]

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

	errMu     sync.Mutex
	lastError string

	open                    func(string) (Repository, error)
	downloadReferencePrices func(context.Context) ([]byte, error)

	now func() time.Time
}

func NewStore(open func(string) (Repository, error), downloadReferencePrices func(context.Context) ([]byte, error)) *Store {
	if downloadReferencePrices == nil {
		downloadReferencePrices = downloadModelsDevPrices
	}
	return &Store{
		state:                   NewState(),
		cfg:                     DefaultConfig(),
		activeRequests:          make(map[string]string),
		activeByScope:           make(map[string]int),
		open:                    open,
		downloadReferencePrices: downloadReferencePrices,
		now:                     time.Now,
	}
}

func (s *Store) Now() time.Time {
	return s.now()
}

// The host invokes Configure on every plugin.reconfigure, so repeated calls
// must be safe.
func (s *Store) Configure(cfg Config) error {
	normalized := cfg.normalized()
	path, err := filepath.Abs(normalized.StateFile)
	if err != nil {
		return fmt.Errorf("解析计费数据库路径 %q：%w", normalized.StateFile, err)
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
			s.AddPluginLog(PluginLogInfo, "配置已更新：%s", normalized.describe())
		}
		return nil
	}

	// Load the incoming database before replacing the active one.
	repo, errOpen := s.open(path)
	if errOpen != nil {
		return errOpen
	}
	now := s.Now()
	snapshot, errLoad := repo.Load(now.Add(-RequestEventRetention), now.Add(-PluginLogRetention))
	if errLoad != nil {
		s.closeRepository(repo)
		return errLoad
	}
	references, errReferencePrices := openReferencePrices(repo, s.downloadReferencePrices, s.AddPluginLog)
	if errReferencePrices != nil {
		s.closeRepository(repo)
		return errReferencePrices
	}
	s.mu.Lock()
	previous := s.repo
	previousReferences := s.referencePrices.Swap(references)
	s.state = snapshot.State
	s.cfg = normalized
	s.repo = repo
	s.path = path
	s.dirty = Changes{}
	s.mu.Unlock()
	previousReferences.close()
	if previous != nil {
		s.closeRepository(previous)
	}

	s.AddPluginLog(PluginLogInfo, "已加载计费数据库：%s，%d 个 API Key、%d 个订阅计划、%d 条请求事件，%s",
		path, len(snapshot.State.Keys), len(snapshot.State.Plans), snapshot.RequestEventCount, normalized.describe())
	return nil
}

// Close releases the database when the host shuts down the plugin.
func (s *Store) Close() {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.mu.Lock()
	repo := s.repo
	previousReferences := s.referencePrices.Swap(nil)
	s.repo = nil
	s.path = ""
	s.dirty = Changes{}
	s.activeRequests = make(map[string]string)
	s.activeByScope = make(map[string]int)
	s.mu.Unlock()
	previousReferences.close()
	if repo != nil {
		s.closeRepository(repo)
	}
}

func (s *Store) closeRepository(repo Repository) {
	if errClose := repo.Close(); errClose != nil {
		s.AddPluginLog(PluginLogError, "关闭计费数据库失败：%v", errClose)
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

// Usage and cycle settlement stay live when persistence fails; their pending
// changes are retried on the next write. Management edits use editConfiguration.
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

// Management edits publish configuration only after saving succeeds.
// Failed edits must not enter the retry queue for already-recorded usage.
func editConfiguration[T any](s *Store, fn func(*State) (T, Changes, error)) (T, error) {
	var value T
	var errSave error
	written := false
	err := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()

		next := *s.state
		next.Plans = make([]Plan, len(s.state.Plans))
		for i, plan := range s.state.Plans {
			next.Plans[i] = clonePlan(plan)
		}
		next.Routes = make([]Route, len(s.state.Routes))
		for i, route := range s.state.Routes {
			next.Routes[i] = cloneRoute(route)
		}
		next.Keys = make(map[string]*KeyState, len(s.state.Keys))
		for scope, key := range s.state.Keys {
			if key == nil {
				next.Keys[scope] = nil
				continue
			}
			copyKey := *key
			copyKey.Cycles = maps.Clone(key.Cycles)
			copyKey.RouteBindings = key.RouteBindings.clone()
			next.Keys[scope] = &copyKey
		}

		result, changes, err := fn(&next)
		if err != nil {
			return err
		}
		changes = s.dirty.merge(changes)
		written = s.repo != nil && !changes.empty()
		if written {
			errSave = s.repo.Save(&next, changes)
			if errSave != nil {
				return fmt.Errorf("保存配置失败：%w", errSave)
			}
			s.dirty = Changes{}
		}
		s.state = &next
		if changes.AllKeys {
			s.blocked.reset()
		} else {
			for _, scope := range changes.Keys {
				s.blocked.clear(scope)
			}
		}
		value = result
		return nil
	}()
	if errSave != nil {
		s.recordWriteError(errSave)
	} else if written {
		s.recordWriteSuccess()
	}
	return value, err
}

func (s *Store) recordWriteSuccess() {
	s.errMu.Lock()
	recovered := s.lastError != ""
	s.lastError = ""
	s.errMu.Unlock()
	if recovered {
		s.AddPluginLog(PluginLogInfo, "计费数据库写入已恢复")
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
		s.AddPluginLog(PluginLogError, "保存计费数据失败：%v", err)
	}
}

// Hold the read lock until the query ends so Configure cannot close its database.
func withRepository[T any](s *Store, fn func(Repository) (T, error)) (T, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.repo == nil {
		var zero T
		return zero, nil
	}
	return fn(s.repo)
}

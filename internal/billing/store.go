package billing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultFlushInterval is the shortest gap between two writes of the state
// document. Writes are debounced rather than synchronous because billing
// updates happen on the request path and must not block it on I/O.
const DefaultFlushInterval = 5 * time.Second

// Store owns the in-memory state document and its persistence.
//
// Locking: cfgMu serializes Configure/Close against each other; mu guards the
// state document and the active config. Disk writes happen outside mu, against
// a snapshot taken under it.
//
// This type deliberately runs no goroutines, and neither does the rest of the
// plugin. It is compiled into a c-shared library that CLIProxyAPI dlopens, so
// it carries its own Go runtime inside a process that already has one. Two Go
// runtimes in one address space only coexist while the second one is inert
// between host calls: a runtime that keeps its own timers, GC cycles, and
// preemption signals running concurrently with the host's took the whole proxy
// down with "fatal error: bad flushGen" in runtime.(*mcache).prepareForSweep.
// Persistence is therefore driven by FlushIfDue on the host calls that are not
// on a client's critical path. Do not reintroduce a background flusher.
type Store struct {
	cfgMu sync.Mutex
	// writeMu and stateGeneration keep a flush based on a stale state snapshot
	// from writing after reconfiguration replaces that state.
	writeMu         sync.Mutex
	stateGeneration atomic.Uint64

	mu    sync.RWMutex
	state *State
	cfg   Config
	path  string

	// pending tracks in-flight requests between interception and their
	// terminal event. It is runtime-only and never persisted.
	pending *pendingTable

	dirty atomic.Bool

	usageUnpriced     atomic.Int64
	usageNoTokens     atomic.Int64
	usageUnclassified atomic.Int64

	statusMu  sync.Mutex
	lastFlush time.Time
	lastError string

	flushInterval time.Duration

	// now is injectable so period math and timestamps are testable.
	now func() time.Time
}

func NewStore() *Store {
	return &Store{
		state:         NewState(),
		cfg:           DefaultConfig(),
		pending:       &pendingTable{entries: make(map[string]PendingRequest)},
		flushInterval: DefaultFlushInterval,
		now:           time.Now,
	}
}

func (s *Store) Now() time.Time {
	return s.now()
}

// Configure applies a new plugin configuration, reloading the state document
// when the target path changes. It is safe to call repeatedly: the host invokes
// it on every plugin.reconfigure.
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
		s.cfg = normalized
		s.mu.Unlock()
		return nil
	}

	// Read the incoming document before touching anything, so a switch to an
	// unreadable path leaves the current document live.
	loaded, errLoad := loadState(path)
	if errLoad != nil {
		return errLoad
	}

	// Serialize the final write with Flush, then publish the new document,
	// config and path under one state lock. The generation change invalidates a
	// Flush that captured the previous state before writeMu was acquired.
	s.writeMu.Lock()
	s.mu.Lock()
	errSwitch := s.persistLocked(currentPath)
	if errSwitch == nil {
		s.state = loaded
		s.cfg = normalized
		s.path = path
		s.stateGeneration.Add(1)
		s.dirty.Store(false)
	}
	s.mu.Unlock()
	s.writeMu.Unlock()
	if errSwitch != nil {
		return fmt.Errorf("切换到 %s 前保存原状态失败：%w", path, errSwitch)
	}

	return nil
}

// Close performs the final write. The host calls it through the C ABI shutdown
// hook, which is the last chance to persist anything the debounce held back.
func (s *Store) Close() {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if errFlush := s.Flush(); errFlush != nil {
		s.recordFlushError(errFlush)
	}
}

// Enabled reports whether the plugin should act on requests. A disabled plugin
// still answers Management API calls so an operator can inspect it.
func (s *Store) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Enabled
}

// Read runs fn against the state under a read lock. fn must not mutate.
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

// Update runs fn against the state under a write lock and marks the document
// dirty so the flusher persists it.
func (s *Store) Update(fn func(*State)) {
	s.mu.Lock()
	fn(s.state)
	s.mu.Unlock()
	s.dirty.Store(true)
}

// updateResult marks the document dirty only when fn reports a change.
func updateResult[T any](s *Store, fn func(*State) (T, bool)) T {
	s.mu.Lock()
	value, changed := fn(s.state)
	s.mu.Unlock()
	if changed {
		s.dirty.Store(true)
	}
	return value
}

// Flush writes the document to disk when it has pending changes. It is a no-op
// for a clean document or an unconfigured store.
func (s *Store) Flush() error {
	if !s.dirty.CompareAndSwap(true, false) {
		return nil
	}
	generation := s.stateGeneration.Load()
	s.mu.RLock()
	path := s.path
	raw, errMarshal := json.MarshalIndent(s.state, "", "  ")
	s.mu.RUnlock()
	if errMarshal != nil {
		s.dirty.Store(true)
		return fmt.Errorf("编码计费状态：%w", errMarshal)
	}
	if path == "" {
		s.dirty.Store(true)
		return fmt.Errorf("尚未配置状态文件路径")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if generation != s.stateGeneration.Load() {
		return nil
	}
	if errWrite := writeFileAtomic(path, raw); errWrite != nil {
		// Keep the document dirty so a transient failure is retried on the next
		// host call instead of dropping the accumulated spend.
		s.dirty.Store(true)
		s.recordFlushError(errWrite)
		return errWrite
	}
	s.statusMu.Lock()
	s.lastFlush = s.Now()
	s.lastError = ""
	s.statusMu.Unlock()
	return nil
}

// persistLocked retires the current state file during a path change. The caller
// holds both writeMu and mu, so the write and state swap cannot interleave with
// usage updates or a Flush based on an older snapshot. It always writes the
// document because dirty may already be false while such a Flush waits.
func (s *Store) persistLocked(path string) error {
	if path == "" {
		return nil
	}
	raw, errMarshal := json.MarshalIndent(s.state, "", "  ")
	if errMarshal != nil {
		s.dirty.Store(true)
		return fmt.Errorf("编码计费状态：%w", errMarshal)
	}
	if errWrite := writeFileAtomic(path, raw); errWrite != nil {
		s.dirty.Store(true)
		s.recordFlushError(errWrite)
		return errWrite
	}
	s.dirty.Store(false)
	s.statusMu.Lock()
	s.lastFlush = s.Now()
	s.lastError = ""
	s.statusMu.Unlock()
	return nil
}

// FlushIfDue writes the document when it has pending changes and the debounce
// window has elapsed since the last successful write.
//
// This is the whole persistence schedule. There is no timer: see the note on
// Store for why this library must stay inert between host calls. The plugin
// calls it after the host RPCs that are off a client's critical path, so a
// change is normally on disk within one request of being made. A change made
// inside the debounce window waits for the next such call, and Close catches
// whatever is still held back at shutdown.
func (s *Store) FlushIfDue() {
	if !s.dirty.Load() {
		return
	}
	s.statusMu.Lock()
	last := s.lastFlush
	s.statusMu.Unlock()
	if !last.IsZero() && s.Now().Sub(last) < s.flushInterval {
		return
	}
	if errFlush := s.Flush(); errFlush != nil {
		s.recordFlushError(errFlush)
	}
}

func (s *Store) recordFlushError(err error) {
	s.statusMu.Lock()
	s.lastError = err.Error()
	s.statusMu.Unlock()
}

// Status is the payload of the /status Management API route.
//
// LastFlushedAt uses `omitzero` rather than `omitempty`: the latter does not
// suppress a zero time.Time, and reporting "0001-01-01" for "never flushed"
// reads as a bug.
type Status struct {
	Plugin    string `json:"plugin"`
	Version   string `json:"version"`
	Enabled   bool   `json:"enabled"`
	StateFile string `json:"state_file"`
	Prices    int    `json:"prices"`
	Plans     int    `json:"plans"`
	Keys      int    `json:"keys"`
	BoundKeys int    `json:"bound_keys"`
	// LogRetained is how many billing log entries are held and LogEntries is how
	// many the config allows. Zero for the latter means the log is switched off.
	LogRetained   int       `json:"log_retained"`
	LogEntries    int       `json:"log_entries"`
	PendingWrite  bool      `json:"pending_write"`
	LastFlushedAt time.Time `json:"last_flushed_at,omitzero"`
	LastError     string    `json:"last_error,omitempty"`
	Counters      Counters  `json:"counters"`
}

// Counters are since-restart tallies shown above the billing log.
type Counters struct {
	UsageUnpriced     int64 `json:"usage_unpriced"`
	UsageNoTokens     int64 `json:"usage_no_tokens"`
	UsageUnclassified int64 `json:"usage_unclassified"`
	PendingRequests   int   `json:"pending_requests"`
}

// Status snapshots the state exposed by the Management API.
func (s *Store) Status(pluginName, version string) Status {
	s.mu.RLock()
	status := Status{
		Plugin:      pluginName,
		Version:     version,
		Enabled:     s.cfg.Enabled,
		StateFile:   s.path,
		Prices:      len(s.state.Prices),
		Plans:       len(s.state.Plans),
		Keys:        len(s.state.Keys),
		LogRetained: len(s.state.Log),
		LogEntries:  s.cfg.LogEntries,
	}
	for _, key := range s.state.Keys {
		if key != nil && key.PlanID != "" {
			status.BoundKeys++
		}
	}
	s.mu.RUnlock()

	status.PendingWrite = s.dirty.Load()
	status.Counters = Counters{
		UsageUnpriced:     s.usageUnpriced.Load(),
		UsageNoTokens:     s.usageNoTokens.Load(),
		UsageUnclassified: s.usageUnclassified.Load(),
	}
	status.Counters.PendingRequests = s.pending.len()
	s.statusMu.Lock()
	status.LastFlushedAt = s.lastFlush
	status.LastError = s.lastError
	s.statusMu.Unlock()
	return status
}

func resolveStatePath(path string) (string, error) {
	if path == "" {
		path = DefaultStateFile
	}
	absolute, errAbs := filepath.Abs(path)
	if errAbs != nil {
		return "", fmt.Errorf("解析状态文件路径 %q：%w", path, errAbs)
	}
	return absolute, nil
}

// loadState reads the document at path. A missing file is not an error: it
// yields a fresh document so a first run bootstraps cleanly.
func loadState(path string) (*State, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return NewState(), nil
		}
		return nil, fmt.Errorf("读取状态文件 %s：%w", path, errRead)
	}
	if len(raw) == 0 {
		return NewState(), nil
	}
	state := NewState()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(state); errDecode != nil {
		return nil, fmt.Errorf("解析状态文件 %s：%w", path, errDecode)
	}
	if errDecode := decoder.Decode(&struct{}{}); errDecode != io.EOF {
		return nil, fmt.Errorf("解析状态文件 %s：文档包含多余内容", path)
	}
	if state.Version != StateVersion {
		return nil, fmt.Errorf("状态文件 %s 的格式版本为 %d，当前插件需要版本 %d", path, state.Version, StateVersion)
	}
	state.normalize()
	return state, nil
}

// writeFileAtomic writes through a sibling temp file and renames, so a crash or
// a concurrent reader never observes a partial document.
func writeFileAtomic(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
		return fmt.Errorf("创建文件目录 %s：%w", dir, errMkdir)
	}
	tmp, errTemp := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if errTemp != nil {
		return fmt.Errorf("在 %s 创建临时文件：%w", dir, errTemp)
	}
	tmpName := tmp.Name()
	// Removing the temp file is best effort on every failure path: a leftover
	// is inert, and it is the rename below that decides correctness.
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, errWrite := tmp.Write(raw); errWrite != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("写入临时文件 %s：%w", tmpName, errWrite)
	}
	if errSync := tmp.Sync(); errSync != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("同步临时文件 %s：%w", tmpName, errSync)
	}
	if errClose := tmp.Close(); errClose != nil {
		cleanup()
		return fmt.Errorf("关闭临时文件 %s：%w", tmpName, errClose)
	}
	if errChmod := os.Chmod(tmpName, 0o600); errChmod != nil {
		cleanup()
		return fmt.Errorf("设置临时文件 %s 权限：%w", tmpName, errChmod)
	}
	if errRename := os.Rename(tmpName, path); errRename != nil {
		cleanup()
		return fmt.Errorf("替换文件 %s：%w", path, errRename)
	}
	return nil
}

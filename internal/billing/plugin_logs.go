package billing

import (
	"fmt"
	"time"
)

type PluginLogLevel string

const (
	PluginLogDebug PluginLogLevel = "debug"
	PluginLogInfo  PluginLogLevel = "info"
	PluginLogError PluginLogLevel = "error"
)

const PluginLogRetention = 30 * 24 * time.Hour

type PluginLog struct {
	ID      int64          `json:"id"`
	At      time.Time      `json:"at"`
	Level   PluginLogLevel `json:"level"`
	Message string         `json:"message"`
}

type PluginLogQuery struct {
	Levels   []PluginLogLevel
	BeforeID int64
	Limit    int
	Since    time.Time
}

type PluginLogPage struct {
	Entries      []PluginLog `json:"entries"`
	NextBeforeID int64       `json:"next_before_id,omitempty"`
}

// Event tolerates a nil store because the panic handler reports through it; a
// diagnostics sink that can itself fail is worse than no diagnostics. For the
// same reason a database that refuses the line drops it rather than reporting
// the refusal, which would be another write to the same database.
func (s *Store) AddPluginLog(level PluginLogLevel, format string, args ...any) {
	if s == nil {
		return
	}
	entry := PluginLog{At: s.Now(), Level: level, Message: fmt.Sprintf(format, args...)}
	_, _ = withRepository(s, func(repo Repository) (struct{}, error) {
		return struct{}{}, repo.AppendPluginLog(entry, entry.At.Add(-PluginLogRetention))
	})
}

func (s *Store) PluginLogsPage(query PluginLogQuery) (PluginLogPage, error) {
	retention := s.Now().Add(-PluginLogRetention)
	if query.Since.IsZero() || query.Since.Before(retention) {
		query.Since = retention
	}
	return withRepository(s, func(repo Repository) (PluginLogPage, error) {
		return repo.PluginLogsPage(query)
	})
}

func (s *Store) ClearPluginLogs() (int, error) {
	return withRepository(s, func(repo Repository) (int, error) {
		return repo.ClearPluginLogs()
	})
}

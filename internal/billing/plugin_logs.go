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
	Entries      []PluginLog            `json:"entries"`
	LevelCounts  map[PluginLogLevel]int `json:"level_counts"`
	NextBeforeID int64                  `json:"next_before_id,omitempty"`
}

// Panics may be reported before the store exists, and a failed write must not log again.
func (s *Store) AddPluginLog(level PluginLogLevel, format string, args ...any) {
	if s == nil {
		return
	}
	_, _ = withRepository(s, func(repo Repository) (struct{}, error) {
		if level == PluginLogDebug && !s.cfg.Debug {
			return struct{}{}, nil
		}
		entry := PluginLog{At: s.Now(), Level: level, Message: fmt.Sprintf(format, args...)}
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

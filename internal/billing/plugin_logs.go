package billing

import (
	"fmt"
	"time"
)

type PluginLogLevel string

const (
	PluginLogInfo  PluginLogLevel = "info"
	PluginLogError PluginLogLevel = "error"
)

const PluginLogRetention = 30 * 24 * time.Hour

// PluginLog is one operational line kept for PluginLogRetention and bounded
// by nothing else: the log records occasional operational events — a reload, a
// failing disk — and dropping the oldest to make room for the newest would hide
// exactly the onset an operator is looking for.
type PluginLog struct {
	At      time.Time      `json:"at"`
	Level   PluginLogLevel `json:"level"`
	Message string         `json:"message"`
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

func (s *Store) PluginLogs() ([]PluginLog, error) {
	entries, err := withRepository(s, func(repo Repository) ([]PluginLog, error) {
		return repo.PluginLogs(s.Now().Add(-PluginLogRetention))
	})
	if entries == nil {
		entries = []PluginLog{}
	}
	return entries, err
}

func (s *Store) ClearPluginLogs() (int, error) {
	return withRepository(s, func(repo Repository) (int, error) {
		return repo.ClearPluginLogs()
	})
}

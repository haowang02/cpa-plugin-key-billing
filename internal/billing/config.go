package billing

import (
	"fmt"
	"strings"
	"time"

	// Embed the IANA time zone database so period boundaries resolve on hosts
	// that ship without tzdata (scratch/alpine containers are the common case).
	_ "time/tzdata"

	"gopkg.in/yaml.v3"
)

// DefaultStateFile is where the plugin persists its state when unconfigured.
// It is resolved relative to the CPA process working directory.
const DefaultStateFile = "plugins/cpa-key-billing-state.json"

const DefaultTimezone = "UTC"

// DefaultLogEntries is how many billed requests the log keeps when the config
// says nothing. It is modest on purpose: the log lives inside the state
// document, which is rewritten whole on every flush, so retention is paid for in
// disk writes for as long as traffic keeps arriving.
const DefaultLogEntries = 200

// MaxLogEntries caps what an operator may ask for. Past this the document stops
// being something that can be loaded at startup and rewritten every few seconds.
const MaxLogEntries = 5000

type Config struct {
	// Enabled is injected by the host, which only loads a plugin whose config
	// sets it, so in practice it is always true by the time this is decoded.
	// It is still honoured rather than assumed: gating on it costs one atomic
	// read and keeps the plugin inert if the host ever does deliver a disabled
	// config, whereas assuming it would mean billing traffic an operator
	// believes they turned off.
	Enabled bool `yaml:"enabled"`
	// StateFile is the JSON document holding prices, plans, bindings, and stats.
	StateFile string `yaml:"state_file"`
	// DefaultTimezone anchors every plan's period boundaries. Plans carry no
	// zone of their own, so this is the single deployment-wide answer to "when
	// does the day roll over".
	DefaultTimezone string `yaml:"default_timezone"`
	// LogEntries is how many billed requests the log retains. Once it is full,
	// recording one more discards the oldest.
	//
	// Zero really means zero, which is how the log is turned off: an omitted key
	// leaves DefaultConfig's value in place, so only an operator who wrote 0 gets
	// no log. Existing entries are dropped the first time a request is billed
	// under the smaller setting, rather than on reconfigure, so lowering it in a
	// deployment that is not serving anything leaves the history readable.
	LogEntries int `yaml:"log_entries"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:         false,
		StateFile:       DefaultStateFile,
		DefaultTimezone: DefaultTimezone,
		LogEntries:      DefaultLogEntries,
	}
}

func DecodeConfig(raw []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(strings.TrimSpace(string(raw))) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return Config{}, fmt.Errorf("解析插件配置：%w", errUnmarshal)
		}
	}
	return cfg.normalized()
}

func (c Config) normalized() (Config, error) {
	c.StateFile = strings.TrimSpace(c.StateFile)
	if c.StateFile == "" {
		c.StateFile = DefaultStateFile
	}
	c.DefaultTimezone = strings.TrimSpace(c.DefaultTimezone)
	if c.DefaultTimezone == "" {
		c.DefaultTimezone = DefaultTimezone
	}
	if _, errLoad := time.LoadLocation(c.DefaultTimezone); errLoad != nil {
		return Config{}, fmt.Errorf("默认时区 %q 无效：%w", c.DefaultTimezone, errLoad)
	}
	// A nonsensical retention is clamped rather than rejected: it costs disk and
	// nothing else, and refusing to load the plugin over it would take billing
	// down with the typo.
	if c.LogEntries < 0 {
		c.LogEntries = 0
	}
	if c.LogEntries > MaxLogEntries {
		c.LogEntries = MaxLogEntries
	}
	return c, nil
}

// Location resolves the configured time zone. Callers may rely on a non-nil
// result because normalized() already validated the name.
func (c Config) Location() *time.Location {
	loc, errLoad := time.LoadLocation(c.DefaultTimezone)
	if errLoad != nil || loc == nil {
		return time.UTC
	}
	return loc
}

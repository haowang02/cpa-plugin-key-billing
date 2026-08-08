package billing

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const DefaultStateFile = "plugins/cpa-key-billing-state.json"

// DefaultLogEntries is how many billed requests the log keeps when the config
// says nothing. It is modest on purpose: the log lives inside the state
// document, which is rewritten whole on every flush, so retention is paid for in
// disk writes for as long as traffic keeps arriving.
const DefaultLogEntries = 200

const MaxLogEntries = 5000

type Config struct {
	Enabled   bool   `yaml:"enabled"`
	StateFile string `yaml:"state_file"`
	// Zero disables request-log retention.
	LogEntries int `yaml:"log_entries"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:    false,
		StateFile:  DefaultStateFile,
		LogEntries: DefaultLogEntries,
	}
}

func DecodeConfig(raw []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(strings.TrimSpace(string(raw))) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return Config{}, fmt.Errorf("解析插件配置：%w", errUnmarshal)
		}
	}
	return cfg.normalized(), nil
}

func (c Config) normalized() Config {
	c.StateFile = strings.TrimSpace(c.StateFile)
	if c.StateFile == "" {
		c.StateFile = DefaultStateFile
	}
	if c.LogEntries < 0 {
		c.LogEntries = 0
	}
	if c.LogEntries > MaxLogEntries {
		c.LogEntries = MaxLogEntries
	}
	return c
}

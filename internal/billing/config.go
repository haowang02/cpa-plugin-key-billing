package billing

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

const DefaultStateFile = "plugins/cpa-key-billing-state-v1.db"

type Config struct {
	Enabled   bool   `yaml:"enabled"`
	StateFile string `yaml:"state_file"`
}

// Priority and Store belong to the host and are ignored by this plugin.
type configDocument struct {
	Enabled   bool      `yaml:"enabled"`
	StateFile string    `yaml:"state_file"`
	Priority  int       `yaml:"priority"`
	Store     yaml.Node `yaml:"store"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:   false,
		StateFile: DefaultStateFile,
	}
}

func DecodeConfig(raw []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(bytes.TrimSpace(raw)) > 0 {
		document := configDocument{Enabled: cfg.Enabled, StateFile: cfg.StateFile}
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if errDecode := decoder.Decode(&document); errDecode != nil {
			return Config{}, fmt.Errorf("解析插件配置：%w", errDecode)
		}
		if errTrailing := decoder.Decode(&struct{}{}); errTrailing != io.EOF {
			return Config{}, fmt.Errorf("解析插件配置：只能包含一个 YAML 文档")
		}
		cfg.Enabled = document.Enabled
		cfg.StateFile = document.StateFile
	}
	return cfg.normalized(), nil
}

func (c Config) describe() string {
	if c.Enabled {
		return "已启用"
	}
	return "已停用"
}

func (c Config) normalized() Config {
	c.StateFile = strings.TrimSpace(c.StateFile)
	if c.StateFile == "" {
		c.StateFile = DefaultStateFile
	}
	return c
}

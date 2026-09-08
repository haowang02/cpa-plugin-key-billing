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
	Enabled                            bool   `yaml:"enabled"`
	Debug                              bool   `yaml:"debug"`
	StateFile                          string `yaml:"state_file"`
	CodexFastModeBilling               bool   `yaml:"codex_fast_mode_billing"`
	CodexFastModeBillingExcludedModels string `yaml:"codex_fast_mode_billing_excluded_models"`
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
		document := struct {
			Config `yaml:",inline"`
			// These fields belong to the host and are ignored by the plugin.
			Priority int       `yaml:"priority"`
			Store    yaml.Node `yaml:"store"`
		}{Config: cfg}
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if errDecode := decoder.Decode(&document); errDecode != nil {
			return Config{}, fmt.Errorf("解析插件配置：%w", errDecode)
		}
		if errTrailing := decoder.Decode(&struct{}{}); errTrailing != io.EOF {
			return Config{}, fmt.Errorf("解析插件配置：只能包含一个 YAML 文档")
		}
		cfg = document.Config
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

// Exclusions use billing model IDs and preserve routing prefixes.
func (c Config) excludesCodexFastBilling(model string) bool {
	model = NormalizeModelID(ModelWithoutThinkingSuffix(model))
	if model == "" {
		return false
	}
	for _, excluded := range strings.Split(c.CodexFastModeBillingExcludedModels, ",") {
		if model == NormalizeModelID(ModelWithoutThinkingSuffix(strings.TrimSpace(excluded))) {
			return true
		}
	}
	return false
}

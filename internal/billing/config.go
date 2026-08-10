package billing

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const DefaultStateFile = "plugins/cpa-key-billing-state.json"

type Config struct {
	Enabled   bool   `yaml:"enabled"`
	StateFile string `yaml:"state_file"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:   false,
		StateFile: DefaultStateFile,
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

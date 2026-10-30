package billing

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultStateFile = "plugins/cpa-key-billing-state-v1.db"

const maxRetentionDays = RetentionDays((1<<63 - 1) / (24 * time.Hour))

type Config struct {
	Enabled                   bool          `yaml:"enabled"`
	Debug                     bool          `yaml:"debug"`
	StateFile                 string        `yaml:"state_file"`
	CodexFastModeBilling      bool          `yaml:"codex_fast_mode_billing"`
	RequestEventRetentionDays RetentionDays `yaml:"request_event_retention_days"`
	AllowUnpricedModels       bool          `yaml:"allow_unpriced_models"`
	CNYPerUSD                 ExchangeRate  `yaml:"cny_per_usd"`
	MaskAPIKeyViewEmails      bool          `yaml:"mask_api_key_view_emails"`
	AllowAPIKeyQuotaReset     bool          `yaml:"allow_api_key_quota_reset"`
	Routing                   RoutingConfig `yaml:"routing"`
}

type RetentionDays int

func (d *RetentionDays) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag != "!!int" {
		return fmt.Errorf("request_event_retention_days must be an integer from 1 to 106751")
	}
	var value int
	if err := node.Decode(&value); err != nil {
		return err
	}
	*d = RetentionDays(value)
	return nil
}

type RoutingConfig struct {
	Strategy           string `yaml:"strategy" json:"strategy"`
	SessionAffinity    bool   `yaml:"session-affinity" json:"session-affinity"`
	SessionAffinityTTL string `yaml:"session-affinity-ttl" json:"session-affinity-ttl"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:                   false,
		StateFile:                 DefaultStateFile,
		RequestEventRetentionDays: 365,
		CNYPerUSD:                 DefaultExchangeRate,
		Routing:                   RoutingConfig{Strategy: "round-robin", SessionAffinityTTL: "1h"},
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
			return Config{}, fmt.Errorf("Parse plugin configuration: %w", errDecode)
		}
		if errTrailing := decoder.Decode(&struct{}{}); errTrailing != io.EOF {
			return Config{}, fmt.Errorf("Plugin configuration must contain exactly one YAML document")
		}
		cfg = document.Config
	}
	return cfg.normalized()
}

func (c Config) describe() string {
	if c.Enabled {
		return "enabled"
	}
	return "disabled"
}

func (c Config) normalized() (Config, error) {
	c.StateFile = strings.TrimSpace(c.StateFile)
	if c.StateFile == "" {
		c.StateFile = DefaultStateFile
	}
	if c.RequestEventRetentionDays < 1 || c.RequestEventRetentionDays > maxRetentionDays {
		return Config{}, fmt.Errorf("request_event_retention_days must be an integer from 1 to 106751")
	}
	if !c.CNYPerUSD.valid() {
		return Config{}, fmt.Errorf("cny_per_usd must be a positive number")
	}
	switch strings.ToLower(strings.TrimSpace(c.Routing.Strategy)) {
	case "", "round-robin", "roundrobin", "rr":
		c.Routing.Strategy = "round-robin"
	case "weighted-round-robin", "weightedroundrobin", "wrr":
		c.Routing.Strategy = "weighted-round-robin"
	case "fill-first", "fillfirst", "ff":
		c.Routing.Strategy = "fill-first"
	default:
		return Config{}, fmt.Errorf("routing.strategy must be round-robin, weighted-round-robin, or fill-first")
	}
	c.Routing.SessionAffinityTTL = strings.TrimSpace(c.Routing.SessionAffinityTTL)
	if c.Routing.SessionAffinityTTL == "" {
		c.Routing.SessionAffinityTTL = "1h"
	}
	ttl, err := time.ParseDuration(c.Routing.SessionAffinityTTL)
	if err != nil || ttl <= 0 {
		return Config{}, fmt.Errorf("routing.session-affinity-ttl must be a positive duration")
	}
	if ttl < time.Second {
		c.Routing.SessionAffinityTTL = "1s"
	}
	return c, nil
}

func (d RetentionDays) cutoff(now time.Time) time.Time {
	if d == 0 {
		return time.Time{}
	}
	return now.Add(-time.Duration(d) * 24 * time.Hour)
}

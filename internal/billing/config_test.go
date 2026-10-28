package billing

import "testing"

func TestDecodeConfigDefaults(t *testing.T) {
	cfg, errDecode := DecodeConfig([]byte("enabled: true\npriority: 10\nstore:\n  id: cpa-key-billing\n  version: 0.5.1\n"))
	if errDecode != nil {
		t.Fatalf("DecodeConfig: %v", errDecode)
	}
	if !cfg.Enabled || cfg.Debug || cfg.CodexFastModeBilling || cfg.AllowUnpricedModels || cfg.MaskAPIKeyViewEmails || cfg.AllowAPIKeyQuotaReset ||
		cfg.CNYPerUSD != 7 || cfg.StateFile != DefaultStateFile {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.Routing != DefaultConfig().Routing {
		t.Fatalf("routing defaults = %+v", cfg.Routing)
	}
	cfg, errDecode = DecodeConfig([]byte("routing:\n  strategy: WRR\n  session-affinity: true\n  session-affinity-ttl: 30m\n"))
	if errDecode != nil || cfg.Routing.Strategy != "weighted-round-robin" || !cfg.Routing.SessionAffinity || cfg.Routing.SessionAffinityTTL != "30m" {
		t.Fatalf("routing = %+v, error = %v", cfg.Routing, errDecode)
	}
	cfg, errDecode = DecodeConfig([]byte("enabled: true\ndebug: true\ncodex_fast_mode_billing: true\nallow_unpriced_models: true\n" +
		"mask_api_key_view_emails: true\nallow_api_key_quota_reset: true\ncny_per_usd: 7.2\n"))
	if errDecode != nil || !cfg.Debug || !cfg.CodexFastModeBilling || !cfg.AllowUnpricedModels || !cfg.MaskAPIKeyViewEmails ||
		!cfg.AllowAPIKeyQuotaReset || cfg.CNYPerUSD != 7.2 {
		t.Fatalf("config = %+v, error = %v", cfg, errDecode)
	}
}

func TestDecodeConfigRejectsUnknownFieldsAndExtraDocuments(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown field":         "enable: true\n",
		"extra document":        "enabled: true\n---\nenabled: false\n",
		"unknown strategy":      "routing:\n  strategy: random\n",
		"unknown routing field": "routing:\n  session-affinity-subagents: true\n",
		"invalid ttl":           "routing:\n  session-affinity-ttl: tomorrow\n",
		"zero ttl":              "routing:\n  session-affinity-ttl: 0s\n",
		"zero exchange rate":    "cny_per_usd: 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, errDecode := DecodeConfig([]byte(raw)); errDecode == nil {
				t.Fatal("DecodeConfig accepted invalid configuration")
			}
		})
	}
}

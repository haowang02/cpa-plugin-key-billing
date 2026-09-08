package billing

import "testing"

func TestDecodeConfigDefaults(t *testing.T) {
	cfg, errDecode := DecodeConfig([]byte("enabled: true\npriority: 10\nstore:\n  id: cpa-key-billing\n  version: 0.5.1\n"))
	if errDecode != nil {
		t.Fatalf("DecodeConfig: %v", errDecode)
	}
	if !cfg.Enabled || cfg.Debug || cfg.CodexFastModeBilling || cfg.CodexFastModeBillingExcludedModels != "" || cfg.StateFile != DefaultStateFile {
		t.Fatalf("config = %+v", cfg)
	}
	cfg, errDecode = DecodeConfig([]byte("enabled: true\ndebug: true\ncodex_fast_mode_billing: true\n"))
	if errDecode != nil || !cfg.Debug || !cfg.CodexFastModeBilling {
		t.Fatalf("config = %+v, error = %v", cfg, errDecode)
	}
}

func TestDecodeCodexFastBillingExclusions(t *testing.T) {
	cfg, errDecode := DecodeConfig([]byte("codex_fast_mode_billing: true\ncodex_fast_mode_billing_excluded_models: 'fast-alias, Route/SECOND , , '\n"))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	for _, test := range []struct {
		model    string
		excluded bool
	}{
		{"fast-alias", true}, {"FAST-ALIAS(high)", true}, {"route/second", true},
		{"second", false}, {"prefix/fast-alias", false}, {"fast-alias-other", false}, {"", false},
	} {
		if got := cfg.excludesCodexFastBilling(test.model); got != test.excluded {
			t.Fatalf("model %q: excluded=%t, want %t", test.model, got, test.excluded)
		}
	}
	cfg, errDecode = DecodeConfig([]byte("codex_fast_mode_billing_excluded_models: ''\n"))
	if errDecode != nil || cfg.excludesCodexFastBilling("fast-alias") {
		t.Fatalf("empty exclusions: %+v, error=%v", cfg, errDecode)
	}
}

func TestDecodeConfigRejectsUnknownFieldsAndExtraDocuments(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown field":  "enable: true\n",
		"extra document": "enabled: true\n---\nenabled: false\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, errDecode := DecodeConfig([]byte(raw)); errDecode == nil {
				t.Fatal("DecodeConfig accepted invalid configuration")
			}
		})
	}
}

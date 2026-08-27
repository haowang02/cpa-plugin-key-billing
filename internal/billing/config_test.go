package billing

import "testing"

func TestDecodeConfigDefaults(t *testing.T) {
	cfg, errDecode := DecodeConfig([]byte("enabled: true\npriority: 10\nstore:\n  id: cpa-key-billing\n  version: 0.5.1\n"))
	if errDecode != nil {
		t.Fatalf("DecodeConfig: %v", errDecode)
	}
	if !cfg.Enabled || cfg.StateFile != DefaultStateFile {
		t.Fatalf("config = %+v", cfg)
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

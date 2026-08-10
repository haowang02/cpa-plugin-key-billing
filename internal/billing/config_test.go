package billing

import "testing"

func TestDecodeConfigDefaults(t *testing.T) {
	cfg, errDecode := DecodeConfig([]byte("enabled: true\n"))
	if errDecode != nil {
		t.Fatalf("DecodeConfig: %v", errDecode)
	}
	if !cfg.Enabled || cfg.StateFile != DefaultStateFile {
		t.Fatalf("config = %+v", cfg)
	}
}

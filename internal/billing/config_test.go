package billing

import "testing"

func TestDecodeConfigDefaults(t *testing.T) {
	cfg, errDecode := DecodeConfig([]byte("enabled: true\n"))
	if errDecode != nil {
		t.Fatalf("DecodeConfig: %v", errDecode)
	}
	if !cfg.Enabled || cfg.StateFile != DefaultStateFile || cfg.LogEntries != DefaultLogEntries {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestDecodeConfigNormalizesPathAndLogLimit(t *testing.T) {
	cfg, errDecode := DecodeConfig([]byte("state_file: '   '\nlog_entries: 999999\n"))
	if errDecode != nil {
		t.Fatalf("DecodeConfig: %v", errDecode)
	}
	if cfg.StateFile != DefaultStateFile || cfg.LogEntries != MaxLogEntries {
		t.Fatalf("config = %+v", cfg)
	}
	cfg, errDecode = DecodeConfig([]byte("log_entries: -1\n"))
	if errDecode != nil || cfg.LogEntries != 0 {
		t.Fatalf("negative log config = %+v, %v", cfg, errDecode)
	}
}

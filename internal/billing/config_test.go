package billing

import "testing"

func TestDecodeConfigDefaults(t *testing.T) {
	cfg, errDecode := DecodeConfig(nil)
	if errDecode != nil {
		t.Fatalf("DecodeConfig(nil) error = %v", errDecode)
	}
	if cfg.Enabled {
		t.Fatalf("Enabled = true, want false so an unconfigured plugin stays inert")
	}
	if cfg.StateFile != DefaultStateFile {
		t.Fatalf("StateFile = %q, want %q", cfg.StateFile, DefaultStateFile)
	}
	if cfg.DefaultTimezone != DefaultTimezone {
		t.Fatalf("DefaultTimezone = %q, want %q", cfg.DefaultTimezone, DefaultTimezone)
	}
}

func TestDecodeConfigAppliesHostValues(t *testing.T) {
	raw := []byte("enabled: true\npriority: 100\nstate_file: \"/tmp/billing.json\"\ndefault_timezone: \"Asia/Shanghai\"\n")
	cfg, errDecode := DecodeConfig(raw)
	if errDecode != nil {
		t.Fatalf("DecodeConfig error = %v", errDecode)
	}
	if !cfg.Enabled {
		t.Fatalf("Enabled = false, want true")
	}
	if cfg.StateFile != "/tmp/billing.json" {
		t.Fatalf("StateFile = %q", cfg.StateFile)
	}
	if cfg.Location().String() != "Asia/Shanghai" {
		t.Fatalf("Location = %q, want Asia/Shanghai", cfg.Location())
	}
}

func TestDecodeConfigBlankFieldsFallBackToDefaults(t *testing.T) {
	cfg, errDecode := DecodeConfig([]byte("enabled: true\nstate_file: \"   \"\ndefault_timezone: \"\"\n"))
	if errDecode != nil {
		t.Fatalf("DecodeConfig error = %v", errDecode)
	}
	if cfg.StateFile != DefaultStateFile {
		t.Fatalf("StateFile = %q, want %q", cfg.StateFile, DefaultStateFile)
	}
	if cfg.DefaultTimezone != DefaultTimezone {
		t.Fatalf("DefaultTimezone = %q, want %q", cfg.DefaultTimezone, DefaultTimezone)
	}
}

func TestDecodeConfigRejectsUnknownTimezone(t *testing.T) {
	if _, errDecode := DecodeConfig([]byte("default_timezone: \"Mars/Olympus\"\n")); errDecode == nil {
		t.Fatal("DecodeConfig accepted an unknown time zone, want an error")
	}
}

func TestDecodeConfigRejectsMalformedYAML(t *testing.T) {
	if _, errDecode := DecodeConfig([]byte("enabled: [unterminated\n")); errDecode == nil {
		t.Fatal("DecodeConfig accepted malformed YAML, want an error")
	}
}

// TestEmbeddedTimezoneDatabase guards the `time/tzdata` import. Without it a
// container image with no tzdata would silently fail every non-UTC plan.
func TestEmbeddedTimezoneDatabase(t *testing.T) {
	for _, zone := range []string{"Asia/Shanghai", "America/New_York", "Europe/Berlin"} {
		cfg, errDecode := DecodeConfig([]byte("default_timezone: \"" + zone + "\"\n"))
		if errDecode != nil {
			t.Fatalf("time zone %s unavailable: %v", zone, errDecode)
		}
		if cfg.Location().String() != zone {
			t.Fatalf("Location = %q, want %q", cfg.Location(), zone)
		}
	}
}

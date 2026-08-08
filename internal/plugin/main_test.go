package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	dir, errTemp := os.MkdirTemp("", "cpa-plugin-catalog-test-")
	if errTemp != nil {
		panic(errTemp)
	}
	raw, errRead := os.ReadFile(filepath.Join("..", "billing", "testdata", "catalog.json"))
	if errRead != nil {
		panic(errRead)
	}
	cache := filepath.Join(dir, "catalog.json")
	if errWrite := os.WriteFile(cache, raw, 0o600); errWrite != nil {
		panic(errWrite)
	}
	if errSet := os.Setenv("CPA_KEY_BILLING_CATALOG_CACHE", cache); errSet != nil {
		panic(errSet)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

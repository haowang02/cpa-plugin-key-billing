package billing

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	dir, errTemp := os.MkdirTemp("", "cpa-billing-catalog-test-")
	if errTemp != nil {
		panic(errTemp)
	}
	raw, errRead := os.ReadFile(filepath.Join("testdata", "catalog.json"))
	if errRead != nil {
		panic(errRead)
	}
	cache := filepath.Join(dir, catalogCacheName)
	if errWrite := os.WriteFile(cache, raw, 0o600); errWrite != nil {
		panic(errWrite)
	}
	if errSet := os.Setenv(catalogCacheEnv, cache); errSet != nil {
		panic(errSet)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

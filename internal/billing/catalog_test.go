package billing

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const unpricedModel = "cpa-key-billing-test-unpriced-model"

func TestCachedCatalogIsUsable(t *testing.T) {
	loaded := builtinCatalog()
	info := loaded.info
	rules := loaded.rules
	if info.Models < 5 || len(rules) != info.Models || info.FetchedAt.IsZero() || !strings.Contains(info.Source, "models.dev") {
		t.Fatalf("catalog = %+v with %d rules", info, len(rules))
	}
	for _, model := range []string{"gpt-4o", "claude-sonnet-4-5-20250929", "gemini-2.0-flash"} {
		rule, known := lookupBuiltin(model, "")
		if !known || rule.InputPer1M <= 0 || rule.OutputPer1M <= 0 {
			t.Fatalf("catalog price %q = %+v, %v", model, rule, known)
		}
	}
	if _, known := lookupBuiltin(unpricedModel, ""); known {
		t.Fatalf("test sentinel %q unexpectedly has a catalog price", unpricedModel)
	}
}

func TestCatalogCarriesSingleLongContextTier(t *testing.T) {
	rule, known := lookupBuiltin("gpt-5.6-sol", "")
	if !known || rule.LongContext == nil || rule.LongContext.ThresholdInputTokens != 272000 ||
		rule.LongContext.InputPer1M != 10 || rule.LongContext.OutputPer1M != 45 {
		t.Fatalf("tiered default = %+v, known=%v", rule, known)
	}
}

func TestSearchCatalogRanksExactMatchAndCapsResults(t *testing.T) {
	results := SearchCatalog(" GPT-4O ", 5)
	if len(results) == 0 || len(results) > 5 || results[0].Pattern != "gpt-4o" {
		t.Fatalf("SearchCatalog() = %+v", results)
	}
	if results := SearchCatalog("", 20); len(results) != 0 {
		t.Fatalf("empty search returned %d results", len(results))
	}
}

func TestCatalogBareNameUsesCanonicalProviderPrice(t *testing.T) {
	bare, known := lookupBuiltin("gpt-5.3-codex", "")
	canonical, canonicalKnown := lookupBuiltin("openai/gpt-5.3-codex", "")
	if !known || !canonicalKnown || !samePrice(bare, canonical) {
		t.Fatalf("bare = %+v (%v), canonical = %+v (%v)", bare, known, canonical, canonicalKnown)
	}
}

func TestCatalogLookupUsesUpstreamThenBillingModel(t *testing.T) {
	sample := builtinCatalog().rules[0]
	rule, ok := lookupBuiltin(strings.ToUpper(sample.Pattern), "")
	if !ok || rule.Pattern != sample.Pattern {
		t.Fatalf("lookup = %+v, %v", rule, ok)
	}
	if _, ok = lookupBuiltin(unpricedModel, sample.Pattern); !ok {
		t.Fatal("billing model lookup did not find the catalog row")
	}
}

func TestCatalogLookupDoesNotLoadOnTheRequestPath(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "request path must not download", http.StatusInternalServerError)
	}))
	defer server.Close()

	t.Setenv(catalogCacheEnv, filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv(catalogSourceEnv, server.URL)
	runtimeCatalog.Lock()
	savedLoaded := runtimeCatalog.loaded.Load()
	savedRetryAfter := runtimeCatalog.retryAfter
	savedLastError := runtimeCatalog.lastError
	runtimeCatalog.loaded.Store(nil)
	runtimeCatalog.retryAfter = time.Time{}
	runtimeCatalog.lastError = nil
	runtimeCatalog.Unlock()
	t.Cleanup(func() {
		runtimeCatalog.Lock()
		runtimeCatalog.loaded.Store(savedLoaded)
		runtimeCatalog.retryAfter = savedRetryAfter
		runtimeCatalog.lastError = savedLastError
		runtimeCatalog.Unlock()
	})

	state := NewState()
	if price := state.ResolvePrice("gpt-4o", "gpt-4o"); price.Source != PriceSourceNone {
		t.Fatalf("price = %+v, want an unresolved cold-cache price", price)
	}
	if price := state.ResolvePrice("gpt-4o", "gpt-4o"); price.Source != PriceSourceNone {
		t.Fatalf("second price = %+v, want an unresolved cold-cache price", price)
	}
	if requests != 0 {
		t.Fatalf("request-path price lookup downloaded the catalog %d times", requests)
	}
}

func TestCatalogEnsureHonorsDownloadCooldown(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	t.Setenv(catalogCacheEnv, filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv(catalogSourceEnv, server.URL)
	runtimeCatalog.Lock()
	savedLoaded := runtimeCatalog.loaded.Load()
	savedRetryAfter := runtimeCatalog.retryAfter
	savedLastError := runtimeCatalog.lastError
	runtimeCatalog.loaded.Store(nil)
	runtimeCatalog.retryAfter = time.Time{}
	runtimeCatalog.lastError = nil
	runtimeCatalog.Unlock()
	t.Cleanup(func() {
		runtimeCatalog.Lock()
		runtimeCatalog.loaded.Store(savedLoaded)
		runtimeCatalog.retryAfter = savedRetryAfter
		runtimeCatalog.lastError = savedLastError
		runtimeCatalog.Unlock()
	})

	if _, errEnsure := EnsureBuiltinCatalog(); errEnsure == nil {
		t.Fatal("first ensure unexpectedly succeeded")
	}
	if _, errEnsure := EnsureBuiltinCatalog(); errEnsure == nil {
		t.Fatal("cooldown ensure unexpectedly succeeded")
	}
	if requests != 1 {
		t.Fatalf("downloads during cooldown = %d, want 1", requests)
	}
}

func TestPriceOverridesBeatTheCatalog(t *testing.T) {
	sample := builtinCatalog().rules[0]
	state := NewState()
	state.Prices = []PriceRule{{Pattern: "*", InputPer1M: 7}}
	price := state.ResolvePrice(sample.Pattern, "")
	if price.Source != PriceSourceOverride || price.InputPer1M != 7 {
		t.Fatalf("price = %+v, want the operator override", price)
	}
}

func TestCatalogDownloadsToSystemCacheAndRedownloadsWhenDeleted(t *testing.T) {
	raw, errRead := os.ReadFile(filepath.Join("testdata", "catalog.json"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	t.Setenv(catalogCacheEnv, filepath.Join(t.TempDir(), "catalog.json"))
	t.Setenv(catalogSourceEnv, server.URL)
	runtimeCatalog.Lock()
	savedLoaded := runtimeCatalog.loaded.Load()
	savedRetryAfter := runtimeCatalog.retryAfter
	savedLastError := runtimeCatalog.lastError
	runtimeCatalog.loaded.Store(nil)
	runtimeCatalog.retryAfter = time.Time{}
	runtimeCatalog.lastError = nil
	runtimeCatalog.Unlock()
	t.Cleanup(func() {
		runtimeCatalog.Lock()
		runtimeCatalog.loaded.Store(savedLoaded)
		runtimeCatalog.retryAfter = savedRetryAfter
		runtimeCatalog.lastError = savedLastError
		runtimeCatalog.Unlock()
	})

	cache := CatalogCachePath()
	if _, errEnsure := EnsureBuiltinCatalog(); errEnsure != nil {
		t.Fatalf("first ensure: %v", errEnsure)
	}
	if requests != 1 {
		t.Fatalf("downloads = %d, want 1", requests)
	}
	if _, errStat := os.Stat(cache); errStat != nil {
		t.Fatalf("cache was not written: %v", errStat)
	}
	if _, errEnsure := EnsureBuiltinCatalog(); errEnsure != nil || requests != 1 {
		t.Fatalf("cache reuse: err=%v downloads=%d", errEnsure, requests)
	}
	if errRemove := os.Remove(cache); errRemove != nil {
		t.Fatal(errRemove)
	}
	if _, errEnsure := EnsureBuiltinCatalog(); errEnsure != nil || requests != 2 {
		t.Fatalf("redownload after delete: err=%v downloads=%d", errEnsure, requests)
	}
}

func TestRefreshPriceCatalogAdvancesBuiltinRowsAndPreservesCustomRows(t *testing.T) {
	initialRaw, errRead := os.ReadFile(filepath.Join("testdata", "catalog.json"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	refreshedRaw := []byte(strings.Replace(string(initialRaw),
		`"input":5,"output":15,"cache_read":2.5`,
		`"input":6,"output":18,"cache_read":3`, 1))
	if string(refreshedRaw) == string(initialRaw) {
		t.Fatal("test catalog replacement did not change the fixture")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(refreshedRaw)
	}))
	defer server.Close()

	cache := filepath.Join(t.TempDir(), "catalog.json")
	t.Setenv(catalogCacheEnv, cache)
	t.Setenv(catalogSourceEnv, server.URL)
	runtimeCatalog.Lock()
	savedLoaded := runtimeCatalog.loaded.Load()
	savedRetryAfter := runtimeCatalog.retryAfter
	savedLastError := runtimeCatalog.lastError
	runtimeCatalog.loaded.Store(nil)
	runtimeCatalog.retryAfter = time.Time{}
	runtimeCatalog.lastError = nil
	runtimeCatalog.Unlock()
	t.Cleanup(func() {
		runtimeCatalog.Lock()
		runtimeCatalog.loaded.Store(savedLoaded)
		runtimeCatalog.retryAfter = savedRetryAfter
		runtimeCatalog.lastError = savedLastError
		runtimeCatalog.Unlock()
	})

	if errWrite := os.WriteFile(cache, initialRaw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if _, errEnsure := EnsureBuiltinCatalog(); errEnsure != nil {
		t.Fatal(errEnsure)
	}

	store := newStore(t)
	store.state.Prices = []PriceRule{
		{Pattern: "gpt-4o", InputPer1M: 5, OutputPer1M: 15, CacheReadPer1M: floatPtr(2.5)},
		{Pattern: "gpt-5.3-codex", InputPer1M: 99, OutputPer1M: 101},
	}
	result, errRefresh := store.RefreshPriceCatalog()
	if errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if result.UpdatedModels != 1 {
		t.Fatalf("updated models = %d, want 1", result.UpdatedModels)
	}
	if got := store.state.Prices[0]; got.InputPer1M != 6 || got.OutputPer1M != 18 || got.CacheReadPer1M == nil || *got.CacheReadPer1M != 3 {
		t.Fatalf("built-in row = %+v, want refreshed prices", got)
	}
	if got := store.state.Prices[1]; got.InputPer1M != 99 || got.OutputPer1M != 101 {
		t.Fatalf("custom row = %+v, want preserved prices", got)
	}
}

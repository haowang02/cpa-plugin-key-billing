package billing

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryReferencePriceRepository struct {
	mu                      sync.Mutex
	metadata                ReferencePriceMetadata
	prices                  []ReferencePrice
	reads, imports, touches int
	fail                    error
}

func (r *memoryRepository) OpenReferencePrices() (ReferencePriceRepository, error) {
	if r.referencePrices == nil {
		raw, err := os.ReadFile("testdata/models_dev_prices.json")
		if err != nil {
			return nil, err
		}
		prices, err := parseModelsDevPrices(raw)
		if err != nil {
			return nil, err
		}
		r.referencePrices = &memoryReferencePriceRepository{
			metadata: ReferencePriceMetadata{
				SourceURL:   ModelsDevPricesURL,
				ContentHash: fmt.Sprintf("%x", sha256.Sum256(raw)),
				Version:     1,
				FetchedAt:   time.Now(),
				ModelCount:  len(prices),
				Usable:      true,
			},
			prices: prices,
		}
	}
	return r.referencePrices, nil
}

func (r *memoryReferencePriceRepository) Close() error { return nil }

func (r *memoryReferencePriceRepository) LoadReferencePriceMetadata(context.Context) (ReferencePriceMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.metadata, r.fail
}

func (r *memoryReferencePriceRepository) SaveReferencePrices(_ context.Context, metadata ReferencePriceMetadata, prices []ReferencePrice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.metadata = metadata
	if prices != nil {
		r.prices = append([]ReferencePrice{}, prices...)
		r.imports++
	} else {
		r.touches++
	}
	return nil
}

func (r *memoryReferencePriceRepository) ReferencePriceCandidates(_ context.Context, names []string) ([]ReferencePrice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	prices := []ReferencePrice{}
	for _, price := range r.prices {
		if wanted[ReferenceModelKey(price.ModelID)] {
			prices = append(prices, price)
		}
	}
	return prices, r.fail
}

func (r *memoryReferencePriceRepository) SearchReferencePrices(context.Context, string, int) ([]ReferencePrice, error) {
	return nil, errors.New("unexpected reference price search")
}

func newReferencePriceStore(t *testing.T, age time.Duration) (*Store, *memoryReferencePriceRepository) {
	t.Helper()
	s, repo := newStoreWithRepository(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	ref := s.referencePrices.Load()
	ref.metadata.FetchedAt = now.Add(-age)
	repo.referencePrices.metadata = ref.metadata
	return s, repo.referencePrices
}

func referencePricesJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/models_dev_prices.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReferenceFreshnessAndSynchronousRecovery(t *testing.T) {
	raw := referencePricesJSON(t)
	for _, tc := range []struct {
		name      string
		age       time.Duration
		model     string
		downloads int
		known     bool
	}{
		{"fresh-known", time.Hour, "gpt-4o", 0, true}, {"fresh-missing", time.Hour, "new-model", 0, false},
		{"possible-known", time.Hour + time.Nanosecond, "gpt-4o", 0, true}, {"new-model", time.Hour + time.Nanosecond, "new-model", 1, true},
		{"24h", 24 * time.Hour, "gpt-4o", 0, true}, {"expired", 24*time.Hour + time.Nanosecond, "gpt-4o", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newReferencePriceStore(t, tc.age)
			calls := 0
			s.referencePrices.Load().download = func(context.Context) ([]byte, error) {
				calls++
				body := strings.Replace(string(raw), `"gpt-4o":`, `"new-model":{"id":"new-model","cost":{"input":3,"output":4}},"gpt-4o":`, 1)
				return []byte(body), nil
			}
			p, _, err := s.ResolveModelPrice(tc.model, tc.model, true)
			if err != nil || calls != tc.downloads || (p.Source != PriceSourceNone) != tc.known {
				t.Fatalf("price=%+v error=%v downloads=%d", p, err, calls)
			}
		})
	}
}

func TestReferenceHashRetainsCacheAndOnlyTouchesMetadata(t *testing.T) {
	s, repo := newReferencePriceStore(t, 25*time.Hour)
	raw := referencePricesJSON(t)
	s.referencePrices.Load().download = func(context.Context) ([]byte, error) { return raw, nil }
	_, _, _ = s.ResolveModelPrice("gpt-4o", "", false)
	_, _, _ = s.ResolveModelPrice("missing", "", false)
	old := s.ReferencePriceMetadata()
	ref := s.referencePrices.Load()
	cached := ref.entries["gpt-4o"]
	result, err := s.RefreshReferencePrices()
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || result.Metadata.Version != old.Version || !result.Metadata.FetchedAt.After(old.FetchedAt) || repo.imports != 0 || repo.touches != 1 || ref.entries["gpt-4o"] != cached {
		t.Fatalf("result=%+v imports=%d touches=%d", result, repo.imports, repo.touches)
	}
	reads := repo.reads
	_, _, _ = s.ResolveModelPrice("missing", "", true)
	if repo.reads != reads {
		t.Fatal("negative cache lost")
	}
}

func TestManualReferenceRefreshIgnoresFreshnessAndCooldown(t *testing.T) {
	store, _ := newReferencePriceStore(t, 10*time.Minute)
	calls := 0
	fail := false
	raw := referencePricesJSON(t)
	store.referencePrices.Load().download = func(context.Context) ([]byte, error) {
		calls++
		if fail {
			return nil, errors.New("download failed")
		}
		return raw, nil
	}
	if _, err := store.RefreshReferencePrices(); err != nil || calls != 1 {
		t.Fatalf("fresh manual refresh: calls=%d err=%v", calls, err)
	}
	fail = true
	if _, err := store.RefreshReferencePrices(); err == nil || calls != 2 {
		t.Fatalf("failed manual refresh: calls=%d err=%v", calls, err)
	}
	if !store.ReferencePriceMetadata().RetryAfter.After(store.Now()) {
		t.Fatal("expected active retry cooldown")
	}
	fail = false
	if _, err := store.RefreshReferencePrices(); err != nil || calls != 3 {
		t.Fatalf("manual refresh during cooldown: calls=%d err=%v", calls, err)
	}
	if metadata := store.ReferencePriceMetadata(); metadata.LastError != "" || !metadata.RetryAfter.IsZero() {
		t.Fatalf("success did not clear failure metadata: %+v", metadata)
	}
}

func TestReferenceFailureFallbackCooldownAndStorageError(t *testing.T) {
	s, repo := newReferencePriceStore(t, 25*time.Hour)
	calls := 0
	s.referencePrices.Load().download = func(context.Context) ([]byte, error) {
		calls++
		return nil, errors.New("download failed")
	}
	old := s.ReferencePriceMetadata()
	p, _, err := s.ResolveModelPrice("gpt-4o", "", true)
	if err != nil || p.Source != PriceSourceReference {
		t.Fatal(p, err)
	}
	p, _, err = s.ResolveModelPrice("missing", "", true)
	if err != nil || p.Source != PriceSourceNone || calls != 1 || !s.ReferencePriceMetadata().FetchedAt.Equal(old.FetchedAt) {
		t.Fatal(p, err, calls)
	}
	repo.fail = errors.New("disk failed")
	_, _, err = s.ResolveModelPrice("another-missing", "", false)
	if err == nil {
		t.Fatal("database failure hidden")
	}
	if _, ok := s.referencePrices.Load().entries["another-missing"]; ok {
		t.Fatal("stored a negative result for SQL failure")
	}
	repo.fail = nil
	now := s.ReferencePriceMetadata().RetryAfter.Add(time.Second)
	s.now = func() time.Time { return now }
	_, _, _ = s.ResolveModelPrice("missing", "", true)
	if calls != 2 {
		t.Fatal("did not retry after cooldown")
	}
}

func TestReferenceRefreshInvalidatesCacheAndPreservesCustomPrices(t *testing.T) {
	s, repo := newReferencePriceStore(t, 25*time.Hour)
	_, _ = s.UpsertPrice(CustomPrice{ModelID: "gpt-4o"})
	_, _, _ = s.ResolveModelPrice("gpt-5.3-codex", "", false)
	s.referencePrices.Load().download = func(context.Context) ([]byte, error) {
		return []byte(`{"providers":{"test":{"models":{"only":{"cost":{"input":1,"output":2}}}}}}`), nil
	}
	result, err := s.RefreshReferencePrices()
	if err != nil || !result.Changed || repo.imports != 1 || len(s.referencePrices.Load().entries) != 0 {
		t.Fatal(result, err)
	}
	p, _, err := s.ResolveModelPrice("gpt-4o", "", true)
	if err != nil || p.Source != PriceSourceCustom || p.InputPer1M != 0 {
		t.Fatal(p, err)
	}
	p, _, err = s.ResolveModelPrice("gpt-5.3-codex", "", true)
	if err != nil || p.Source != PriceSourceNone {
		t.Fatal("removed reference remained usable", p, err)
	}
}

func TestCustomPriceWinsAfterReferenceStorageFailure(t *testing.T) {
	store, repository := newReferencePriceStore(t, 2*time.Hour)
	raw := referencePricesJSON(t)
	store.referencePrices.Load().download = func(context.Context) ([]byte, error) {
		_, err := store.UpsertPrice(CustomPrice{ModelID: "new-model", PriceRates: PriceRates{InputPer1M: 3}})
		if err != nil {
			t.Fatal(err)
		}
		repository.fail = errors.New("reference database unavailable")
		return raw, nil
	}
	price, model, err := store.ResolveModelPrice("new-model", "new-model(high)", true)
	if err != nil || model != "new-model" || price.Source != PriceSourceCustom || price.InputPer1M != 3 {
		t.Fatalf("reference failure overrode a new custom price: %+v, %q, %v", price, model, err)
	}
}

func TestReferenceConcurrentRefreshDoesNotBlockLocalPrices(t *testing.T) {
	s, _ := newReferencePriceStore(t, 2*time.Hour)
	if _, err := s.ClearPluginLogs(); err != nil {
		t.Fatal(err)
	}
	raw := referencePricesJSON(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.referencePrices.Load().download = func(context.Context) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return raw, nil
	}
	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() { defer wg.Done(); _, _, _ = s.ResolveModelPrice("missing", "", true) }()
	}
	<-entered
	local := make(chan struct{})
	go func() {
		defer close(local)
		p, _, err := s.ResolveModelPrice("gpt-4o", "", true)
		if err != nil || p.Source != PriceSourceReference {
			t.Error(p, err)
		}
	}()
	select {
	case <-local:
	case <-time.After(time.Second):
		close(release)
		wg.Wait()
		t.Fatal("local reference blocked by download")
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("downloads=%d", calls.Load())
	}
	if logs := mustPluginLogs(t, s); len(logs) != 2 {
		t.Fatalf("concurrent waiters duplicated refresh logs: %+v", logs)
	}
}

func TestReferenceBoundedCache(t *testing.T) {
	s, _ := newReferencePriceStore(t, 0)
	for i := 0; i < referencePriceCacheCapacity+50; i++ {
		_, _, _ = s.ResolveModelPrice(fmt.Sprintf("missing-%d", i), "", false)
	}
	if len(s.referencePrices.Load().entries) != referencePriceCacheCapacity {
		t.Fatal("unbounded cache")
	}
}

func TestReferenceSwitchRejectsInFlightOldReferencePrices(t *testing.T) {
	s, _ := newReferencePriceStore(t, 25*time.Hour)
	entered, release := make(chan struct{}), make(chan struct{})
	raw := referencePricesJSON(t)
	s.referencePrices.Load().download = func(context.Context) ([]byte, error) {
		close(entered)
		<-release
		return raw, nil
	}
	result := make(chan error, 1)
	go func() { _, _, err := s.ResolveModelPrice("gpt-4o", "", true); result <- err }()
	<-entered
	s.open = func(string) (Repository, error) { return &memoryRepository{state: NewState()}, nil }
	cfg := DefaultConfig()
	cfg.StateFile = t.TempDir() + "/other.db"
	err := s.Configure(cfg)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("old database price escaped after switching stores")
	}
}

func TestReferenceMissingReferencePricesLoadsOnRequestAndLocalReadsNeverDownload(t *testing.T) {
	s, repo := newReferencePriceStore(t, 0)
	s.referencePrices.Load().metadata = ReferencePriceMetadata{}
	repo.metadata, repo.prices = ReferencePriceMetadata{}, nil
	raw := referencePricesJSON(t)
	var calls atomic.Int32
	s.referencePrices.Load().download = func(context.Context) ([]byte, error) {
		calls.Add(1)
		return raw, nil
	}
	p, _, err := s.ResolveModelPrice("gpt-4o", "", false)
	if err != nil || p.Source != PriceSourceNone || calls.Load() != 0 {
		t.Fatal("local-only caller downloaded reference prices", p, err)
	}
	p, _, err = s.ResolveModelPrice("gpt-4o", "", true)
	if err != nil || p.Source != PriceSourceReference || calls.Load() != 1 {
		t.Fatal("first model request did not recover", p, err)
	}
	cacheSize := len(s.referencePrices.Load().entries)
	_, err = s.ModelPriceRows([]string{"gpt-4o", "missing"}, false)
	if err != nil || len(s.referencePrices.Load().entries) != cacheSize || calls.Load() != 1 {
		t.Fatal("display changed request cache or downloaded", err)
	}
}

func TestPriceRowsBatchReadsWithoutFillingRequestCache(t *testing.T) {
	store, repository := newReferencePriceStore(t, 0)
	models := make([]string, 300)
	for i := range models {
		model := fmt.Sprintf("model-%03d", i)
		models[i] = model
		repository.prices = append(repository.prices, ReferencePrice{
			ProviderID: "vendor", ModelID: model, PriceRates: &PriceRates{InputPer1M: float64(i)},
		})
	}
	if _, err := store.UpsertPrice(CustomPrice{ModelID: "retired"}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ModelPriceRows(models, true)
	if err != nil || len(rows) != 301 {
		t.Fatalf("rows=%d error=%v", len(rows), err)
	}
	for i, row := range rows[:300] {
		if row.Source != PriceSourceReference || !row.InModels || row.InputPer1M != float64(i) {
			t.Fatalf("row %d: %+v", i, row)
		}
	}
	if row := rows[300]; row.Source != PriceSourceCustom || row.InModels || row.InputPer1M != 0 {
		t.Fatalf("retired custom price lost: %+v", row)
	}
	if repository.reads != 3 {
		t.Fatalf("SQLite reads=%d, want 3 batches", repository.reads)
	}
	reads := repository.reads
	if _, _, err := store.ResolveModelPrice(models[0], "", false); err != nil {
		t.Fatal(err)
	}
	if repository.reads != reads+1 {
		t.Fatal("display populated the request cache")
	}
	if _, _, err := store.ResolveModelPrice(models[0], "", false); err != nil {
		t.Fatal(err)
	}
	if repository.reads != reads+1 {
		t.Fatal("request did not populate its cache")
	}
}

func TestReferencePricesDoNotHideUpstreamPriceConflicts(t *testing.T) {
	store, repository := newReferencePriceStore(t, 0)
	repository.prices = []ReferencePrice{
		{ProviderID: "first", ModelID: "upstream", PriceRates: &PriceRates{InputPer1M: 1}},
		{ProviderID: "second", ModelID: "upstream", PriceRates: &PriceRates{InputPer1M: 2}},
		{ProviderID: "maker", ModelID: "requested", IsCanonical: true, PriceRates: &PriceRates{InputPer1M: 3}},
	}
	for i := 0; i < 2; i++ {
		match, err := store.referencePrices.Load().lookup(context.Background(), "route/upstream(xhigh)", "requested")
		if err != nil || match.found {
			t.Fatalf("upstream conflict fell back to requested price: %+v err=%v", match, err)
		}
	}
	if repository.reads != 1 {
		t.Fatalf("negative match was not cached: reads=%d", repository.reads)
	}
	match, err := store.referencePrices.Load().lookup(context.Background(), "absent", "route/requested(xhigh)")
	if err != nil || !match.found || match.price.ModelID != "requested" {
		t.Fatalf("absent upstream did not fall back to requested model: %+v err=%v", match, err)
	}
}

func TestMatchReferencePriceNormalizesOnlyPrefixSuffixAndCase(t *testing.T) {
	prices := []ReferencePrice{
		{ProviderID: "openai", ModelID: "gpt-5.6-sol", IsCanonical: true, PriceRates: &PriceRates{InputPer1M: 2}},
		{ProviderID: "reseller", ModelID: "gpt-5.6-sol", PriceRates: &PriceRates{InputPer1M: 9}},
	}
	for _, model := range []string{"gpt-5.6-sol", " CODEX/GPT-5.6-SOL(xhigh) ", "route/openai/gpt-5.6-sol(32768)"} {
		price, found := MatchReferencePrice(model, prices)
		if !found || price.ProviderID != "openai" || price.InputPer1M != 2 {
			t.Fatalf("%q: price=%+v found=%t", model, price, found)
		}
	}
	for _, model := range []string{"", "codex/", "gpt-56-sol", "gpt5.6sol", "gpt-5.6-sol-vision-exp", "gpt-5.6-*", "codex:gpt-5.6-sol"} {
		if price, found := MatchReferencePrice(model, prices); found {
			t.Fatalf("unrelated model %q matched %+v", model, price)
		}
	}
}

func TestMatchReferencePriceRequiresUnambiguousSupportedRates(t *testing.T) {
	paid := &PriceRates{InputPer1M: 1, OutputPer1M: 2}
	other := &PriceRates{InputPer1M: 3, OutputPer1M: 4}
	for _, test := range []struct {
		name   string
		prices []ReferencePrice
		found  bool
	}{
		{name: "empty"},
		{name: "missing rates", prices: []ReferencePrice{{ModelID: "chat"}}},
		{name: "single candidate", prices: []ReferencePrice{{ModelID: "chat", PriceRates: &PriceRates{}}}, found: true},
		{name: "same rates", prices: []ReferencePrice{
			{ProviderID: "a", ModelID: "family/chat", PriceRates: paid},
			{ProviderID: "b", ModelID: "chat", PriceRates: paid},
		}, found: true},
		{name: "conflicting rates", prices: []ReferencePrice{
			{ProviderID: "a", ModelID: "chat", PriceRates: paid},
			{ProviderID: "b", ModelID: "chat", PriceRates: other},
		}},
		{name: "canonical wins", prices: []ReferencePrice{
			{ProviderID: "reseller", ModelID: "chat", PriceRates: other},
			{ProviderID: "maker", ModelID: "chat", IsCanonical: true, PriceRates: paid},
		}, found: true},
		{name: "unsupported canonical blocks fallback", prices: []ReferencePrice{
			{ProviderID: "reseller", ModelID: "chat", PriceRates: paid},
			{ProviderID: "maker", ModelID: "chat", IsCanonical: true},
		}},
		{name: "conflicting canonical rates", prices: []ReferencePrice{
			{ProviderID: "a", ModelID: "chat", IsCanonical: true, PriceRates: paid},
			{ProviderID: "b", ModelID: "chat", IsCanonical: true, PriceRates: other},
		}},
		{name: "unrelated canonical ignored", prices: []ReferencePrice{
			{ProviderID: "a", ModelID: "other", IsCanonical: true},
			{ProviderID: "b", ModelID: "chat", PriceRates: paid},
		}, found: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, found := MatchReferencePrice("route/chat(xhigh)", test.prices)
			if found != test.found {
				t.Fatalf("found=%t, want %t", found, test.found)
			}
		})
	}
}

func TestReferencePriceSyncLogsOnlyActualDownloads(t *testing.T) {
	store, _ := newReferencePriceStore(t, 25*time.Hour)
	if _, err := store.ClearPluginLogs(); err != nil {
		t.Fatal(err)
	}
	raw := referencePricesJSON(t)
	store.referencePrices.Load().download = func(context.Context) ([]byte, error) { return raw, nil }
	if _, _, err := store.ResolveModelPrice("gpt-4o", "gpt-4o", true); err != nil {
		t.Fatal(err)
	}
	logs := mustPluginLogs(t, store)
	if len(logs) != 2 || logs[0].Level != PluginLogInfo || !strings.Contains(logs[0].Message, "are unchanged") ||
		logs[1].Level != PluginLogInfo || !strings.Contains(logs[1].Message, "Syncing reference prices from models.dev") {
		t.Fatalf("sync logs = %+v", logs)
	}
	if _, _, err := store.ResolveModelPrice("gpt-4o", "gpt-4o", true); err != nil {
		t.Fatal(err)
	}
	if logs := mustPluginLogs(t, store); len(logs) != 2 {
		t.Fatalf("fresh lookup produced sync logs: %+v", logs)
	}
}

func TestReferenceRequestSuffixIsRemovedOnce(t *testing.T) {
	prices := []ReferencePrice{
		{ProviderID: "maker", ModelID: "family/model(custom)", PriceRates: &PriceRates{InputPer1M: 1}},
		{ProviderID: "maker", ModelID: "model", PriceRates: &PriceRates{InputPer1M: 9}},
	}
	const requested = "provider/model(custom)(high)"
	matched, found := MatchReferencePrice(requested, prices)
	if !found || matched.InputPer1M != 1 {
		t.Fatalf("request suffix stripped more than once: %+v", matched)
	}
	matched, found = matchReferencePriceKeys(prices, referencePriceKeys(requested, requested))
	if !found || matched.InputPer1M != 1 {
		t.Fatalf("lookup keys differ from direct matching: %+v", matched)
	}
	store, repository := newReferencePriceStore(t, 0)
	repository.prices = prices
	rows, err := store.ModelPriceRows([]string{requested}, false)
	if err != nil || len(rows) != 1 || rows[0].Source != PriceSourceReference || rows[0].InputPer1M != 1 {
		t.Fatalf("display matching differs from request matching: %+v, %v", rows, err)
	}
	if keys := referencePriceKeys("model", "auto(high)"); len(keys) != 1 || keys[0] != "model" {
		t.Fatalf("auto request became a reference model: %v", keys)
	}
}

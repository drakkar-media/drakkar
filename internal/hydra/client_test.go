package hydra

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
)

func TestSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("t") != "search" {
			t.Fatalf("unexpected query %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Dune.2021.1080p.WEB-DL.x265-GRP","link":"http://example/nzb","indexer":"hydra","size":12345,"epoch":1710000000}]}`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc"})
	got, err := client.Search(context.Background(), SearchRequest{Query: "Dune 2021"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].Indexer != "hydra" || got[0].SizeBytes != 12345 {
		t.Fatalf("unexpected result %+v", got[0])
	}
}

func TestSearchXML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <item>
      <title>Dune.2021.1080p.WEB-DL.x265-GRP</title>
      <link>http://example/nzb</link>
      <pubDate>Fri, 05 Jun 2026 12:00:00 +0000</pubDate>
      <newznab:attr name="indexer" value="hydra" />
      <newznab:attr name="size" value="12345" />
    </item>
  </channel>
</rss>`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc"})
	got, err := client.Search(context.Background(), SearchRequest{Query: "Dune 2021"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].Indexer != "hydra" || got[0].SizeBytes != 12345 || got[0].Link != "http://example/nzb" {
		t.Fatalf("unexpected result %+v", got[0])
	}
}

func TestProbeUsesCaps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("t"); got != "caps" {
			t.Fatalf("unexpected probe type %q", got)
		}
		if got := r.URL.Query().Get("q"); got != "" {
			t.Fatalf("probe should not search, got q=%q", got)
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><caps></caps>`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL + "/api", APIKey: "abc"})
	if err := client.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSearchUsesStructuredMovieParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("t"); got != "movie" {
			t.Fatalf("expected movie search, got %q", got)
		}
		if got := r.URL.Query().Get("cat"); got != "2000,2010,2020,2030,2040,2045,2050,2060" {
			t.Fatalf("expected movie categories, got %q", got)
		}
		if got := r.URL.Query().Get("imdbid"); got != "1160419" {
			t.Fatalf("expected imdbid 1160419, got %q", got)
		}
		if got := r.URL.Query().Get("q"); got != "Dune 2021" {
			t.Fatalf("expected q, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc"})
	if _, err := client.Search(context.Background(), SearchRequest{MediaType: "movie", Query: "Dune 2021", IMDbID: "tt1160419"}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchOmitsFreeTextWhenOnlyIMDbIDIsUsed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("imdbid"); got != "1160419" {
			t.Fatalf("expected imdbid 1160419, got %q", got)
		}
		if got := r.URL.Query().Get("q"); got != "" {
			t.Fatalf("expected empty q, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc"})
	if _, err := client.Search(context.Background(), SearchRequest{MediaType: "movie", IMDbID: "tt1160419"}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchUsesStructuredTVParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("t"); got != "tvsearch" {
			t.Fatalf("expected tvsearch, got %q", got)
		}
		if got := r.URL.Query().Get("cat"); got != "5000" {
			t.Fatalf("expected tv categories, got %q", got)
		}
		if got := r.URL.Query().Get("imdbid"); got != "9140554" {
			t.Fatalf("expected imdbid 9140554, got %q", got)
		}
		if got := r.URL.Query().Get("tvdbid"); got != "362472" {
			t.Fatalf("expected tvdbid 362472, got %q", got)
		}
		if got := r.URL.Query().Get("season"); got != "2" {
			t.Fatalf("expected season 2, got %q", got)
		}
		if got := r.URL.Query().Get("ep"); got != "3" {
			t.Fatalf("expected ep 3, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc"})
	if _, err := client.Search(context.Background(), SearchRequest{MediaType: "episode", Query: "Loki S02E03", IMDbID: "tt9140554", TVDBID: 362472, SeasonNumber: 2, EpisodeNumber: 3}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchRecentUsesCategoryOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("t"); got != "search" {
			t.Fatalf("expected search, got %q", got)
		}
		if got := r.URL.Query().Get("cat"); got != "5000" {
			t.Fatalf("expected tv category, got %q", got)
		}
		if got := r.URL.Query().Get("q"); got != "" {
			t.Fatalf("expected empty q, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc"})
	if _, err := client.SearchRecent(context.Background(), "tv"); err != nil {
		t.Fatal(err)
	}
}

func TestSearchStartsCooldownOn429(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc"})
	if _, err := client.Search(context.Background(), SearchRequest{Query: "Dune 2021"}); err == nil {
		t.Fatal("expected rate limit error")
	}
	if _, err := client.SearchRecent(context.Background(), "movie"); err == nil {
		t.Fatal("expected cooldown error")
	}
}

func TestSearchUsesCacheWithinTTL(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Dune.2021.1080p.WEB-DL.x265-GRP","link":"http://example/nzb","indexer":"hydra","size":12345,"epoch":1710000000}]}`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc", SearchCacheTTLSeconds: 3600})
	if _, err := client.Search(context.Background(), SearchRequest{MediaType: "movie", Query: "Dune 2021"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(context.Background(), SearchRequest{MediaType: "movie", Query: "Dune 2021"}); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
}

func TestSearchRecentUsesFeedCacheWithinTTL(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc", FeedCacheTTLSeconds: 3600})
	if _, err := client.SearchRecent(context.Background(), "tv"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SearchRecent(context.Background(), "tv"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
}

func TestSearchDefaultDoesNotCache(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc"})
	if _, err := client.Search(context.Background(), SearchRequest{MediaType: "movie", Query: "Dune 2021"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(context.Background(), SearchRequest{MediaType: "movie", Query: "Dune 2021"}); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("expected 2 upstream hits with default no-cache, got %d", got)
	}
}

func TestSearchRecentDefaultDoesNotCache(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc"})
	if _, err := client.SearchRecent(context.Background(), "tv"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SearchRecent(context.Background(), "tv"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("expected 2 upstream hits with default no-cache, got %d", got)
	}
}

func TestSetConfigAppliesCachePolicyAndClearsEntries(t *testing.T) {
	client := NewClient(config.ServiceConfig{
		URL:                   "http://old.example/api",
		APIKey:                "old-key",
		SearchCacheTTLSeconds: 60,
		FeedCacheTTLSeconds:   120,
		FeedMaxResults:        25,
	})
	before := client.getCacheConfig()
	searchKey := versionedCacheKey(before.generation, searchCacheKey(SearchRequest{Query: "Dune"}))
	client.storeCache(client.searchCache, searchKey, []SearchResult{{Title: "old result"}}, before.searchTTL, searchCacheMaxEntries)
	if _, ok := client.lookupCache(client.searchCache, searchKey); !ok {
		t.Fatal("expected primed cache entry")
	}

	client.SetConfig(config.ServiceConfig{
		URL:                   "http://new.example/api",
		APIKey:                "new-key",
		SearchCacheTTLSeconds: 5,
		FeedCacheTTLSeconds:   10,
		FeedMaxResults:        50,
	})
	after := client.getCacheConfig()
	if after.generation == before.generation {
		t.Fatal("cache generation did not advance")
	}
	if after.searchTTL != 5*time.Second || after.feedTTL != 10*time.Second || after.feedMaxResults != 50 {
		t.Fatalf("cache policy was not applied: %+v", after)
	}
	if client.getBaseURL() != "http://new.example/api" || client.getAPIKey() != "new-key" {
		t.Fatal("upstream configuration was not applied")
	}
	if _, ok := client.lookupCache(client.searchCache, searchKey); ok {
		t.Fatal("upstream change retained an old cache entry")
	}
}

func TestSetConfigUpdatesFeedResultLimit(t *testing.T) {
	limits := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limits <- r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, FeedMaxResults: 10})
	client.SetConfig(config.ServiceConfig{URL: server.URL, FeedMaxResults: 77})
	if _, err := client.SearchRecent(context.Background(), "tv"); err != nil {
		t.Fatal(err)
	}
	if got := <-limits; got != "77" {
		t.Fatalf("feed limit = %q, want 77", got)
	}
}

func TestSearchCacheEvictsLeastRecentlyUsedAndPrunesExpired(t *testing.T) {
	client := NewClient(config.ServiceConfig{})
	now := time.Now()
	client.cacheMu.Lock()
	client.searchCache["expired"] = cachedResults{expiresAt: now.Add(-time.Second), lastUsed: now.Add(-time.Hour)}
	for i := 0; i < searchCacheMaxEntries; i++ {
		key := fmt.Sprintf("key-%d", i)
		client.searchCache[key] = cachedResults{
			results:   []SearchResult{{Title: key}},
			expiresAt: now.Add(time.Hour),
			lastUsed:  now.Add(time.Duration(i) * time.Second),
		}
	}
	client.cacheMu.Unlock()

	client.storeCache(client.searchCache, "new", []SearchResult{{Title: "new"}}, time.Hour, searchCacheMaxEntries)
	client.cacheMu.Lock()
	defer client.cacheMu.Unlock()
	if len(client.searchCache) != searchCacheMaxEntries {
		t.Fatalf("cache has %d entries, want cap %d", len(client.searchCache), searchCacheMaxEntries)
	}
	if _, ok := client.searchCache["expired"]; ok {
		t.Fatal("expired entry was not pruned")
	}
	if _, ok := client.searchCache["key-0"]; ok {
		t.Fatal("least recently used entry was not evicted")
	}
	if _, ok := client.searchCache["new"]; !ok {
		t.Fatal("new cache entry was not stored")
	}
}

// TestSearchPaginates verifies that Search fetches subsequent pages when the
// first page is full (100 results) and stops when a partial page is received.
func TestSearchPaginates(t *testing.T) {
	// Build a full page of exactly searchPageSize results.
	items := make([]string, searchPageSize)
	for i := range items {
		items[i] = fmt.Sprintf(`{"title":"Release%d","link":"http://example/%d","indexer":"hydra","size":100,"epoch":1710000000}`, i, i)
	}
	fullPage := `{"results":[` + strings.Join(items, ",") + `]}`
	partialPage := `{"results":[{"title":"Last","link":"http://example/last","indexer":"hydra","size":100,"epoch":1710000000}]}`

	var offsets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		offsets = append(offsets, offset)
		w.Header().Set("Content-Type", "application/json")
		if offset == "0" {
			_, _ = w.Write([]byte(fullPage))
		} else {
			_, _ = w.Write([]byte(partialPage))
		}
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc"})
	results, err := client.Search(context.Background(), SearchRequest{Query: "Show"})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 2 {
		t.Fatalf("expected 2 page requests (offsets 0 and 100), got %d: %v", len(offsets), offsets)
	}
	if offsets[0] != "0" || offsets[1] != "100" {
		t.Fatalf("unexpected offsets: %v", offsets)
	}
	// 100 results from page 0 + 1 result from page 1
	if len(results) != searchPageSize+1 {
		t.Fatalf("expected %d results, got %d", searchPageSize+1, len(results))
	}
}

// TestConcurrentSearchesShareRateLimit verifies that raising maxConcurrentSearches
// does not increase the request rate hitting NZBHydra2: first-page call timestamps
// must still be spaced by at least searchInterval apart, because throttle() paces
// off a single shared clock (lastCall) rather than one clock per concurrency slot.
func TestConcurrentSearchesShareRateLimit(t *testing.T) {
	const delay = 100 * time.Millisecond

	var mu sync.Mutex
	var callTimes []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callTimes = append(callTimes, time.Now())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc"})
	client.SetSearchDelay(delay)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := client.Search(context.Background(), SearchRequest{Query: fmt.Sprintf("Show%d", n)})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(callTimes) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(callTimes))
	}
	sort.Slice(callTimes, func(i, j int) bool { return callTimes[i].Before(callTimes[j]) })
	for i := 1; i < len(callTimes); i++ {
		gap := callTimes[i].Sub(callTimes[i-1])
		if gap < delay-10*time.Millisecond {
			t.Fatalf("requests %d and %d fired only %s apart, want >= ~%s (concurrency must not bypass shared rate limit)", i-1, i, gap, delay)
		}
	}
}

// TestConcurrentIdenticalSearchesCoalesceIntoOneUpstreamCall guards against a
// check-then-act race in lookupSearchCache/storeSearchCache: two concurrent
// calls for the exact same request (e.g. two different episodes of the same
// show issuing the same season-pack query) could both miss the cache before
// either had stored a result, and both hit the live NZBHydra2 API for the
// identical request. searchSem only bounds total concurrency -- it does not
// deduplicate identical requests, so it does not close this gap on its own.
// Search now funnels concurrent identical requests through searchFlight (a
// SingleFlight-shaped coalescer) so only one of them actually calls
// NZBHydra2 and the rest share its result.
//
// The handler blocks on release until every goroutine has been given a
// chance to reach the cache-miss + coalescing point, so a fast single
// request finishing before the others even start can't produce a false
// pass -- if the race were still present, every one of the concurrent
// callers would reach the handler and block on release, and hits would come
// back > 1.
func TestConcurrentIdenticalSearchesCoalesceIntoOneUpstreamCall(t *testing.T) {
	var hits int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Dune.2021.1080p.WEB-DL.x265-GRP","link":"http://example/nzb","indexer":"hydra","size":12345,"epoch":1710000000}]}`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc"})

	const callers = 5
	var wg sync.WaitGroup
	results := make([][]SearchResult, callers)
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = client.Search(context.Background(), SearchRequest{MediaType: "movie", Query: "Dune 2021"})
		}(i)
	}

	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d returned error: %v", i, err)
		}
		if len(results[i]) != 1 || results[i][0].Indexer != "hydra" {
			t.Fatalf("call %d: unexpected result %+v", i, results[i])
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected exactly 1 upstream hit across %d concurrent identical searches, got %d", callers, got)
	}
}

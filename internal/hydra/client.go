// Package hydra implements the search-provider client for NZBHydra2, the
// meta-indexer that aggregates results across the user's configured Usenet
// indexers. It speaks the Newznab-compatible API (JSON or RSS/XML), adding
// Radarr/Sonarr-equivalent pagination, caching, request coalescing, and
// rate-limit backoff on top.
package hydra

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/httperr"
)

// defaultSearchInterval is the minimum gap between consecutive Hydra API calls.
// 0 matches Sonarr/Radarr behaviour: do not add client-side throttling unless
// explicitly configured by the user.
var defaultSearchInterval time.Duration

// ErrRateLimited is returned (optionally wrapped) when NZBHydra2 responds
// with 429 or when a call is made while an active cooldown (see
// startCooldown) is still in effect.
var ErrRateLimited = errors.New("nzbhydra2 rate limited")

const (
	searchPageSize        = 100 // results per page — matches Radarr/Sonarr PageSize
	searchMaxPages        = 30  // max pages per request → 3,000 results cap (matches Radarr/Sonarr)
	defaultFeedMaxResults = 1200
	searchCacheMaxEntries = 256
	feedCacheMaxEntries   = 16
)

var (
	// Matches Radarr's default categories (2000–2060 full set).
	movieCategories = []string{"2000", "2010", "2020", "2030", "2040", "2045", "2050", "2060"}
	// Use root TV category so Hydra/indexers can return any TV subcategory.
	tvCategories     = []string{"5000"}
	rateLimitBackoff = []time.Duration{
		15 * time.Minute,
		30 * time.Minute,
		60 * time.Minute,
		3 * time.Hour,
		6 * time.Hour,
		12 * time.Hour,
		24 * time.Hour,
	}
)

// Client is a search-provider client for a single NZBHydra2 instance.
//
// It owns request throttling/concurrency limiting, response caching with
// single-flight coalescing of identical concurrent requests, and rate-limit
// backoff, so callers can issue searches without individually managing any
// of that. The target URL/API key and outbound HTTP transport can be
// updated live via SetConfig/SetTransport (e.g. after a settings save)
// without disrupting in-flight requests.
//
// Client is safe for concurrent use.
type Client struct {
	cfgMu           sync.RWMutex
	baseURL         string
	apiKey          string
	cacheGeneration uint64
	searchCacheTTL  time.Duration
	feedCacheTTL    time.Duration
	feedMaxResults  int
	httpClient      atomic.Pointer[http.Client]

	searchInterval time.Duration // 0 = no throttle (Sonarr/Radarr behaviour)

	// searchSem limits concurrent in-flight NZBHydra2 HTTP requests.
	// NZBHydra2 applies per-indexer load limiting; hammering it with many
	// concurrent searches exhausts daily API quotas. 3 concurrent matches
	// typical Radarr+Sonarr combined load on a shared NZBHydra2 instance.
	searchSem chan struct{}

	rateMu        sync.Mutex
	lastCall      time.Time
	cooldownUntil time.Time
	rateLimitHits int

	cacheMu     sync.Mutex
	searchCache map[string]cachedResults
	feedCache   map[string]cachedResults

	// searchFlight/feedFlight coalesce concurrent Search/SearchRecent calls
	// that share a cache key (e.g. two different episodes of the same show
	// issuing the same season-pack query). Without this, lookupSearchCache/
	// storeSearchCache (and the feed-cache equivalent) were a plain
	// check-then-act: two goroutines could both miss the cache before either
	// had stored a result, and both would hit the live NZBHydra2 API for the
	// identical request. searchSem bounds total concurrent requests but does
	// not deduplicate identical ones, so it doesn't close this gap on its
	// own. See internal/cache.SingleFlight (used the same way by
	// CachedDecodedSource in internal/nntp) for the reference pattern this
	// mirrors — reimplemented locally here because SingleFlight is typed to
	// []byte and search results aren't a byte blob.
	searchFlight *hydraFlight
	feedFlight   *hydraFlight
}

// cachedResults is a cache entry shared by the search and feed caches, along
// with the wall-clock time it expires.
type cachedResults struct {
	results   []SearchResult
	expiresAt time.Time
	lastUsed  time.Time
}

type cacheConfig struct {
	generation     uint64
	searchTTL      time.Duration
	feedTTL        time.Duration
	feedMaxResults int
}

// hydraFlight deduplicates concurrent calls that share a key, so only one
// goroutine actually runs the fetch while the rest wait for and share its
// result. Mirrors internal/cache.SingleFlight's lock+channel design.
type hydraFlight struct {
	mu      sync.Mutex
	flights map[string]*hydraFlightCall
}

// hydraFlightCall tracks a single in-flight fetch: followers block on done
// and then read results/err once the leader has populated them.
type hydraFlightCall struct {
	done    chan struct{}
	results []SearchResult
	err     error
}

func newHydraFlight() *hydraFlight {
	return &hydraFlight{flights: make(map[string]*hydraFlightCall)}
}

// Do runs fn for the first caller with a given key; concurrent callers with
// the same key wait for that result instead of running fn themselves. Each
// caller (leader and followers alike) gets back its own copy of the results
// slice, so nobody can mutate a shared backing array out from under another
// caller.
//
// fn runs with a detached (non-cancellable) context, same rationale as
// SingleFlight.Do: a caller cancelling its own wait (see the ctx.Done() case
// below) must not abort the in-flight fetch out from under any other caller
// still waiting on it.
func (f *hydraFlight) Do(ctx context.Context, key string, fn func(context.Context) ([]SearchResult, error)) ([]SearchResult, error) {
	f.mu.Lock()
	if active, ok := f.flights[key]; ok {
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-active.done:
			return cloneResults(active.results), active.err
		}
	}
	active := &hydraFlightCall{done: make(chan struct{})}
	f.flights[key] = active
	f.mu.Unlock()

	active.results, active.err = fn(context.WithoutCancel(ctx))
	close(active.done)

	f.mu.Lock()
	delete(f.flights, key)
	f.mu.Unlock()

	return cloneResults(active.results), active.err
}

// SearchResult is a single release returned by NZBHydra2, normalized from
// either its JSON or RSS/XML response format.
type SearchResult struct {
	Title        string
	Link         string
	Indexer      string
	SizeBytes    int64
	PublishedAt  time.Time
	Grabs        int
	IndexerScore int
	Passworded   bool
}

// SearchRequest describes a single Search call's query parameters. IMDbID/
// TMDBID/TVDBID and SeasonNumber/EpisodeNumber are optional identifier-based
// refinements layered on top of (or instead of) a free-text Query, mirroring
// how Radarr/Sonarr build their own indexer queries.
type SearchRequest struct {
	MediaType     string
	Query         string
	IMDbID        string
	TMDBID        int64 // tmdbid= parameter (Radarr/Sonarr first-tier ID search)
	TVDBID        int64
	SeasonNumber  int
	EpisodeNumber int
}

// NewClient creates a Client for the NZBHydra2 instance described by cfg.
//
// It applies a default feed result limit when cfg.FeedMaxResults is unset
// and initializes the outbound HTTP transport with a 30s timeout; use
// SetTransport afterward to route requests through a custom transport (e.g.
// a privacy proxy).
func NewClient(cfg config.ServiceConfig) *Client {
	// 3 concurrent searches: each *arr app (Radarr, Sonarr, ...) independently
	// enforces its own 2-second per-indexer rate limit (HttpIndexerBase.RateLimit),
	// not a global one — running Radarr+Sonarr together against the same
	// NZBHydra2 instance already produces 2+ concurrent requests in practice.
	// NZBHydra2 itself also applies its own per-indexer pacing toward the real
	// Usenet indexers, so this client-side cap only needs to bound Drakkar's own
	// worker pool, not re-implement indexer rate limiting from scratch.
	const maxConcurrentSearches = 3
	c := &Client{
		searchInterval: defaultSearchInterval,
		searchSem:      make(chan struct{}, maxConcurrentSearches),
		searchCache:    make(map[string]cachedResults),
		feedCache:      make(map[string]cachedResults),
		searchFlight:   newHydraFlight(),
		feedFlight:     newHydraFlight(),
	}
	c.SetConfig(cfg)
	c.httpClient.Store(&http.Client{Timeout: 30 * time.Second})
	return c
}

// SetTransport swaps the underlying HTTP transport (e.g. to route through
// privacy.Manager) live, without disturbing any other client state.
func (c *Client) SetTransport(transport http.RoundTripper) {
	c.httpClient.Store(&http.Client{Timeout: 30 * time.Second, Transport: transport})
}

// SetConfig updates the upstream and cache policy live. Any policy change
// invalidates existing entries and advances the cache generation so an
// in-flight request using the old configuration cannot repopulate the cache.
func (c *Client) SetConfig(cfg config.ServiceConfig) {
	searchTTL := cacheTTL(cfg.SearchCacheTTLSeconds)
	feedTTL := cacheTTL(cfg.FeedCacheTTLSeconds)
	feedMaxResults := cfg.FeedMaxResults
	if feedMaxResults <= 0 {
		feedMaxResults = defaultFeedMaxResults
	}
	baseURL := strings.TrimRight(cfg.URL, "/")

	c.cfgMu.Lock()
	changed := c.baseURL != baseURL ||
		c.apiKey != cfg.APIKey ||
		c.searchCacheTTL != searchTTL ||
		c.feedCacheTTL != feedTTL ||
		c.feedMaxResults != feedMaxResults
	c.baseURL = baseURL
	c.apiKey = cfg.APIKey
	c.searchCacheTTL = searchTTL
	c.feedCacheTTL = feedTTL
	c.feedMaxResults = feedMaxResults
	if changed {
		c.cacheGeneration++
		c.cacheMu.Lock()
		clear(c.searchCache)
		clear(c.feedCache)
		c.cacheMu.Unlock()
	}
	c.cfgMu.Unlock()
}

func cacheTTL(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	if int64(seconds) > math.MaxInt64/int64(time.Second) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(seconds) * time.Second
}

func (c *Client) getCacheConfig() cacheConfig {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return cacheConfig{
		generation:     c.cacheGeneration,
		searchTTL:      c.searchCacheTTL,
		feedTTL:        c.feedCacheTTL,
		feedMaxResults: c.feedMaxResults,
	}
}

func (c *Client) getBaseURL() string {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.baseURL
}

func (c *Client) getAPIKey() string {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.apiKey
}

// SetSearchDelay configures the minimum delay between consecutive Hydra API
// calls. 0 means no delay (matches Sonarr/Radarr behaviour).
func (c *Client) SetSearchDelay(d time.Duration) {
	c.rateMu.Lock()
	c.searchInterval = d
	c.rateMu.Unlock()
}

// Name returns the provider identifier used in logs and error messages.
func (c *Client) Name() string {
	return "nzbhydra2"
}

// Probe verifies connectivity to NZBHydra2 by requesting its capabilities
// endpoint. It does not consume the search rate limit or cache.
func (c *Client) Probe(ctx context.Context) error {
	u, err := c.apiURL()
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("t", "caps")
	if apiKey := c.getAPIKey(); apiKey != "" {
		q.Set("apikey", apiKey)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Load().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("nzbhydra2 caps status %d", resp.StatusCode)
	}
	return nil
}

// origin resolves the configured base URL down to just its scheme+host[:port]
// -- NZBHydra2's undocumented /internalapi endpoints live at the server
// root, but the configured URL is the Newznab search base (conventionally
// ".../api"), so the configured path segment isn't part of these calls.
func (c *Client) origin() (string, error) {
	base := strings.TrimSpace(c.getBaseURL())
	if base == "" {
		return "", errors.New("nzbhydra2 not configured")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("nzbhydra2 url: %w", err)
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String(), nil
}

// ProxyConfig describes the SOCKS5 proxy Drakkar itself is configured to
// use, as forwarded into NZBHydra2 by SyncProxy.
type ProxyConfig struct {
	Host     string
	Port     int
	Username string
	Password string
}

// SyncProxy pushes Drakkar's own privacy-routing choice into NZBHydra2's
// native proxy settings via its internal config API, so NZBHydra2's own
// outbound indexer traffic -- which Drakkar cannot route itself, since
// NZBHydra2 is a separate process with its own networking entirely outside
// Drakkar's control -- goes through the same SOCKS5 proxy. Passing
// enabled=false clears NZBHydra2's proxy back to "no proxy"; NZBHydra2 has
// no WireGuard proxy type, so callers should pass enabled=false for both
// Direct and WireGuard mode, and only enabled=true for SOCKS5 mode.
//
// NZBHydra2's config API has no per-field PATCH, so this fetches the entire
// current config, patches only the "main" section's proxy* fields, and
// writes the whole thing back. Every other field -- including other
// secrets, which NZBHydra2 masks as the literal string "***UNCHANGED***" on
// GET -- round-trips untouched; confirmed live against a real instance that
// NZBHydra2 treats that mask specially server-side and does not overwrite
// the real stored value when it comes back unchanged, so this is safe even
// though the intermediate representation briefly holds the mask text for
// fields this call never intends to change.
func (c *Client) SyncProxy(ctx context.Context, enabled bool, proxy ProxyConfig) error {
	origin, err := c.origin()
	if err != nil {
		return err
	}

	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/internalapi/config", nil)
	if err != nil {
		return err
	}
	getResp, err := c.httpClient.Load().Do(getReq)
	if err != nil {
		return fmt.Errorf("nzbhydra2 get config: %w", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode < 200 || getResp.StatusCode >= 300 {
		return fmt.Errorf("nzbhydra2 get config status %d", getResp.StatusCode)
	}
	body, err := io.ReadAll(getResp.Body)
	if err != nil {
		return fmt.Errorf("nzbhydra2 get config: %w", err)
	}

	var full map[string]json.RawMessage
	if err := json.Unmarshal(body, &full); err != nil {
		return fmt.Errorf("nzbhydra2 config: %w", err)
	}
	var main map[string]json.RawMessage
	if err := json.Unmarshal(full["main"], &main); err != nil {
		return fmt.Errorf("nzbhydra2 config: main section: %w", err)
	}

	set := func(key string, v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		main[key] = raw
		return nil
	}

	if enabled {
		if err := set("proxyType", "SOCKS"); err != nil {
			return err
		}
		if err := set("proxyHost", proxy.Host); err != nil {
			return err
		}
		if err := set("proxyPort", proxy.Port); err != nil {
			return err
		}
		if err := set("proxyUsername", proxy.Username); err != nil {
			return err
		}
		if err := set("proxyPassword", proxy.Password); err != nil {
			return err
		}
		if err := set("proxyIgnoreLocal", true); err != nil {
			return err
		}
	} else {
		if err := set("proxyType", "NONE"); err != nil {
			return err
		}
		if err := set("proxyHost", nil); err != nil {
			return err
		}
		if err := set("proxyUsername", nil); err != nil {
			return err
		}
		if err := set("proxyPassword", nil); err != nil {
			return err
		}
	}

	mainRaw, err := json.Marshal(main)
	if err != nil {
		return err
	}
	full["main"] = mainRaw
	newBody, err := json.Marshal(full)
	if err != nil {
		return err
	}

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, origin+"/internalapi/config", bytes.NewReader(newBody))
	if err != nil {
		return err
	}
	putReq.Header.Set("Content-Type", "application/json")
	putResp, err := c.httpClient.Load().Do(putReq)
	if err != nil {
		return fmt.Errorf("nzbhydra2 put config: %w", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(putResp.Body)
		return fmt.Errorf("nzbhydra2 put config status %d: %s", putResp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var result struct {
		OK            bool     `json:"ok"`
		ErrorMessages []string `json:"errorMessages"`
	}
	if err := json.NewDecoder(putResp.Body).Decode(&result); err == nil && !result.OK {
		return fmt.Errorf("nzbhydra2 rejected proxy config: %v", result.ErrorMessages)
	}
	return nil
}

// hydraIndexerConfig is the subset of NZBHydra2's internal config response
// (GET /internalapi/config) this client cares about. NZBHydra2 doesn't
// expose its configured sub-indexer list through the public Newznab API
// (Sonarr/Radarr/Drakkar all see it as a single aggregated indexer), so
// listing them for the UI requires its own admin-facing internal API --
// undocumented but stable enough in practice for this read-only use, and
// this deployment runs it with authType "NONE" (no session required).
type hydraIndexerConfig struct {
	Indexers []struct {
		Name  string `json:"name"`
		State string `json:"state"` // ENABLED | DISABLED_USER | DISABLED_SYSTEM | DISABLED_SYSTEM_TEMPORARY
	} `json:"indexers"`
}

// Indexers returns the names of every indexer currently enabled in
// NZBHydra2's own configuration, sorted alphabetically -- used to populate
// a selectable list (e.g. Privacy Routing's per-indexer exclusion list)
// instead of requiring the operator to type names by hand. Returns an
// error if NZBHydra2 isn't reachable or the response doesn't parse;
// callers should treat that as "list unavailable" and fall back to manual
// entry rather than failing outright.
func (c *Client) Indexers(ctx context.Context) ([]string, error) {
	origin, err := c.origin()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/internalapi/config", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Load().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("nzbhydra2 internal config status %d", resp.StatusCode)
	}
	var cfg hydraIndexerConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("nzbhydra2 internal config: %w", err)
	}
	names := make([]string, 0, len(cfg.Indexers))
	for _, ix := range cfg.Indexers {
		if ix.State == "ENABLED" && strings.TrimSpace(ix.Name) != "" {
			names = append(names, ix.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// SearchRecent fetches the most recent releases for mediaType ("movie" or
// "episode"/"tv"), used to populate the RSS-style recent-releases feed.
// Results are served from the feed cache when fresh, and concurrent calls
// for the same mediaType are coalesced via feedFlight so only one actually
// reaches NZBHydra2.
func (c *Client) SearchRecent(ctx context.Context, mediaType string) ([]SearchResult, error) {
	cacheCfg := c.getCacheConfig()
	key := versionedCacheKey(cacheCfg.generation, strings.ToLower(strings.TrimSpace(mediaType)))
	if cached, ok := c.lookupCache(c.feedCache, key); ok {
		return cached, nil
	}
	return c.feedFlight.Do(ctx, key, func(ctx context.Context) ([]SearchResult, error) {
		if err := c.throttle(ctx); err != nil {
			return nil, err
		}
		u, err := c.apiURL()
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("t", "search")
		q.Set("cat", recentCategory(mediaType))
		q.Set("limit", fmt.Sprintf("%d", cacheCfg.feedMaxResults))
		q.Set("extended", "1")
		if apiKey := c.getAPIKey(); apiKey != "" {
			q.Set("apikey", apiKey)
		}
		u.RawQuery = q.Encode()
		results, err := c.doSearchRequest(ctx, u)
		if err == nil {
			c.storeCache(c.feedCache, key, results, cacheCfg.feedTTL, feedCacheMaxEntries)
		}
		return results, err
	})
}

// throttle enforces searchInterval between consecutive Hydra API calls.
func (c *Client) throttle(ctx context.Context) error {
	c.rateMu.Lock()
	if time.Now().Before(c.cooldownUntil) {
		c.rateMu.Unlock()
		return fmt.Errorf("%w until %s", ErrRateLimited, c.cooldownUntil.UTC().Format(time.RFC3339))
	}
	now := time.Now()
	next := c.lastCall.Add(c.searchInterval)
	if next.Before(now) {
		next = now
	}
	prev := c.lastCall
	c.lastCall = next
	c.rateMu.Unlock()
	wait := time.Until(next)
	if wait > 0 {
		select {
		case <-ctx.Done():
			// Revert lastCall so the next call isn't penalised for a cancelled wait.
			c.rateMu.Lock()
			c.lastCall = prev
			c.rateMu.Unlock()
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil
}

// Search fetches results from NZBHydra2 with Radarr/Sonarr-compatible pagination.
// It requests pages of searchPageSize until a partial page is received (indexer
// exhausted) or searchMaxPages is reached (1,000 result cap). Only the first
// page triggers the search throttle; subsequent pages are served from
// NZBHydra2's internal cache and are fetched immediately.
func (c *Client) Search(ctx context.Context, request SearchRequest) ([]SearchResult, error) {
	cacheCfg := c.getCacheConfig()
	key := versionedCacheKey(cacheCfg.generation, searchCacheKey(request))
	if cached, ok := c.lookupCache(c.searchCache, key); ok {
		return cached, nil
	}

	// searchFlight.Do coalesces concurrent identical requests (e.g. two
	// episodes of the same show issuing the same season-pack query) that all
	// miss the cache above at once, so only one of them actually pages
	// through NZBHydra2 — see the Client.searchFlight field comment.
	return c.searchFlight.Do(ctx, key, func(ctx context.Context) ([]SearchResult, error) {
		u, err := c.apiURL()
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("t", requestType(request))
		if cats := searchCategories(request.MediaType); cats != "" {
			q.Set("cat", cats)
		}
		if strings.TrimSpace(request.Query) != "" {
			q.Set("q", request.Query)
		}
		if imdbID := normalizeIMDbID(request.IMDbID); imdbID != "" {
			q.Set("imdbid", imdbID)
		}
		// tmdbid= is the primary ID for both Radarr (movies) and Sonarr (TV).
		// NZBHydra2 forwards it to indexers that support it.
		if request.TMDBID > 0 {
			q.Set("tmdbid", fmt.Sprintf("%d", request.TMDBID))
		}
		if strings.EqualFold(request.MediaType, "episode") || strings.EqualFold(request.MediaType, "tv") {
			if request.TVDBID > 0 {
				q.Set("tvdbid", fmt.Sprintf("%d", request.TVDBID))
			}
			if request.SeasonNumber > 0 {
				q.Set("season", fmt.Sprintf("%d", request.SeasonNumber))
			}
			if request.EpisodeNumber > 0 {
				q.Set("ep", fmt.Sprintf("%d", request.EpisodeNumber))
			}
		}
		q.Set("limit", fmt.Sprintf("%d", searchPageSize))
		q.Set("extended", "1")
		if apiKey := c.getAPIKey(); apiKey != "" {
			q.Set("apikey", apiKey)
		}

		// Acquire concurrency slot — limits simultaneous in-flight NZBHydra2
		// requests to maxConcurrentSearches regardless of worker count.
		select {
		case c.searchSem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-c.searchSem }()

		var allResults []SearchResult
		for page := 0; page < searchMaxPages; page++ {
			// Throttle only the first page — subsequent pages are served from
			// NZBHydra2's internal search cache and don't hit the indexers again.
			if page == 0 {
				if err := c.throttle(ctx); err != nil {
					return nil, err
				}
			}
			q.Set("offset", fmt.Sprintf("%d", page*searchPageSize))
			u.RawQuery = q.Encode()

			pageResults, err := c.doSearchRequest(ctx, u)
			if err != nil {
				if page == 0 {
					return nil, err
				}
				// Partial pagination failure: return what we have.
				break
			}
			allResults = append(allResults, pageResults...)
			// Partial page → indexer has no more results.
			if len(pageResults) < searchPageSize {
				break
			}
		}

		if len(allResults) > 0 {
			c.storeCache(c.searchCache, key, allResults, cacheCfg.searchTTL, searchCacheMaxEntries)
		}
		return allResults, nil
	})
}

func (c *Client) doSearchRequest(ctx context.Context, u *url.URL) ([]SearchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Load().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		c.startCooldown()
		return nil, fmt.Errorf("%w: nzbhydra2 search status %d", ErrRateLimited, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classifyHydraHTTPError(resp.StatusCode, body)
	}
	c.recordSuccess()
	if err := detectHydraResponseError(resp.StatusCode, body); err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "<") {
		return parseXMLResults(body)
	}
	return parseJSONResults(body)
}

// startCooldown begins (or extends) a rate-limit cooldown after a 429
// response, using an escalating backoff schedule (rateLimitBackoff) indexed
// by consecutive rate-limit hits, so repeated 429s back off progressively
// further rather than retrying at the same short interval indefinitely.
func (c *Client) startCooldown() {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	level := c.rateLimitHits
	if level >= len(rateLimitBackoff) {
		level = len(rateLimitBackoff) - 1
	}
	until := time.Now().Add(rateLimitBackoff[level])
	if until.After(c.cooldownUntil) {
		c.cooldownUntil = until
	}
	if c.rateLimitHits < len(rateLimitBackoff)-1 {
		c.rateLimitHits++
	}
}

// recordSuccess is called after every non-429 response. It decays the
// rate-limit-hit counter used by startCooldown and clears an already-expired
// cooldown, so a run of successes gradually restores the normal (unbacked-off)
// retry schedule instead of requiring an explicit reset.
func (c *Client) recordSuccess() {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	if c.rateLimitHits > 0 {
		c.rateLimitHits--
	}
	if time.Now().After(c.cooldownUntil) {
		c.cooldownUntil = time.Time{}
	}
}

func (c *Client) lookupCache(cache map[string]cachedResults, key string) ([]SearchResult, bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	entry, ok := cache[key]
	if !ok {
		return nil, false
	}
	now := time.Now()
	if !now.Before(entry.expiresAt) {
		delete(cache, key)
		return nil, false
	}
	entry.lastUsed = now
	cache[key] = entry
	return cloneResults(entry.results), true
}

func (c *Client) storeCache(cache map[string]cachedResults, key string, results []SearchResult, ttl time.Duration, maxEntries int) {
	if ttl <= 0 || maxEntries <= 0 {
		return
	}
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	now := time.Now()
	for cachedKey, entry := range cache {
		if !now.Before(entry.expiresAt) {
			delete(cache, cachedKey)
		}
	}
	if _, exists := cache[key]; !exists && len(cache) >= maxEntries {
		var oldestKey string
		var oldestTime time.Time
		for cachedKey, entry := range cache {
			if oldestKey == "" || entry.lastUsed.Before(oldestTime) {
				oldestKey = cachedKey
				oldestTime = entry.lastUsed
			}
		}
		delete(cache, oldestKey)
	}
	cache[key] = cachedResults{
		results:   cloneResults(results),
		expiresAt: now.Add(ttl),
		lastUsed:  now,
	}
}

func versionedCacheKey(generation uint64, key string) string {
	return fmt.Sprintf("%d:%s", generation, key)
}

func cloneResults(results []SearchResult) []SearchResult {
	if len(results) == 0 {
		return nil
	}
	out := make([]SearchResult, len(results))
	copy(out, results)
	return out
}

func searchCacheKey(request SearchRequest) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(request.MediaType)),
		strings.ToLower(strings.TrimSpace(request.Query)),
		strings.ToLower(strings.TrimSpace(normalizeIMDbID(request.IMDbID))),
		fmt.Sprintf("%d", request.TMDBID),
		fmt.Sprintf("%d", request.TVDBID),
		fmt.Sprintf("%d", request.SeasonNumber),
		fmt.Sprintf("%d", request.EpisodeNumber),
	}, "|")
}

func requestType(request SearchRequest) string {
	switch strings.ToLower(strings.TrimSpace(request.MediaType)) {
	case "movie":
		return "movie"
	case "episode", "tv":
		return "tvsearch"
	default:
		return "search"
	}
}

func recentCategory(mediaType string) string {
	return searchCategories(mediaType)
}

func searchCategories(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie":
		return strings.Join(movieCategories, ",")
	case "episode", "tv":
		return strings.Join(tvCategories, ",")
	default:
		return ""
	}
}

func normalizeIMDbID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "tt")
	if value == "" {
		return ""
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return value
}

// apiURL resolves the configured base URL to the Newznab API endpoint,
// tolerating a base URL that already includes the "/api" suffix so the
// setting works whether the user enters NZBHydra2's root URL or its API URL.
func (c *Client) apiURL() (*url.URL, error) {
	base := strings.TrimRight(c.getBaseURL(), "/")
	if strings.HasSuffix(strings.ToLower(base), "/api") {
		return url.Parse(base)
	}
	return url.Parse(base + "/api")
}

func parseJSONResults(body []byte) ([]SearchResult, error) {
	var payload struct {
		Results []struct {
			Title        string `json:"title"`
			Link         string `json:"link"`
			Indexer      string `json:"indexer"`
			Size         int64  `json:"size"`
			Grabs        int    `json:"grabs"`
			Password     int    `json:"password"`
			IndexerScore int    `json:"hydraIndexerScore"`
			PubDate      string `json:"pubDate"`
			Published    string `json:"publishedDate"`
			Epoch        int64  `json:"epoch"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	out := make([]SearchResult, 0, len(payload.Results))
	for _, item := range payload.Results {
		out = append(out, SearchResult{
			Title:        item.Title,
			Link:         item.Link,
			Indexer:      item.Indexer,
			SizeBytes:    item.Size,
			PublishedAt:  parsePublished(item.Epoch, item.PubDate, item.Published),
			Grabs:        item.Grabs,
			IndexerScore: item.IndexerScore,
			Passworded:   item.Password != 0,
		})
	}
	return out, nil
}

func parseXMLResults(body []byte) ([]SearchResult, error) {
	var payload struct {
		Channel struct {
			Items []struct {
				Title   string `xml:"title"`
				Link    string `xml:"link"`
				PubDate string `xml:"pubDate"`
				Attrs   []struct {
					Name  string `xml:"name,attr"`
					Value string `xml:"value,attr"`
				} `xml:"attr"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(payload.Channel.Items))
	for _, item := range payload.Channel.Items {
		result := SearchResult{
			Title:       item.Title,
			Link:        item.Link,
			PublishedAt: parsePublished(0, item.PubDate),
		}
		for _, attr := range item.Attrs {
			switch strings.ToLower(strings.TrimSpace(attr.Name)) {
			case "indexer", "hydraindexername":
				result.Indexer = attr.Value
			case "size":
				fmt.Sscan(attr.Value, &result.SizeBytes)
			case "grabs":
				fmt.Sscan(attr.Value, &result.Grabs)
			case "hydraindexerscore":
				fmt.Sscan(attr.Value, &result.IndexerScore)
			case "password":
				var v int
				fmt.Sscan(attr.Value, &v)
				result.Passworded = v != 0
			}
		}
		out = append(out, result)
	}
	return out, nil
}

func parsePublished(epoch int64, values ...string) time.Time {
	if epoch > 0 {
		return time.Unix(epoch, 0).UTC()
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return parsed.UTC()
		}
		if parsed, err := time.Parse(time.RFC1123Z, value); err == nil {
			return parsed.UTC()
		}
		if parsed, err := time.Parse(time.RFC1123, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func classifyHydraHTTPError(statusCode int, body []byte) error {
	return httperr.ClassifyStatus("nzbhydra2", "search", statusCode, body)
}

func detectHydraResponseError(statusCode int, body []byte) error {
	return httperr.DetectResponseError("nzbhydra2", "search", statusCode, body)
}

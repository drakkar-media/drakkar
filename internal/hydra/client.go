package hydra

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/httperr"
)

// defaultSearchInterval is the minimum gap between consecutive Hydra API calls.
// 0 matches Sonarr/Radarr behaviour: do not add client-side throttling unless
// explicitly configured by the user.
var defaultSearchInterval time.Duration

var ErrRateLimited = errors.New("nzbhydra2 rate limited")

const (
	searchPageSize = 100 // results per page — matches Radarr/Sonarr PageSize
	searchMaxPages = 30  // max pages per request → 3,000 results cap (matches Radarr/Sonarr)
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

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client

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

	cacheMu        sync.Mutex
	searchCacheTTL time.Duration
	feedCacheTTL   time.Duration
	feedMaxResults int
	searchCache    map[string]cachedResults
	feedCache      map[string]cachedResults

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

type cachedResults struct {
	results   []SearchResult
	expiresAt time.Time
}

// hydraFlight deduplicates concurrent calls that share a key, so only one
// goroutine actually runs the fetch while the rest wait for and share its
// result. Mirrors internal/cache.SingleFlight's lock+channel design.
type hydraFlight struct {
	mu      sync.Mutex
	flights map[string]*hydraFlightCall
}

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

type SearchRequest struct {
	MediaType     string
	Query         string
	IMDbID        string
	TMDBID        int64 // tmdbid= parameter (Radarr/Sonarr first-tier ID search)
	TVDBID        int64
	SeasonNumber  int
	EpisodeNumber int
}

func NewClient(cfg config.ServiceConfig) *Client {
	searchCacheTTL := time.Duration(cfg.SearchCacheTTLSeconds) * time.Second
	if searchCacheTTL < 0 {
		searchCacheTTL = 0
	}
	feedCacheTTL := time.Duration(cfg.FeedCacheTTLSeconds) * time.Second
	if feedCacheTTL < 0 {
		feedCacheTTL = 0
	}
	feedMaxResults := cfg.FeedMaxResults
	if feedMaxResults <= 0 {
		feedMaxResults = 1200
	}
	// 3 concurrent searches: each *arr app (Radarr, Sonarr, ...) independently
	// enforces its own 2-second per-indexer rate limit (HttpIndexerBase.RateLimit),
	// not a global one — running Radarr+Sonarr together against the same
	// NZBHydra2 instance already produces 2+ concurrent requests in practice.
	// NZBHydra2 itself also applies its own per-indexer pacing toward the real
	// Usenet indexers, so this client-side cap only needs to bound Drakkar's own
	// worker pool, not re-implement indexer rate limiting from scratch.
	const maxConcurrentSearches = 3
	return &Client{
		baseURL: strings.TrimRight(cfg.URL, "/"),
		apiKey:  cfg.APIKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		searchInterval: defaultSearchInterval,
		searchSem:      make(chan struct{}, maxConcurrentSearches),
		searchCacheTTL: searchCacheTTL,
		feedCacheTTL:   feedCacheTTL,
		feedMaxResults: feedMaxResults,
		searchCache:    make(map[string]cachedResults),
		feedCache:      make(map[string]cachedResults),
		searchFlight:   newHydraFlight(),
		feedFlight:     newHydraFlight(),
	}
}

// SetSearchDelay configures the minimum delay between consecutive Hydra API
// calls. 0 means no delay (matches Sonarr/Radarr behaviour).
func (c *Client) SetSearchDelay(d time.Duration) {
	c.rateMu.Lock()
	c.searchInterval = d
	c.rateMu.Unlock()
}

func (c *Client) Name() string {
	return "nzbhydra2"
}

func (c *Client) Probe(ctx context.Context) error {
	u, err := c.apiURL()
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("t", "caps")
	if c.apiKey != "" {
		q.Set("apikey", c.apiKey)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("nzbhydra2 caps status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) SearchRecent(ctx context.Context, mediaType string) ([]SearchResult, error) {
	if cached, ok := c.lookupFeedCache(mediaType); ok {
		return cached, nil
	}
	key := strings.ToLower(strings.TrimSpace(mediaType))
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
		q.Set("limit", fmt.Sprintf("%d", c.feedMaxResults))
		q.Set("extended", "1")
		if c.apiKey != "" {
			q.Set("apikey", c.apiKey)
		}
		u.RawQuery = q.Encode()
		results, err := c.doSearchRequest(ctx, u)
		if err == nil {
			c.storeFeedCache(mediaType, results)
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
	if cached, ok := c.lookupSearchCache(request); ok {
		return cached, nil
	}

	// searchFlight.Do coalesces concurrent identical requests (e.g. two
	// episodes of the same show issuing the same season-pack query) that all
	// miss the cache above at once, so only one of them actually pages
	// through NZBHydra2 — see the Client.searchFlight field comment.
	key := searchCacheKey(request)
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
		if c.apiKey != "" {
			q.Set("apikey", c.apiKey)
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
			c.storeSearchCache(request, allResults)
		}
		return allResults, nil
	})
}

func (c *Client) doSearchRequest(ctx context.Context, u *url.URL) ([]SearchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
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

func (c *Client) lookupSearchCache(request SearchRequest) ([]SearchResult, bool) {
	return c.lookupCache(c.searchCache, searchCacheKey(request))
}

func (c *Client) storeSearchCache(request SearchRequest, results []SearchResult) {
	c.storeCache(c.searchCache, searchCacheKey(request), results, c.searchCacheTTL)
}

func (c *Client) lookupFeedCache(mediaType string) ([]SearchResult, bool) {
	return c.lookupCache(c.feedCache, strings.ToLower(strings.TrimSpace(mediaType)))
}

func (c *Client) storeFeedCache(mediaType string, results []SearchResult) {
	c.storeCache(c.feedCache, strings.ToLower(strings.TrimSpace(mediaType)), results, c.feedCacheTTL)
}

func (c *Client) lookupCache(cache map[string]cachedResults, key string) ([]SearchResult, bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	entry, ok := cache[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(cache, key)
		return nil, false
	}
	return cloneResults(entry.results), true
}

func (c *Client) storeCache(cache map[string]cachedResults, key string, results []SearchResult, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	cache[key] = cachedResults{
		results:   cloneResults(results),
		expiresAt: time.Now().Add(ttl),
	}
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

func (c *Client) apiURL() (*url.URL, error) {
	base := strings.TrimRight(c.baseURL, "/")
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

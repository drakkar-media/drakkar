// Package tvdb implements a metadata client for the TheTVDB v4 REST API,
// used to enrich TV library items with series titles, years, and IMDb IDs.
package tvdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/mediadate"
)

// Client implements metadata lookups against the TheTVDB v4 API.
//
// TheTVDB requires a login exchange (API key -> bearer token) rather than a
// per-request API key, so Client caches the resulting token under mu for up
// to 29 days and transparently re-authenticates when it expires or is
// invalidated. Client is safe for concurrent use.
type Client struct {
	baseURL    string
	httpClient *http.Client

	mu        sync.Mutex
	apiKey    string
	token     string
	tokenTime time.Time
}

// SeriesDetails is the flattened, application-facing view of a TheTVDB
// "series extended" response.
type SeriesDetails struct {
	Name   string
	Year   int
	IMDbID string
}

// NewClient creates a Client configured with cfg's TVDB API key.
func NewClient(cfg config.MetadataConfig) *Client {
	c := &Client{
		baseURL:    "https://api4.thetvdb.com/v4",
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	c.SetConfig(cfg)
	return c
}

// SetConfig updates the API key live, e.g. after a settings save. Clears
// the cached login token so the next call re-authenticates with the new key.
func (c *Client) SetConfig(cfg config.MetadataConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiKey = strings.TrimSpace(cfg.TVDB.APIKey)
	c.token = ""
}

// getAPIKey returns the currently configured API key, or "" if none has
// been set.
func (c *Client) getAPIKey() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.apiKey
}

// Enabled reports whether c is non-nil and has a configured API key. Callers
// use this to skip TVDB lookups entirely when the integration is disabled,
// rather than relying on every request failing.
func (c *Client) Enabled() bool {
	return c != nil && strings.TrimSpace(c.getAPIKey()) != ""
}

// SeriesDetails fetches series metadata, retrying once with a freshly
// logged-in token on a 401. tokenValue caches the login token for 29 days
// and previously never invalidated it on an actual auth failure — if TVDB
// revoked/rotated the token server-side before then, every call would keep
// resending the same dead token and fail with 401 for up to 29 days.
func (c *Client) SeriesDetails(ctx context.Context, tvdbID int64) (SeriesDetails, error) {
	out, status, err := c.seriesDetailsOnce(ctx, tvdbID)
	if status != http.StatusUnauthorized {
		return out, err
	}
	c.invalidateToken()
	out, _, err = c.seriesDetailsOnce(ctx, tvdbID)
	return out, err
}

// seriesDetailsOnce performs a single series-details request without
// retrying on 401, returning the response status alongside the result so
// SeriesDetails can decide whether to re-authenticate and retry.
func (c *Client) seriesDetailsOnce(ctx context.Context, tvdbID int64) (SeriesDetails, int, error) {
	req, err := c.newRequest(ctx, http.MethodGet, c.baseURL+"/series/"+strconv.FormatInt(tvdbID, 10)+"/extended", nil)
	if err != nil {
		return SeriesDetails{}, 0, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return SeriesDetails{}, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SeriesDetails{}, resp.StatusCode, fmt.Errorf("tvdb series status %d", resp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return SeriesDetails{}, resp.StatusCode, err
	}
	data, _ := payload["data"].(map[string]any)
	out := SeriesDetails{
		Name: firstString(data, "name", "seriesName"),
		Year: firstInt(data, "year"),
	}
	if out.Year == 0 {
		out.Year = mediadate.Year(firstString(data, "firstAired", "first_air_time", "first_air_date"))
	}
	out.IMDbID = extractIMDbID(data)
	return out, resp.StatusCode, nil
}

// newRequest builds an authenticated TVDB API request, obtaining a bearer
// token via tokenValue (logging in or reusing the cached token as needed).
func (c *Client) newRequest(ctx context.Context, method, rawURL string, body []byte) (*http.Request, error) {
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.tokenValue(ctx))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// tokenValue returns a valid bearer token, reusing the cached token while it
// is younger than 29 days (TheTVDB's token lifetime) and logging in for a
// fresh one otherwise. A login failure yields "" rather than an error, since
// the resulting request will simply fail its own auth check downstream.
func (c *Client) tokenValue(ctx context.Context) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Since(c.tokenTime) < 29*24*time.Hour {
		return c.token
	}
	apiKey := c.apiKey
	token, err := c.login(ctx, apiKey)
	if err != nil {
		return ""
	}
	c.token = token
	c.tokenTime = time.Now().UTC()
	return c.token
}

// invalidateToken clears the cached login token so the next tokenValue call
// re-authenticates instead of resending a token TVDB has already rejected.
func (c *Client) invalidateToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
}

// login exchanges apiKey for a fresh TheTVDB bearer token.
func (c *Client) login(ctx context.Context, apiKey string) (string, error) {
	payload := map[string]string{"apikey": apiKey}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/login", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("tvdb login status %d", resp.StatusCode)
	}
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.Data.Token), nil
}

// firstString returns the first non-blank string value found in values
// under any of keys, trying keys in order — used because TheTVDB's field
// names vary across API versions/endpoints (e.g. "name" vs "seriesName").
func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// firstInt returns the first int-like value (JSON numbers decode as
// float64) found in values under any of keys, trying keys in order.
func firstInt(values map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := values[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		}
	}
	return 0
}

// extractIMDbID resolves an IMDb ID from a TheTVDB series payload, first
// checking direct imdbId/imdb_id fields and falling back to scanning the
// remoteIds list for an entry whose source name mentions IMDb — TheTVDB
// exposes the ID either way depending on the endpoint.
func extractIMDbID(values map[string]any) string {
	if imdb := firstString(values, "imdbId", "imdb_id"); imdb != "" {
		return imdb
	}
	remoteIDs, _ := values["remoteIds"].([]any)
	for _, raw := range remoteIDs {
		item, _ := raw.(map[string]any)
		source := strings.ToLower(firstString(item, "sourceName", "type"))
		if strings.Contains(source, "imdb") {
			if value := firstString(item, "id", "value"); value != "" {
				return value
			}
		}
	}
	return ""
}

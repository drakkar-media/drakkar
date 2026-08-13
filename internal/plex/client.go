// Package plex provides a minimal Plex Media Server client for triggering
// library section refreshes after Drakkar publishes a new media file.
package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/drakkar-media/drakkar/internal/mediaserver"
)

type plexEndpoint struct {
	serverURL string
	token     string
}

// Client calls the Plex HTTP API.
//
// The server URL and token are held behind an atomic.Pointer so that
// SetConfig can swap them in place (e.g. when the user updates Plex settings
// or completes OAuth) without disrupting requests already in flight on other
// goroutines. Client is safe for concurrent use.
type Client struct {
	endpoint   atomic.Pointer[plexEndpoint]
	httpClient *http.Client
	cloudURL   string
}

// SetConfig atomically replaces the server URL and token used by subsequent
// requests, allowing configuration to be hot-reloaded (e.g. after a settings
// save) without recreating the Client or interrupting in-flight requests.
func (c *Client) SetConfig(serverURL, token string) {
	c.endpoint.Store(&plexEndpoint{serverURL: strings.TrimRight(serverURL, "/"), token: token})
}

// Library is a Plex library section.
type Library struct {
	Key       string   `json:"key"`
	Title     string   `json:"title"`
	Type      string   `json:"type"` // "movie" or "show"
	Agent     string   `json:"agent"`
	Locations []string `json:"locations,omitempty"`
}

// TestResult is returned from a connection test.
type TestResult struct {
	OK         bool      `json:"ok"`
	ServerName string    `json:"serverName"`
	Libraries  []Library `json:"libraries"`
	Error      string    `json:"error,omitempty"`
}

// NewClient creates a Client targeting the given Plex server URL, authenticated
// with the given token. Either value may be empty; use SetConfig later to
// supply them once available, and Enabled to check readiness before use.
func NewClient(serverURL, token string) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		cloudURL:   "https://discover.provider.plex.tv",
	}
	c.SetConfig(serverURL, token)
	return c
}

func (c *Client) getEndpoint() plexEndpoint {
	if e := c.endpoint.Load(); e != nil {
		return *e
	}
	return plexEndpoint{}
}

// Enabled reports whether both a server URL and a token are configured.
// Every method that talks to Plex treats a disabled client as a no-op rather
// than an error, since Plex integration is optional and its absence is not a
// failure condition.
func (c *Client) Enabled() bool {
	e := c.getEndpoint()
	return c != nil && strings.TrimSpace(e.serverURL) != "" && strings.TrimSpace(e.token) != ""
}

// Test verifies connectivity and returns the server name + library list.
func (c *Client) Test(ctx context.Context) TestResult {
	if !c.Enabled() {
		return TestResult{Error: "plex not configured"}
	}
	// Fetch server info
	type serverInfo struct {
		MediaContainer struct {
			FriendlyName string `json:"friendlyName"`
		} `json:"MediaContainer"`
	}
	var info serverInfo
	if err := c.get(ctx, "/", &info); err != nil {
		return TestResult{Error: err.Error()}
	}
	libs, err := c.Libraries(ctx)
	if err != nil {
		return TestResult{OK: true, ServerName: info.MediaContainer.FriendlyName, Error: err.Error()}
	}
	return TestResult{
		OK:         true,
		ServerName: info.MediaContainer.FriendlyName,
		Libraries:  libs,
	}
}

// Libraries returns all library sections from the Plex server.
func (c *Client) Libraries(ctx context.Context) ([]Library, error) {
	type response struct {
		MediaContainer struct {
			Directory []struct {
				Key      string `json:"key"`
				Title    string `json:"title"`
				Type     string `json:"type"`
				Agent    string `json:"agent"`
				Location []struct {
					Path string `json:"path"`
				} `json:"Location"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	var resp response
	if err := c.get(ctx, "/library/sections", &resp); err != nil {
		return nil, err
	}
	out := make([]Library, 0, len(resp.MediaContainer.Directory))
	for _, d := range resp.MediaContainer.Directory {
		lib := Library{Key: d.Key, Title: d.Title, Type: d.Type, Agent: d.Agent}
		for _, location := range d.Location {
			if strings.TrimSpace(location.Path) != "" {
				lib.Locations = append(lib.Locations, location.Path)
			}
		}
		out = append(out, lib)
	}
	return out, nil
}

// RefreshPathAuto triggers a path refresh using either the configured section
// key or automatic section detection from Plex library root locations.
func (c *Client) RefreshPathAuto(ctx context.Context, preferredSectionKey, filePath string) error {
	if !c.Enabled() {
		return nil
	}
	filePath = filepath.Clean(strings.TrimSpace(filePath))
	if filePath == "" {
		if preferredSectionKey != "" {
			return c.RefreshSection(ctx, preferredSectionKey)
		}
		return nil
	}
	if preferredSectionKey != "" {
		return c.RefreshPath(ctx, preferredSectionKey, filePath)
	}
	libs, err := c.Libraries(ctx)
	if err != nil {
		return err
	}
	candidates := matchingLibrariesForPath(libs, filePath)
	if len(candidates) == 0 {
		// filePath doesn't fall under any known Plex library location -- most
		// likely a mismatch between Drakkar's configured library paths and
		// what Plex itself reports (different mount inside the Plex
		// container/host). Refreshing every section won't actually make
		// Plex pick up the file, so without this warning the underlying
		// misconfiguration is invisible: the refresh calls below still
		// return success.
		slog.Warn("plex: path matched no known library location, refreshing all sections as a fallback",
			"path", filePath, "libraryCount", len(libs))
		candidates = libs
	}
	var firstErr error
	for _, lib := range candidates {
		if err := c.RefreshPath(ctx, lib.Key, filePath); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// matchingLibrariesForPath returns the libraries whose root Location covers
// filePath, matched by exact equality or path-prefix containment. A file can
// match more than one library location.
func matchingLibrariesForPath(libs []Library, filePath string) []Library {
	filePath = filepath.Clean(filePath)
	var out []Library
	for _, lib := range libs {
		for _, root := range lib.Locations {
			root = filepath.Clean(strings.TrimSpace(root))
			if root == "" {
				continue
			}
			if filePath == root || strings.HasPrefix(filePath, root+string(filepath.Separator)) {
				out = append(out, lib)
				break
			}
		}
	}
	return out
}

// RefreshSection triggers a full scan of a library section by key.
// If key is empty, refreshes all sections.
func (c *Client) RefreshSection(ctx context.Context, sectionKey string) error {
	if !c.Enabled() {
		return nil
	}
	if sectionKey == "" {
		libs, err := c.Libraries(ctx)
		if err != nil {
			return err
		}
		for _, lib := range libs {
			if err := c.refreshSection(ctx, lib.Key); err != nil {
				return err
			}
		}
		return nil
	}
	return c.refreshSection(ctx, sectionKey)
}

// RefreshPath triggers a targeted scan of a specific file path within Plex.
// This is faster than a full section scan.
func (c *Client) RefreshPath(ctx context.Context, sectionKey, filePath string) error {
	if !c.Enabled() {
		return nil
	}
	endpoint := fmt.Sprintf("/library/sections/%s/refresh?path=%s", sectionKey, url.QueryEscape(filePath))
	return c.get(ctx, endpoint, nil)
}

// RemoveFromWatchlist removes a movie or show identified by its TMDB ID from
// the Plex account watchlist associated with the configured token. Plex's
// cloud API requires its own rating key, so the method resolves watchlist
// entries through their provider metadata before issuing the idempotent
// remove action.
func (c *Client) RemoveFromWatchlist(ctx context.Context, mediaType string, tmdbID int64) (bool, error) {
	if !c.Enabled() || tmdbID <= 0 {
		return false, nil
	}
	wantedType := strings.ToLower(strings.TrimSpace(mediaType))
	if wantedType == "tv" || wantedType == "episode" {
		wantedType = "show"
	}
	if wantedType != "movie" && wantedType != "show" {
		return false, fmt.Errorf("plex watchlist: unsupported media type %q", mediaType)
	}

	const pageSize = 100
	for offset := 0; ; offset += pageSize {
		path := fmt.Sprintf(
			"/library/sections/watchlist/all?X-Plex-Container-Start=%d&X-Plex-Container-Size=%d",
			offset, pageSize,
		)
		var page plexWatchlistPage
		status, err := c.cloudRequest(ctx, http.MethodGet, path, &page)
		if err != nil {
			return false, fmt.Errorf("plex watchlist list: %w", err)
		}
		if status == http.StatusNotFound {
			return false, nil
		}
		for _, item := range page.MediaContainer.Metadata {
			matches, err := c.watchlistItemMatches(ctx, item.RatingKey, wantedType, tmdbID)
			if err != nil {
				return false, err
			}
			if !matches {
				continue
			}
			removePath := "/actions/removeFromWatchlist?ratingKey=" + url.QueryEscape(item.RatingKey)
			status, err := c.cloudRequest(ctx, http.MethodPut, removePath, nil)
			if err != nil {
				return false, fmt.Errorf("plex watchlist remove: %w", err)
			}
			if status == http.StatusNotFound {
				return false, nil
			}
			return true, nil
		}
		if len(page.MediaContainer.Metadata) == 0 || offset+pageSize >= page.MediaContainer.TotalSize {
			return false, nil
		}
	}
}

type plexWatchlistPage struct {
	MediaContainer struct {
		TotalSize int `json:"totalSize"`
		Metadata  []struct {
			RatingKey string `json:"ratingKey"`
		} `json:"Metadata"`
	} `json:"MediaContainer"`
}

type plexCloudMetadata struct {
	MediaContainer struct {
		Metadata []struct {
			Type string    `json:"type"`
			Guid plexGUIDs `json:"Guid"`
		} `json:"Metadata"`
		Video []struct {
			Type string    `json:"type"`
			Guid plexGUIDs `json:"Guid"`
		} `json:"Video"`
	} `json:"MediaContainer"`
}

type plexGUIDs []string

func (g *plexGUIDs) UnmarshalJSON(data []byte) error {
	var arr []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &arr); err == nil {
		*g = (*g)[:0]
		for _, item := range arr {
			if strings.TrimSpace(item.ID) != "" {
				*g = append(*g, item.ID)
			}
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if strings.TrimSpace(s) == "" {
			*g = nil
		} else {
			*g = []string{s}
		}
	}
	return nil
}

func (c *Client) watchlistItemMatches(ctx context.Context, ratingKey, wantedType string, tmdbID int64) (bool, error) {
	var payload plexCloudMetadata
	status, err := c.cloudRequest(ctx, http.MethodGet, "/library/metadata/"+url.PathEscape(ratingKey), &payload)
	if err != nil {
		return false, fmt.Errorf("plex watchlist metadata %s: %w", ratingKey, err)
	}
	if status == http.StatusNotFound {
		return false, nil
	}
	type guidItem struct {
		mediaType string
		guids     []string
	}
	items := make([]guidItem, 0, len(payload.MediaContainer.Metadata)+len(payload.MediaContainer.Video))
	for _, item := range payload.MediaContainer.Metadata {
		entry := guidItem{mediaType: item.Type, guids: item.Guid}
		items = append(items, entry)
	}
	for _, item := range payload.MediaContainer.Video {
		entry := guidItem{mediaType: item.Type, guids: item.Guid}
		items = append(items, entry)
	}
	needle := fmt.Sprintf("tmdb://%d", tmdbID)
	for _, item := range items {
		if !strings.EqualFold(item.mediaType, wantedType) {
			continue
		}
		for _, guid := range item.guids {
			if strings.EqualFold(guid, needle) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (c *Client) cloudRequest(ctx context.Context, method, requestPath string, out any) (int, error) {
	e := c.getEndpoint()
	cloudURL := strings.TrimRight(c.cloudURL, "/")
	if cloudURL == "" {
		cloudURL = "https://discover.provider.plex.tv"
	}
	req, err := http.NewRequestWithContext(ctx, method, cloudURL+requestPath, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Plex-Token", e.token)
	req.Header.Set("X-Plex-Product", "Drakkar")
	req.Header.Set("X-Plex-Client-Identifier", "drakkar")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("plex cloud HTTP %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func (c *Client) refreshSection(ctx context.Context, sectionKey string) error {
	return c.get(ctx, fmt.Sprintf("/library/sections/%s/refresh", sectionKey), nil)
}

func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	e := c.getEndpoint()
	return mediaserver.Get(ctx, c.httpClient, e.serverURL, path, "X-Plex-Token", e.token, "plex", out)
}

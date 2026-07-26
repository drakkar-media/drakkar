// Package opensubtitles implements a subtitles.Provider backed by the
// OpenSubtitles.com REST API, handling authentication token caching and
// search/download translation to the shared subtitles types.
package opensubtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/subtitles"
	"github.com/drakkar-media/drakkar/internal/subtitleutil"
	"github.com/drakkar-media/drakkar/internal/version"
)

// Client implements subtitles.Provider against the OpenSubtitles.com API.
//
// Authentication is optional: if username/password are unset, Search runs
// unauthenticated (API-key only) since login is only required for
// Download and for authenticated search quotas. When credentials are
// present, the session token and the per-account API host it grants are
// cached and shared across calls; Client is safe for concurrent use.
type Client struct {
	apiKey     string
	username   string
	password   string
	baseURL    string
	httpClient *http.Client
	userAgent  string

	mu        sync.Mutex
	token     string
	tokenTime time.Time
	apiHost   string
}

// NewClient creates a Client for the OpenSubtitles.com API using the given
// credentials. Username/password may be empty, in which case authenticated
// operations (Download, Probe) will fail but unauthenticated Search still
// works.
func NewClient(auth config.SubtitleAuth) *Client {
	return &Client{
		apiKey:     strings.TrimSpace(auth.APIKey),
		username:   strings.TrimSpace(auth.Username),
		password:   strings.TrimSpace(auth.Password),
		baseURL:    "https://api.opensubtitles.com/api/v1",
		httpClient: &http.Client{Timeout: 30 * time.Second},
		userAgent:  "Drakkar v" + version.Version,
	}
}

// Name returns the provider identifier "opensubtitles", used to route
// stored subtitle candidates back to this provider.
func (c *Client) Name() string {
	return "opensubtitles"
}

// Probe verifies that the configured credentials can authenticate against
// the OpenSubtitles API, forcing a login if no cached token is available.
// It is used for connectivity/settings checks rather than by the search or
// download paths themselves.
func (c *Client) Probe(ctx context.Context) error {
	_, err := c.tokenValue(ctx)
	return err
}

// Search queries OpenSubtitles for subtitles matching input, preferring a
// TMDB ID lookup when available and falling back to a title/year query
// otherwise, restricted to languages when non-empty. Authentication is
// attempted only if credentials are configured; an anonymous request is
// sent otherwise. Each matching file is expanded into its own
// ProviderCandidate since a single OpenSubtitles entry may bundle several
// files (e.g. one per episode in a season pack).
func (c *Client) Search(ctx context.Context, input database.SubtitleSearchInput, languages []string) ([]subtitles.ProviderCandidate, error) {
	reqURL, err := url.Parse(c.apiBaseURL() + "/subtitles")
	if err != nil {
		return nil, err
	}
	q := reqURL.Query()
	if input.TMDBID > 0 {
		q.Set("tmdb_id", strconv.FormatInt(input.TMDBID, 10))
	} else {
		q.Set("query", subtitleutil.SearchTitle(input))
	}
	if mediaType := typeForSearch(input.MediaType); mediaType != "" {
		q.Set("type", mediaType)
	}
	if joined := normalizeLanguages(languages); joined != "" {
		q.Set("languages", joined)
	}
	if year := subtitleutil.SearchYear(input); year > 0 {
		q.Set("year", strconv.Itoa(year))
	}
	if input.SeasonNumber > 0 {
		q.Set("season_number", strconv.Itoa(input.SeasonNumber))
	}
	if input.EpisodeNumber > 0 {
		q.Set("episode_number", strconv.Itoa(input.EpisodeNumber))
	}
	q.Set("order_by", "download_count")
	q.Set("order_direction", "desc")
	reqURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, err
	}
	if err := c.authorize(ctx, req, false); err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("opensubtitles search status %d", resp.StatusCode)
	}

	var payload struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Language       string `json:"language"`
				Release        string `json:"release"`
				FeatureDetails struct {
					Title         string `json:"title"`
					MovieName     string `json:"movie_name"`
					SeasonNumber  int    `json:"season_number"`
					EpisodeNumber int    `json:"episode_number"`
				} `json:"feature_details"`
				HearingImpaired bool `json:"hearing_impaired"`
				Files           []struct {
					FileID   int64  `json:"file_id"`
					FileName string `json:"file_name"`
				} `json:"files"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	out := make([]subtitles.ProviderCandidate, 0, len(payload.Data))
	for _, item := range payload.Data {
		for _, file := range item.Attributes.Files {
			format := strings.TrimPrefix(strings.ToLower(path.Ext(file.FileName)), ".")
			if format == "" {
				format = "srt"
			}
			out = append(out, subtitles.ProviderCandidate{
				Language:        strings.ToLower(strings.TrimSpace(item.Attributes.Language)),
				Title:           subtitleutil.FirstNonEmpty(item.Attributes.FeatureDetails.Title, item.Attributes.FeatureDetails.MovieName, file.FileName),
				ReleaseName:     subtitleutil.FirstNonEmpty(item.Attributes.Release, file.FileName),
				Format:          format,
				HearingImpaired: item.Attributes.HearingImpaired,
				ExternalID:      strconv.FormatInt(file.FileID, 10),
				DownloadURL:     strconv.FormatInt(file.FileID, 10),
				SeasonNumber:    item.Attributes.FeatureDetails.SeasonNumber,
				EpisodeNumber:   item.Attributes.FeatureDetails.EpisodeNumber,
			})
		}
	}
	return out, nil
}

// Download resolves rawURL — the file ID string previously returned as
// ProviderCandidate.DownloadURL — through OpenSubtitles' two-step download
// flow (POST /download to obtain a signed, time-limited link, then GET that
// link) and returns the subtitle's filename and contents. The response body
// is capped at 2 MiB, well beyond any legitimate subtitle file size.
func (c *Client) Download(ctx context.Context, rawURL string) (string, []byte, error) {
	fileID, err := strconv.ParseInt(strings.TrimSpace(rawURL), 10, 64)
	if err != nil {
		return "", nil, fmt.Errorf("invalid opensubtitles file id %q", rawURL)
	}

	payload, err := json.Marshal(map[string]int64{"file_id": fileID})
	if err != nil {
		return "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBaseURL()+"/download", bytes.NewReader(payload))
	if err != nil {
		return "", nil, err
	}
	if err := c.authorize(ctx, req, true); err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("opensubtitles download status %d", resp.StatusCode)
	}
	var out struct {
		Link     string `json:"link"`
		FileName string `json:"file_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", nil, err
	}
	downloadReq, err := http.NewRequestWithContext(ctx, http.MethodGet, out.Link, nil)
	if err != nil {
		return "", nil, err
	}
	downloadResp, err := c.httpClient.Do(downloadReq)
	if err != nil {
		return "", nil, err
	}
	defer downloadResp.Body.Close()
	if downloadResp.StatusCode < 200 || downloadResp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("opensubtitles file status %d", downloadResp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(downloadResp.Body, 2<<20+1))
	if err != nil {
		return "", nil, err
	}
	name := strings.TrimSpace(out.FileName)
	if name == "" {
		name = path.Base(downloadReq.URL.Path)
	}
	if name == "." || name == "/" || name == "" {
		name = "subtitle.srt"
	}
	return name, body, nil
}

// authorize sets the API key and User-Agent headers on req and, when
// requireToken is set or credentials are configured, attaches a bearer
// token obtained (or reused) via tokenValue. Search treats a missing token
// as acceptable (requireToken=false) since it can run unauthenticated;
// Download requires one.
func (c *Client) authorize(ctx context.Context, req *http.Request, requireToken bool) error {
	req.Header.Set("Api-Key", c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if !requireToken && (c.username == "" || c.password == "") {
		return nil
	}
	token, err := c.tokenValue(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// tokenValue returns a cached session token, re-authenticating via login
// when none is cached or the cached one is older than 11 hours (just under
// OpenSubtitles' documented 24-hour token lifetime, leaving margin for
// clock drift and in-flight requests).
func (c *Client) tokenValue(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Since(c.tokenTime) < 11*time.Hour {
		return c.token, nil
	}
	token, baseURL, err := c.login(ctx)
	if err != nil {
		return "", err
	}
	c.token = token
	c.tokenTime = time.Now().UTC()
	c.apiHost = normalizeAPIHost(baseURL)
	return c.token, nil
}

func (c *Client) login(ctx context.Context) (string, string, error) {
	payload, err := json.Marshal(map[string]string{
		"username": c.username,
		"password": c.password,
	})
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/login", bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Api-Key", c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("opensubtitles login status %d", resp.StatusCode)
	}
	var out struct {
		Token   string `json:"token"`
		BaseURL string `json:"base_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(out.Token), strings.TrimSpace(out.BaseURL), nil
}

// apiBaseURL returns the API host to use for requests: OpenSubtitles may
// redirect authenticated accounts to a dedicated host on login, so the
// login-supplied host (once known) takes precedence over the default.
func (c *Client) apiBaseURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.apiHost != "" {
		return c.apiHost
	}
	return c.baseURL
}

// normalizeAPIHost turns a bare host or partial URL returned by the login
// endpoint's base_url field into a fully-qualified "https://.../api/v1"
// base URL, defaulting to the standard API host when value is empty.
func normalizeAPIHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "https://api.opensubtitles.com/api/v1"
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	value = strings.TrimRight(value, "/")
	if !strings.HasSuffix(value, "/api/v1") {
		value += "/api/v1"
	}
	return value
}

func normalizeLanguages(values []string) string {
	var out []string
	seen := make(map[string]struct{})
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return strings.Join(out, ",")
}

func typeForSearch(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie":
		return "movie"
	case "episode", "tv":
		return "episode"
	default:
		return ""
	}
}

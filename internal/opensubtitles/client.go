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

func (c *Client) Name() string {
	return "opensubtitles"
}

func (c *Client) Probe(ctx context.Context) error {
	_, err := c.tokenValue(ctx)
	return err
}

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

func (c *Client) apiBaseURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.apiHost != "" {
		return c.apiHost
	}
	return c.baseURL
}

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

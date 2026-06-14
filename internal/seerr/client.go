package seerr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hjongedijk/drakkar/internal/config"
)

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

type Request struct {
	ID            int64
	Type          string
	Status        int
	MediaTitle    string
	MediaYear     int
	TMDBID        int64
	TVDBID        int64
	SeasonNumber  int
	EpisodeNumber int
	EpisodeTitle  string
}

func NewClient(cfg config.ServiceConfig) *Client {
	return &Client{
		baseURL: strings.TrimRight(cfg.URL, "/"),
		apiKey:  cfg.APIKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) Name() string {
	return "seerr"
}

func (c *Client) Probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/status", nil)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("X-Api-Key", c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("seerr status probe status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) PendingRequests(ctx context.Context) ([]Request, error) {
	const pageSize = 5000
	var out []Request
	for skip := 0; ; skip += pageSize {
		payload, err := c.fetchRequestPage(ctx, skip, pageSize)
		if err != nil {
			return nil, err
		}
		for _, item := range payload.Results {
			// Only skip explicitly declined requests (status 3).
			// Import pending (1), approved (2), available (4), and failed (5) so
			// Drakkar can serve all items regardless of what another downloader did.
			if item.Status == 3 {
				continue
			}
			title := strings.TrimSpace(item.Media.Title)
			if title == "" {
				title = strings.TrimSpace(item.Media.Name)
			}
			year := parseYear(item.Media.ReleaseDate)
			if year == 0 {
				year = parseYear(item.Media.FirstAirDate)
			}
			request := Request{
				ID:         item.ID,
				Type:       item.Type,
				Status:     item.Status,
				MediaTitle: title,
				MediaYear:  year,
				TMDBID:     item.Media.TMDBID,
				TVDBID:     item.Media.TVDBID,
			}
			if len(item.Episodes) > 0 {
				request.SeasonNumber = item.Episodes[0].SeasonNumber
				request.EpisodeNumber = item.Episodes[0].EpisodeNumber
				request.EpisodeTitle = item.Episodes[0].Name
			}
			out = append(out, request)
		}
		if payload.PageInfo.Results <= skip+pageSize || len(payload.Results) == 0 {
			break
		}
	}
	return out, nil
}

type requestListPayload struct {
	PageInfo struct {
		Results  int `json:"results"`
		PageSize int `json:"pageSize"`
		Page     int `json:"page"`
	} `json:"pageInfo"`
	Results []struct {
		ID     int64  `json:"id"`
		Type   string `json:"type"`
		Status int    `json:"status"`
		Media  struct {
			TMDBID       int64  `json:"tmdbId"`
			TVDBID       int64  `json:"tvdbId"`
			Title        string `json:"title"`
			Name         string `json:"name"`
			ReleaseDate  string `json:"releaseDate"`
			FirstAirDate string `json:"firstAirDate"`
		} `json:"media"`
		Episodes []struct {
			SeasonNumber  int    `json:"seasonNumber"`
			EpisodeNumber int    `json:"episodeNumber"`
			Name          string `json:"name"`
		} `json:"episodes"`
	} `json:"results"`
}

func (c *Client) fetchRequestPage(ctx context.Context, skip, take int) (requestListPayload, error) {
	u, err := url.Parse(c.baseURL + "/api/v1/request")
	if err != nil {
		return requestListPayload{}, err
	}
	q := u.Query()
	q.Set("take", strconv.Itoa(take))
	q.Set("skip", strconv.Itoa(skip))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return requestListPayload{}, err
	}
	if c.apiKey != "" {
		req.Header.Set("X-Api-Key", c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return requestListPayload{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return requestListPayload{}, fmt.Errorf("seerr request list status %d", resp.StatusCode)
	}

	var payload requestListPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return requestListPayload{}, err
	}
	return payload, nil
}

func (c *Client) CreateRequest(ctx context.Context, mediaType string, tmdbID int64) error {
	body := map[string]any{
		"mediaType": mediaType,
		"mediaId":   tmdbID,
	}
	if mediaType == "tv" {
		body["seasons"] = "all"
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/request", strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("X-Api-Key", c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("seerr create request status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) CreateTVSeasonRequest(ctx context.Context, tmdbID int64, seasons []int) error {
	body := map[string]any{
		"mediaType": "tv",
		"mediaId":   tmdbID,
		"seasons":   seasons,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/request", strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("X-Api-Key", c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("seerr create season request status %d", resp.StatusCode)
	}
	return nil
}

// NotifyAvailable marks a media item as available in Seerr/Overseerr.
// It first looks up the Seerr-internal media ID by TMDB ID, then POSTs to
// the available endpoint. A 404 means the item isn't tracked in Seerr — not an error.
func (c *Client) NotifyAvailable(ctx context.Context, tmdbID int64, mediaType string) error {
	if c.baseURL == "" || c.apiKey == "" {
		return nil
	}
	apiMediaType := "movie"
	if strings.EqualFold(mediaType, "tv") || strings.EqualFold(mediaType, "episode") {
		apiMediaType = "tv"
	}
	// Resolve TMDB ID to Seerr internal media ID.
	infoURL := fmt.Sprintf("%s/api/v1/%s/%d", c.baseURL, apiMediaType, tmdbID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, infoURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil // item not tracked in Seerr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("seerr media lookup status %d", resp.StatusCode)
	}
	var info struct {
		MediaInfo *struct {
			ID int64 `json:"id"`
		} `json:"mediaInfo"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return err
	}
	if info.MediaInfo == nil || info.MediaInfo.ID == 0 {
		return nil // no mediaInfo yet — Seerr doesn't track it
	}
	// Mark as available.
	availURL := fmt.Sprintf("%s/api/v1/media/%d/available", c.baseURL, info.MediaInfo.ID)
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, availURL, strings.NewReader("{}"))
	if err != nil {
		return err
	}
	postReq.Header.Set("Content-Type", "application/json")
	postReq.Header.Set("X-Api-Key", c.apiKey)
	postResp, err := c.httpClient.Do(postReq)
	if err != nil {
		return err
	}
	defer postResp.Body.Close()
	if postResp.StatusCode < 200 || postResp.StatusCode >= 300 {
		return fmt.Errorf("seerr notify available status %d", postResp.StatusCode)
	}
	return nil
}

func parseYear(value string) int {
	if len(value) < 4 {
		return 0
	}
	year, err := strconv.Atoi(value[:4])
	if err != nil {
		return 0
	}
	return year
}

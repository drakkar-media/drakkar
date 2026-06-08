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

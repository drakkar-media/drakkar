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

	"github.com/hjongedijk/drakkar/internal/config"
)

type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client

	mu        sync.Mutex
	token     string
	tokenTime time.Time
}

type SeriesDetails struct {
	Name   string
	Year   int
	IMDbID string
}

func NewClient(cfg config.MetadataConfig) *Client {
	return &Client{
		apiKey:  strings.TrimSpace(cfg.TVDB.APIKey),
		baseURL: "https://api4.thetvdb.com/v4",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) Enabled() bool {
	return c != nil && strings.TrimSpace(c.apiKey) != ""
}

func (c *Client) SeriesDetails(ctx context.Context, tvdbID int64) (SeriesDetails, error) {
	req, err := c.newRequest(ctx, http.MethodGet, c.baseURL+"/series/"+strconv.FormatInt(tvdbID, 10)+"/extended", nil)
	if err != nil {
		return SeriesDetails{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return SeriesDetails{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SeriesDetails{}, fmt.Errorf("tvdb series status %d", resp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return SeriesDetails{}, err
	}
	data, _ := payload["data"].(map[string]any)
	out := SeriesDetails{
		Name: firstString(data, "name", "seriesName"),
		Year: firstInt(data, "year"),
	}
	if out.Year == 0 {
		out.Year = parseYear(firstString(data, "firstAired", "first_air_time", "first_air_date"))
	}
	out.IMDbID = extractIMDbID(data)
	return out, nil
}

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

func (c *Client) tokenValue(ctx context.Context) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Since(c.tokenTime) < 29*24*time.Hour {
		return c.token
	}
	token, err := c.login(ctx)
	if err != nil {
		return ""
	}
	c.token = token
	c.tokenTime = time.Now().UTC()
	return c.token
}

func (c *Client) login(ctx context.Context) (string, error) {
	payload := map[string]string{"apikey": c.apiKey}
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

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

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

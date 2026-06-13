// Package jellyfin provides a minimal Jellyfin client for triggering
// library refreshes after Drakkar publishes new media.
package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client calls the Jellyfin HTTP API.
type Client struct {
	serverURL  string
	apiKey     string
	httpClient *http.Client
}

// TestResult is returned from a connection test.
type TestResult struct {
	OK         bool   `json:"ok"`
	ServerName string `json:"serverName"`
	Version    string `json:"version"`
	Error      string `json:"error,omitempty"`
}

func NewClient(serverURL, apiKey string) *Client {
	return &Client{
		serverURL:  strings.TrimRight(serverURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) Enabled() bool {
	return c != nil && strings.TrimSpace(c.serverURL) != "" && strings.TrimSpace(c.apiKey) != ""
}

// Test verifies connectivity and returns server info.
func (c *Client) Test(ctx context.Context) TestResult {
	if !c.Enabled() {
		return TestResult{Error: "jellyfin not configured"}
	}
	type systemInfo struct {
		ServerName string `json:"ServerName"`
		Version    string `json:"Version"`
	}
	var info systemInfo
	if err := c.get(ctx, "/System/Info", &info); err != nil {
		return TestResult{Error: err.Error()}
	}
	return TestResult{
		OK:         true,
		ServerName: info.ServerName,
		Version:    info.Version,
	}
}

// RefreshLibraries triggers a full library scan.
func (c *Client) RefreshLibraries(ctx context.Context) error {
	if !c.Enabled() {
		return nil
	}
	return c.post(ctx, "/Library/Refresh")
}

func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.serverURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-MediaBrowser-Token", c.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("jellyfin HTTP %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

func (c *Client) post(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-MediaBrowser-Token", c.apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("jellyfin HTTP %d", resp.StatusCode)
	}
	return nil
}

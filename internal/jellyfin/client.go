// Package jellyfin provides a minimal Jellyfin client for triggering
// library refreshes after Drakkar publishes new media.
package jellyfin

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/drakkar-media/drakkar/internal/mediaserver"
)

type jellyfinEndpoint struct {
	serverURL string
	apiKey    string
}

// Client calls the Jellyfin HTTP API.
type Client struct {
	endpoint   atomic.Pointer[jellyfinEndpoint]
	httpClient *http.Client
}

// SetConfig updates the server URL/API key live, e.g. after a settings save.
func (c *Client) SetConfig(serverURL, apiKey string) {
	c.endpoint.Store(&jellyfinEndpoint{serverURL: strings.TrimRight(serverURL, "/"), apiKey: apiKey})
}

func (c *Client) getEndpoint() jellyfinEndpoint {
	if e := c.endpoint.Load(); e != nil {
		return *e
	}
	return jellyfinEndpoint{}
}

// TestResult is returned from a connection test.
type TestResult struct {
	OK         bool   `json:"ok"`
	ServerName string `json:"serverName"`
	Version    string `json:"version"`
	Error      string `json:"error,omitempty"`
}

// NewClient creates a Client for the Jellyfin server at serverURL,
// authenticated with apiKey. Either value may be empty; use SetConfig later
// once real settings are available, and check Enabled before relying on the
// client being usable.
func NewClient(serverURL, apiKey string) *Client {
	c := &Client{httpClient: &http.Client{Timeout: 15 * time.Second}}
	c.SetConfig(serverURL, apiKey)
	return c
}

// Enabled reports whether the client has both a server URL and API key
// configured. Callers should skip Jellyfin integration entirely when false
// rather than issuing a request that is certain to fail.
func (c *Client) Enabled() bool {
	e := c.getEndpoint()
	return c != nil && strings.TrimSpace(e.serverURL) != "" && strings.TrimSpace(e.apiKey) != ""
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
	e := c.getEndpoint()
	return mediaserver.Get(ctx, c.httpClient, e.serverURL, path, "X-MediaBrowser-Token", e.apiKey, "jellyfin", out)
}

func (c *Client) post(ctx context.Context, path string) error {
	e := c.getEndpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.serverURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-MediaBrowser-Token", e.apiKey)
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

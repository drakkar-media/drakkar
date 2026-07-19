package rclone

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client calls the rclone Remote Control (RC) API.
// It is used to refresh rclone's VFS directory cache after new content is
// published so Plex sees new files immediately — matching nzbdav's
// RcloneClient.RefreshVfsPaths() behaviour.
type Client struct {
	rcAddr     string
	httpClient *http.Client
}

func NewClient(rcAddr string) *Client {
	return &Client{
		rcAddr: strings.TrimRight(rcAddr, "/"),
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// RefreshMountPath refreshes the VFS directory cache for path, given as an
// absolute path under the FUSE mountpoint (mountRoot, e.g. config.Runtime's
// FuseMountPath). rclone's own vfs/refresh dir parameter is relative to the
// remote's root, not the OS mountpoint -- passing the mountpoint-prefixed
// path directly makes every call target a directory that doesn't exist from
// rclone's side (confirmed live: RC replies "file does not exist" for a dir
// param still carrying the /mnt/... mount prefix), so the refresh silently
// never invalidated anything.
func (c *Client) RefreshMountPath(ctx context.Context, mountRoot, absPath string) error {
	rel := strings.TrimPrefix(absPath, mountRoot)
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}
	return c.RefreshPath(ctx, rel)
}

// RefreshPath posts vfs/refresh for a single path, expressed relative to the
// rclone remote's root (e.g. "/content/releases/123"), not the OS mountpoint.
// Errors are non-fatal (rclone dir-cache-time handles staleness when RC is
// unavailable).
func (c *Client) RefreshPath(ctx context.Context, path string) error {
	if c == nil || c.rcAddr == "" {
		return nil
	}
	endpoint := c.rcAddr + "/vfs/refresh"
	form := url.Values{}
	form.Set("dir", path)
	form.Set("recursive", "false")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("vfs/refresh: status %d", resp.StatusCode)
	}
	return nil
}

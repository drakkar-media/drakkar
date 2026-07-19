package rclone

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
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

// ErrNotUnderMountRoot is returned by RefreshMountPath when absPath isn't
// genuinely a descendant of mountRoot.
var ErrNotUnderMountRoot = errors.New("rclone: path is not under mount root")

// RefreshMountPath refreshes the VFS directory cache for path, given as an
// absolute path under the FUSE mountpoint (mountRoot, e.g. config.Runtime's
// FuseMountPath). rclone's own vfs/refresh dir parameter is relative to the
// remote's root, not the OS mountpoint -- passing the mountpoint-prefixed
// path directly makes every call target a directory that doesn't exist from
// rclone's side (confirmed live: RC replies "file does not exist" for a dir
// param still carrying the /mnt/... mount prefix), so the refresh silently
// never invalidated anything.
func (c *Client) RefreshMountPath(ctx context.Context, mountRoot, absPath string) error {
	rel, ok := relativeMountPath(mountRoot, absPath)
	if !ok {
		return fmt.Errorf("%w: mountRoot=%q absPath=%q", ErrNotUnderMountRoot, mountRoot, absPath)
	}
	return c.RefreshPath(ctx, rel)
}

// relativeMountPath computes absPath's location relative to mountRoot, for
// passing to rclone's vfs/refresh (which wants a path relative to the
// remote's root, not the OS mountpoint). Unlike a bare strings.TrimPrefix,
// this only succeeds when absPath is genuinely a descendant of mountRoot at
// a real path-component boundary -- found live in an audit that the naive
// version had three failure modes, all silent (the caller always discards
// this function's error today, via `_ = ...`, so these previously just did
// nothing useful instead of erroring):
//  1. absPath == mountRoot exactly would strip to "", becoming "/" -- a
//     refresh of the rclone remote's ENTIRE root, worse than the "releases"
//     parent-directory refresh that saturated the webdav server in v0.2.44/45
//     (that was scoped to one subtree; "/" covers all of them).
//  2. absPath sharing only a string prefix with mountRoot but not a real path
//     boundary (mountRoot=/mnt/drakkar/vfs, absPath=/mnt/drakkar/vfs2/...)
//     would strip the common substring, producing a corrupted path like
//     "/2/...".
//  3. absPath not prefixed by mountRoot at all (a caller/config mismatch)
//     would leave the full mount-prefixed absolute path unchanged -- exactly
//     the "wrong path, always failed silently" bug fixed in a1b31d7.
//
// Currently unreachable in production because every real call site derives
// absPath from the identical mountRoot variable via filepath.Join with
// additional segments -- this closes the gap for any future caller or
// config change that breaks that convention.
func relativeMountPath(mountRoot, absPath string) (string, bool) {
	mountRoot = filepath.Clean(mountRoot)
	absPath = filepath.Clean(absPath)
	if mountRoot == "" || mountRoot == "." {
		return "", false
	}
	if absPath == mountRoot {
		// A real path, but refreshing the whole remote root is not a safe
		// substitute for a scoped subtree refresh -- treat as invalid so the
		// caller (or dir-cache-time) handles staleness instead.
		return "", false
	}
	prefix := mountRoot
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if !strings.HasPrefix(absPath, prefix) {
		return "", false
	}
	rel := strings.TrimPrefix(absPath, mountRoot)
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}
	return rel, true
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

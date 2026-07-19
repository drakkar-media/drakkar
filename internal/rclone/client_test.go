package rclone

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestRelativeMountPathNormalCase(t *testing.T) {
	rel, ok := relativeMountPath("/mnt/drakkar/vfs", "/mnt/drakkar/vfs/content/releases/501915")
	if !ok {
		t.Fatal("expected ok=true for a genuine descendant path")
	}
	if rel != "/content/releases/501915" {
		t.Fatalf("expected /content/releases/501915, got %q", rel)
	}
}

// TestRelativeMountPathRejectsExactMountRoot guards the "worse than the
// releases parent refresh" case found in the 2026-07-19 audit: absPath equal
// to mountRoot must not silently resolve to "/" (a refresh of the ENTIRE
// rclone remote root).
func TestRelativeMountPathRejectsExactMountRoot(t *testing.T) {
	_, ok := relativeMountPath("/mnt/drakkar/vfs", "/mnt/drakkar/vfs")
	if ok {
		t.Fatal("expected ok=false when absPath equals mountRoot exactly -- refreshing the whole remote root is not safe")
	}
}

// TestRelativeMountPathRejectsStringPrefixCollision guards the case where
// mountRoot is a string prefix of absPath but not a real path-component
// boundary (e.g. "/mnt/drakkar/vfs" vs "/mnt/drakkar/vfs2/..."), which a bare
// strings.TrimPrefix would silently corrupt into a garbage path.
func TestRelativeMountPathRejectsStringPrefixCollision(t *testing.T) {
	_, ok := relativeMountPath("/mnt/drakkar/vfs", "/mnt/drakkar/vfs2/content/releases/5")
	if ok {
		t.Fatal("expected ok=false for a string-prefix collision that isn't a real path boundary")
	}
}

// TestRelativeMountPathRejectsUnrelatedPath guards the exact bug fixed in
// a1b31d7: absPath not prefixed by mountRoot at all must not silently pass
// the full mount-prefixed path straight through to rclone.
func TestRelativeMountPathRejectsUnrelatedPath(t *testing.T) {
	_, ok := relativeMountPath("/mnt/drakkar/vfs", "/some/other/root/content/releases/5")
	if ok {
		t.Fatal("expected ok=false for a path with no relation to mountRoot")
	}
}

func TestRelativeMountPathHandlesTrailingSlashOnMountRoot(t *testing.T) {
	rel, ok := relativeMountPath("/mnt/drakkar/vfs/", "/mnt/drakkar/vfs/content/releases/5")
	if !ok {
		t.Fatal("expected ok=true regardless of a trailing slash on mountRoot")
	}
	if rel != "/content/releases/5" {
		t.Fatalf("expected /content/releases/5, got %q", rel)
	}
}

func TestRefreshMountPathReturnsErrorForInvalidPath(t *testing.T) {
	c := NewClient("http://unused:5572")
	err := c.RefreshMountPath(context.Background(), "/mnt/drakkar/vfs", "/unrelated/path")
	if err == nil {
		t.Fatal("expected an error for a path outside mountRoot")
	}
}

func TestRefreshMountPathSendsCorrectDirParam(t *testing.T) {
	var gotDir string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		gotDir = r.Form.Get("dir")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.RefreshMountPath(context.Background(), "/mnt/drakkar/vfs", "/mnt/drakkar/vfs/content/releases/501915"); err != nil {
		t.Fatal(err)
	}
	if gotDir != "/content/releases/501915" {
		t.Fatalf("expected RC to receive dir=/content/releases/501915, got %q", gotDir)
	}
}

func TestRefreshPathNilClientIsNoOp(t *testing.T) {
	var c *Client
	if err := c.RefreshPath(context.Background(), "/content/releases/1"); err != nil {
		t.Fatalf("expected nil-client RefreshPath to be a no-op, got %v", err)
	}
}

func TestRefreshMountPathOnNilClientIsSafe(t *testing.T) {
	var c *Client
	// relativeMountPath succeeds (this is a valid path), so RefreshMountPath
	// proceeds to call c.RefreshPath on a nil receiver -- must not panic.
	if err := c.RefreshMountPath(context.Background(), "/mnt/drakkar/vfs", "/mnt/drakkar/vfs/content/releases/1"); err != nil {
		t.Fatalf("expected nil-client RefreshMountPath to be a safe no-op, got %v", err)
	}
}

func TestRefreshPathEmptyRCAddrIsNoOp(t *testing.T) {
	c := NewClient("")
	if err := c.RefreshPath(context.Background(), "/content/releases/1"); err != nil {
		t.Fatalf("expected empty rcAddr to be a no-op, got %v", err)
	}
}

func TestRefreshPathPropagatesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.RefreshPath(context.Background(), "/content/releases/1"); err == nil {
		t.Fatal("expected an error for a non-2xx RC response")
	}
}

func TestRefreshPathSendsNonRecursive(t *testing.T) {
	var gotRecursive string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := url.ParseQuery("")
		_ = body
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		gotRecursive = r.Form.Get("recursive")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.RefreshPath(context.Background(), "/content/releases/1"); err != nil {
		t.Fatal(err)
	}
	if gotRecursive != "false" {
		t.Fatalf("expected recursive=false, got %q", gotRecursive)
	}
}

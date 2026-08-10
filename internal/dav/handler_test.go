package dav

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/stream"
)

// countingProvider wraps a fixed set of symlink publications and counts how
// many times ListSymlinkPublications is actually called, so tests can assert
// the tree cache is doing its job instead of hitting the "database" on every
// request.
type countingProvider struct {
	pubs      []database.SymlinkPublication
	entries   []database.ContentMountEntry
	vf        stream.VirtualMediaFile
	calls     int64
	openCalls int64
}

func (p *countingProvider) ListSymlinkPublications(ctx context.Context) ([]database.SymlinkPublication, error) {
	atomic.AddInt64(&p.calls, 1)
	return p.pubs, nil
}
func (p *countingProvider) ListContentMountEntries(ctx context.Context) ([]database.ContentMountEntry, error) {
	return p.entries, nil
}
func (p *countingProvider) ListContentMountEntriesForRelease(ctx context.Context, selectedReleaseID int64) ([]database.ContentMountEntry, error) {
	var out []database.ContentMountEntry
	for _, e := range p.entries {
		if e.SelectedReleaseID == selectedReleaseID {
			out = append(out, e)
		}
	}
	return out, nil
}
func (p *countingProvider) OpenVirtualMediaFile(ctx context.Context, virtualFileID int64) (stream.VirtualMediaFile, error) {
	atomic.AddInt64(&p.openCalls, 1)
	return p.vf, nil
}

// endlessVirtualFile is a huge (but never actually backed by real data)
// VirtualMediaFile: ReadAt always fills the destination with zero bytes and
// succeeds, so a handler serving it can be made to write far more than any
// test's client will ever read.
type endlessVirtualFile struct{ size int64 }

func (f endlessVirtualFile) Name() string { return "endless.mkv" }
func (f endlessVirtualFile) Size() int64  { return f.size }
func (f endlessVirtualFile) ReadAt(_ context.Context, dst []byte, offset int64) (int, error) {
	if offset >= f.size {
		return 0, io.EOF
	}
	return len(dst), nil
}

// TestGetTreeCachesWithinTTL guards a real production incident: statCompleted
// and openCompleted each rebuilt the entire /completed-symlinks tree (a full
// ListSymlinkPublications query + buildTree over every row) on every single
// Stat/OpenFile call -- exactly the subtree a Plex library scan walks node by
// node. Confirmed via a live pprof-informed audit this was the dominant
// remaining cost after fixing the analogous file_cache.go bug. getTree must
// serve repeated calls within the TTL window from the cached tree, not
// re-query every time.
func TestGetTreeCachesWithinTTL(t *testing.T) {
	provider := &countingProvider{
		pubs: []database.SymlinkPublication{
			{LibraryPath: "/movies/Some Movie (2021)/Some Movie (2021).mkv", TargetPath: "/vfs/content/releases/1/movie.mkv"},
		},
	}
	fs := &contentFS{db: provider, movieLibPath: "/movies", cacheTTL: time.Hour}

	for i := 0; i < 20; i++ {
		if _, err := fs.getTree(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls := atomic.LoadInt64(&provider.calls); calls != 1 {
		t.Fatalf("expected exactly 1 ListSymlinkPublications call across 20 getTree calls within TTL, got %d", calls)
	}
}

// TestGetTreeRefreshesAfterTTL guards the other half of the same fix: the
// cache must not be permanent -- once the TTL elapses, getTree must pick up
// fresh data rather than serving a stale tree forever.
func TestGetTreeRefreshesAfterTTL(t *testing.T) {
	provider := &countingProvider{}
	fs := &contentFS{db: provider, movieLibPath: "/movies", cacheTTL: 10 * time.Millisecond}

	if _, err := fs.getTree(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.getTree(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := atomic.LoadInt64(&provider.calls); calls != 1 {
		t.Fatalf("expected 1 call before TTL expiry, got %d", calls)
	}

	time.Sleep(20 * time.Millisecond)
	if _, err := fs.getTree(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := atomic.LoadInt64(&provider.calls); calls != 2 {
		t.Fatalf("expected a second call after TTL expiry, got %d", calls)
	}
}

// TestStatAndOpenCompletedResolveCachedTree confirms the cache doesn't break
// correctness: statCompleted/openCompleted must still resolve real entries
// (and reject missing ones) via the cached tree.
func TestStatAndOpenCompletedResolveCachedTree(t *testing.T) {
	provider := &countingProvider{
		pubs: []database.SymlinkPublication{
			{LibraryPath: "/movies/Some Movie (2021)/Some Movie (2021).mkv", TargetPath: "/vfs/content/releases/1/movie.mkv"},
		},
	}
	fs := &contentFS{db: provider, movieLibPath: "/movies", cacheTTL: time.Hour}

	info, err := fs.statCompleted(context.Background(), "movies/Some Movie (2021)/Some Movie (2021).mkv.rclonelink")
	if err != nil {
		t.Fatalf("expected existing symlink to resolve, got err=%v", err)
	}
	if info.IsDir() {
		t.Fatal("expected a file, got a directory")
	}

	if _, err := fs.statCompleted(context.Background(), "movies/Nonexistent.mkv.rclonelink"); err == nil {
		t.Fatal("expected an error for a nonexistent path")
	}

	file, err := fs.openCompleted(context.Background(), "movies/Some Movie (2021)/Some Movie (2021).mkv.rclonelink")
	if err != nil {
		t.Fatalf("expected existing symlink to open, got err=%v", err)
	}
	_ = file.Close()

	if calls := atomic.LoadInt64(&provider.calls); calls != 1 {
		t.Fatalf("expected all 3 calls to share one cached tree build, got %d ListSymlinkPublications calls", calls)
	}
}

// TestPropfindDoesNotOpenVirtualMediaFile guards a real production incident
// (2026-08-10, confirmed live via CPU/heap pprof profiles against the
// production container showing 326% CPU / 9.9GiB heap and a matching NNTP
// traffic spike): golang.org/x/net/webdav's findContentType calls
// fs.OpenFile *before* it even checks mime.TypeByExtension, unless the
// returned os.FileInfo implements the library's ContentTyper escape hatch.
// Without it, every PROPFIND -- issued continuously by rclone's dir-cache
// refresh and by Plex library scans -- opened every single video file
// through the full OpenVirtualMediaFile path (segment-span loading plus a
// live NNTP boundary probe) just to answer a directory listing, storming the
// whole library's worth of NNTP/CPU/memory repeatedly and starving the
// connection pool real playback depends on. A PROPFIND must resolve content
// type from the extension alone and never call OpenVirtualMediaFile.
func TestPropfindDoesNotOpenVirtualMediaFile(t *testing.T) {
	provider := &countingProvider{
		entries: []database.ContentMountEntry{
			{SelectedReleaseID: 1, FileName: "movie.mkv", SizeBytes: 1024},
		},
	}
	h := Handler(provider, "/movies", "/tv")

	req := httptest.NewRequest("PROPFIND", "/content/releases/1/movie.mkv", nil)
	req.Header.Set("Depth", "0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 207 {
		t.Fatalf("expected 207 Multi-Status, got %d: %s", rec.Code, rec.Body.String())
	}
	if calls := atomic.LoadInt64(&provider.openCalls); calls != 0 {
		t.Fatalf("expected PROPFIND to never call OpenVirtualMediaFile, got %d calls", calls)
	}
}

// TestHandlerWriteDeadlineUnsticksAbandonedConnection guards a real
// production incident (2026-08-10): a seek opens a brand-new GET/Range
// request while the old one, still mid-copy from the reader into the TCP
// connection, lingers if the client abandons it without a clean TCP close.
// Go's net/http.Server sets no write deadline of its own, so that stale
// write blocks in the write() syscall for however long the OS's own TCP
// retransmission timeout is (several minutes on Linux by default) -- and
// for that whole time the stale session never releases back to the pool,
// so a new seek's fetch queues behind it. This must resolve in roughly
// writeIdleTimeout, not minutes: a real client that stops reading (without
// closing) must cause the server's Write to fail well within a few
// multiples of the deadline.
func TestHandlerWriteDeadlineUnsticksAbandonedConnection(t *testing.T) {
	orig := writeIdleTimeout
	writeIdleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { writeIdleTimeout = orig })

	provider := &countingProvider{
		entries: []database.ContentMountEntry{
			{SelectedReleaseID: 1, FileName: "movie.mkv", SizeBytes: 64 << 20, VirtualFileID: 1},
		},
		vf: endlessVirtualFile{size: 64 << 20},
	}
	h := Handler(provider, "/movies", "/tv")

	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		close(done)
	}))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("GET /content/releases/1/movie.mkv HTTP/1.1\r\nHost: test\r\n\r\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}

	// Read just enough to get past the response headers, then stop reading
	// entirely without closing the connection -- simulating a client that
	// abandoned the stream without a clean TCP close.
	buf := make([]byte, 4096)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("read headers: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return within a reasonable multiple of writeIdleTimeout -- it blocked on the abandoned connection")
	}
}

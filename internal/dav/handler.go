package dav

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/stream"
	"golang.org/x/net/webdav"
	"golang.org/x/sync/singleflight"
)

func init() {
	// Register media MIME types missing from Go's standard library so that
	// http.ServeContent identifies them by extension instead of sniffing the
	// first 512 bytes (which would trigger a needless NNTP segment fetch).
	for ext, typ := range map[string]string{
		".mkv":  "video/x-matroska",
		".mp4":  "video/mp4",
		".avi":  "video/x-msvideo",
		".mov":  "video/quicktime",
		".wmv":  "video/x-ms-wmv",
		".m4v":  "video/x-m4v",
		".ts":   "video/mp2t",
		".m2ts": "video/mp2t",
		".flac": "audio/flac",
		".mp3":  "audio/mpeg",
		".aac":  "audio/aac",
		".ac3":  "audio/ac3",
		".dts":  "audio/vnd.dts",
	} {
		_ = mime.AddExtensionType(ext, typ)
	}
}

var contentModTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// ContentProvider is the database interface needed by the WebDAV handler.
type ContentProvider interface {
	ListContentMountEntries(ctx context.Context) ([]database.ContentMountEntry, error)
	ListContentMountEntriesForRelease(ctx context.Context, selectedReleaseID int64) ([]database.ContentMountEntry, error)
	OpenVirtualMediaFile(ctx context.Context, virtualFileID int64) (stream.VirtualMediaFile, error)
	ListSymlinkPublications(ctx context.Context) ([]database.SymlinkPublication, error)
}

// Handler returns an HTTP handler serving virtual files over WebDAV.
//
// Directory structure mirrors the FUSE mount so existing library symlinks
// continue to work when rclone replaces FUSE at the same mount point:
//
//	/content/releases/{selectedReleaseID}/{filename}   — streaming content
//	/completed-symlinks/movies/{path}/{file}.rclonelink — rclone symlink files
//	/completed-symlinks/tv/{path}/{file}.rclonelink
func Handler(db ContentProvider, movieLibPath, tvLibPath string) http.Handler {
	h := &webdav.Handler{
		FileSystem: &contentFS{
			db:           db,
			movieLibPath: strings.TrimSuffix(movieLibPath, "/"),
			tvLibPath:    strings.TrimSuffix(tvLibPath, "/"),
		},
		LockSystem: webdav.NewMemLS(),
	}
	// Content-Encoding: identity tells rclone that Content-Length is accurate
	// and the stream is not encoded, enabling direct Range-request pass-through
	// without requiring vfs-cache-mode=full.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "identity")
		h.ServeHTTP(newDeadlineResponseWriter(w), r)
	})
}

// writeIdleTimeout bounds how long a single Write to the client can go
// without the client acknowledging any data before it's treated as dead.
// Confirmed live (2026-08-10): a seek opens a brand-new GET/Range request
// while the old one -- still mid io.Copy from StoredRarReader into the TCP
// connection -- lingers if the client abandons it without a clean TCP
// close (no FIN/RST). Go's net/http.Server sets no write deadline of its
// own, so that stale write blocks in the write() syscall for however long
// the OS's own TCP retransmission timeout is, which defaults to several
// minutes on Linux -- and for that whole time, the stale session's checked-
// out NNTP connections (interactive fetch plus any in-flight read-ahead)
// never return to the pool, so the new seek's fetch queues behind them.
// Refreshed on every successful Write, so a real, slow-but-alive transfer
// is never killed -- only a connection making zero progress for this long.
var writeIdleTimeout = 20 * time.Second

// deadlineResponseWriter wraps an http.ResponseWriter so every Write extends
// a rolling deadline on the underlying connection via http.ResponseController,
// converting an indefinitely-blocking write to a dead peer into a bounded
// error a handful of seconds later instead of minutes.
type deadlineResponseWriter struct {
	http.ResponseWriter
	rc *http.ResponseController
}

func newDeadlineResponseWriter(w http.ResponseWriter) *deadlineResponseWriter {
	return &deadlineResponseWriter{ResponseWriter: w, rc: http.NewResponseController(w)}
}

func (w *deadlineResponseWriter) Write(p []byte) (int, error) {
	// Best-effort: SetWriteDeadline can fail on response writers that don't
	// support it (e.g. in tests using httptest.ResponseRecorder), in which
	// case the write proceeds without a deadline rather than failing outright.
	_ = w.rc.SetWriteDeadline(time.Now().Add(writeIdleTimeout))
	return w.ResponseWriter.Write(p)
}

// Flush is not promoted automatically -- embedding the http.ResponseWriter
// interface only exposes that interface's own methods, not every optional
// interface (http.Flusher, http.Hijacker) the concrete value underneath it
// happens to implement. net/http's chunked-transfer path relies on Flush
// being reachable, so it's forwarded explicitly.
func (w *deadlineResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// contentFS is a read-only webdav.FileSystem backed by the database rather
// than a real directory tree. It serves two independent namespaces under a
// single root: /content (streamed release files, resolved per-request) and
// /completed-symlinks (a mirror of the published library's .rclonelink
// files, rebuilt from ListSymlinkPublications and cached -- see getTree).
type contentFS struct {
	db           ContentProvider
	movieLibPath string
	tvLibPath    string

	treeMu     sync.Mutex
	cachedTree *treeNode
	cachedAt   time.Time
	treeGroup  singleflight.Group
	// cacheTTL overrides defaultCompletedTreeCacheTTL when non-zero; only set
	// directly in tests so they don't need real multi-second sleeps.
	cacheTTL time.Duration

	contentMu       sync.Mutex
	cachedContent   *contentMountIndex
	contentCachedAt time.Time
	contentGroup    singleflight.Group
	// contentCacheTTL overrides defaultCompletedTreeCacheTTL when non-zero;
	// only set directly in tests so they don't need real multi-second sleeps.
	contentCacheTTL time.Duration
}

// contentMountIndex is a point-in-time snapshot of every virtual file across
// every release, indexed by release ID for O(1) per-release lookups without
// a separate database round trip.
type contentMountIndex struct {
	byRelease map[int64][]database.ContentMountEntry
}

// defaultCompletedTreeCacheTTL bounds how stale the /completed-symlinks tree
// can be. getTree used to rebuild the entire tree (a full, unfiltered
// ListSymlinkPublications query plus buildTree over every row) on every
// single Stat/OpenFile call under /completed-symlinks -- exactly the subtree
// a Plex library scan walks. Confirmed live via a pprof-informed code audit
// (same investigation that found the file_cache.go Trim bug) that this is
// the same "recompute everything from scratch on every request" defect,
// except hit even more often (once per file/directory node touched during a
// scan, further doubled by golang.org/x/net/webdav's walkFS calling Stat
// again for every entry Readdir already returned FileInfo for) -- making a
// full scan effectively O(N^2) over the ~11,100-item library. A short TTL is
// safe here: rclone's own dir-cache-time (20s, see docker-compose.yml) already
// assumes and tolerates staleness at the layer above this one, so a newly
// published symlink simply becoming visible a few seconds later is no
// different from what the mount already does.
const defaultCompletedTreeCacheTTL = 10 * time.Second

// getTree returns the current /completed-symlinks tree, rebuilding it from
// the database at most once per cache TTL rather than on every call.
//
// The rebuild itself runs behind treeGroup (singleflight), not just the
// cache swap: without it, every caller that arrived after the TTL expired --
// e.g. the whole burst of Stat/Readdir calls a Plex library scan or rclone
// dir-cache refresh issues back to back -- ran its own full
// ListSymlinkPublications query and buildTree pass concurrently, all
// rebuilding the exact same tree. singleflight collapses that burst into one
// real rebuild; every other concurrent caller just waits for it and shares
// its result.
func (f *contentFS) getTree(ctx context.Context) (*treeNode, error) {
	ttl := f.cacheTTL
	if ttl <= 0 {
		ttl = defaultCompletedTreeCacheTTL
	}
	f.treeMu.Lock()
	if f.cachedTree != nil && time.Since(f.cachedAt) < ttl {
		tree := f.cachedTree
		f.treeMu.Unlock()
		return tree, nil
	}
	f.treeMu.Unlock()

	v, err, _ := f.treeGroup.Do("tree", func() (any, error) {
		pubs, err := f.db.ListSymlinkPublications(ctx)
		if err != nil {
			return nil, err
		}
		tree := f.buildTree(pubs)

		f.treeMu.Lock()
		f.cachedTree = tree
		f.cachedAt = time.Now()
		f.treeMu.Unlock()
		return tree, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*treeNode), nil
}

// getContentIndex returns the current /content/releases index, rebuilding it
// from the database at most once per cache TTL rather than on every call --
// mirrors getTree's exact caching shape for the same reason.
//
// Confirmed live 2026-08-20: unlike /completed-symlinks, /content/releases
// had no caching at all. Opening "/content/releases/" ran an unfiltered
// ListContentMountEntries scan over every virtual_files row in the library
// just to enumerate distinct release IDs; opening "/content/releases/{id}/"
// or "/content/releases/{id}/{filename}" (via either Stat or OpenFile, each
// independently) issued a fresh ListContentMountEntriesForRelease query with
// no caching at all -- an N+1 pattern with no TTL to absorb repeats. A
// single recursive PROPFIND walk of the releases tree -- exactly what
// rclone's dir-cache refresh and a media-server library scan both do
// continuously -- cost one full-table query plus one query per release
// every single time. Indexing the one full-table fetch by release ID here
// serves every per-release lookup from memory too, eliminating the N+1
// entirely rather than just caching its existing shape.
// getContentIndex's rebuild runs behind contentGroup (singleflight) for the
// same reason as getTree's treeGroup: without it, every caller past cache
// expiry issued its own concurrent ListContentMountEntries scan and index
// build instead of sharing one.
func (f *contentFS) getContentIndex(ctx context.Context) (*contentMountIndex, error) {
	ttl := f.contentCacheTTL
	if ttl <= 0 {
		ttl = defaultCompletedTreeCacheTTL
	}
	f.contentMu.Lock()
	if f.cachedContent != nil && time.Since(f.contentCachedAt) < ttl {
		idx := f.cachedContent
		f.contentMu.Unlock()
		return idx, nil
	}
	f.contentMu.Unlock()

	v, err, _ := f.contentGroup.Do("content", func() (any, error) {
		entries, err := f.db.ListContentMountEntries(ctx)
		if err != nil {
			return nil, err
		}
		idx := &contentMountIndex{byRelease: make(map[int64][]database.ContentMountEntry)}
		for _, e := range entries {
			idx.byRelease[e.SelectedReleaseID] = append(idx.byRelease[e.SelectedReleaseID], e)
		}

		f.contentMu.Lock()
		f.cachedContent = idx
		f.contentCachedAt = time.Now()
		f.contentMu.Unlock()
		return idx, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*contentMountIndex), nil
}

// parsedPath is the result of decomposing a WebDAV path.
type parsedPath struct {
	section string // "content", "completed-symlinks", or ""
	rest    string // everything after the section
}

func splitPath(name string) parsedPath {
	name = strings.Trim(name, "/")
	if name == "" {
		return parsedPath{}
	}
	slash := strings.IndexByte(name, '/')
	if slash < 0 {
		return parsedPath{section: name}
	}
	return parsedPath{
		section: name[:slash],
		rest:    name[slash+1:],
	}
}

// --- os.FileInfo helpers ---

type dirInfo struct{ name string }

func (d *dirInfo) Name() string       { return d.name }
func (d *dirInfo) Size() int64        { return 0 }
func (d *dirInfo) Mode() os.FileMode  { return os.ModeDir | 0o555 }
func (d *dirInfo) ModTime() time.Time { return contentModTime }
func (d *dirInfo) IsDir() bool        { return true }
func (d *dirInfo) Sys() any           { return nil }

type fileInfo struct {
	name string
	size int64
}

func (fi *fileInfo) Name() string       { return fi.name }
func (fi *fileInfo) Size() int64        { return fi.size }
func (fi *fileInfo) Mode() os.FileMode  { return 0o444 }
func (fi *fileInfo) ModTime() time.Time { return contentModTime }
func (fi *fileInfo) IsDir() bool        { return false }
func (fi *fileInfo) Sys() any           { return nil }

// ContentType implements golang.org/x/net/webdav's ContentTyper, the only
// way to stop webdav.findContentType from calling fs.OpenFile before it even
// checks mime.TypeByExtension -- confirmed live via pprof (2026-08-10): every
// PROPFIND (issued constantly by rclone's dir cache refresh and Plex library
// scans) was opening every single video file through the full
// OpenVirtualMediaFile path (segment-span loading plus a live NNTP boundary
// probe) just to answer a directory listing, for the whole ~11,100-item
// library repeatedly. That explained simultaneous CPU/memory/network blowup
// and, by starving the NNTP connection pool and decode semaphore that real
// playback also uses, the concurrent stream corruption and failed seeks.
func (fi *fileInfo) ContentType(context.Context) (string, error) {
	if ctype := mime.TypeByExtension(filepath.Ext(fi.name)); ctype != "" {
		return ctype, nil
	}
	return "application/octet-stream", nil
}

// --- webdav.File implementations ---

// dirFile is a read-only directory.
type dirFile struct {
	fi       os.FileInfo
	children []os.FileInfo
	pos      int
}

func (d *dirFile) Close() error                       { return nil }
func (d *dirFile) Read(_ []byte) (int, error)         { return 0, io.EOF }
func (d *dirFile) Write(_ []byte) (int, error)        { return 0, os.ErrPermission }
func (d *dirFile) Seek(_ int64, _ int) (int64, error) { return 0, os.ErrInvalid }
func (d *dirFile) Stat() (os.FileInfo, error)         { return d.fi, nil }

func (d *dirFile) Readdir(count int) ([]os.FileInfo, error) {
	if count <= 0 {
		result := d.children[d.pos:]
		d.pos = len(d.children)
		return result, nil
	}
	if d.pos >= len(d.children) {
		return nil, io.EOF
	}
	end := d.pos + count
	if end > len(d.children) {
		end = len(d.children)
	}
	result := d.children[d.pos:end]
	d.pos = end
	return result, nil
}

// bytesFile serves a static byte slice (used for .rclonelink files).
type bytesFile struct {
	fi  os.FileInfo
	buf []byte
	pos int64
}

func (f *bytesFile) Close() error                         { return nil }
func (f *bytesFile) Write(_ []byte) (int, error)          { return 0, os.ErrPermission }
func (f *bytesFile) Readdir(_ int) ([]os.FileInfo, error) { return nil, os.ErrInvalid }
func (f *bytesFile) Stat() (os.FileInfo, error)           { return f.fi, nil }

func (f *bytesFile) Seek(offset int64, whence int) (int64, error) {
	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = f.pos + offset
	case io.SeekEnd:
		newPos = int64(len(f.buf)) + offset
	default:
		return 0, os.ErrInvalid
	}
	if newPos < 0 {
		return 0, os.ErrInvalid
	}
	f.pos = newPos
	return newPos, nil
}

func (f *bytesFile) Read(p []byte) (int, error) {
	if f.pos >= int64(len(f.buf)) {
		return 0, io.EOF
	}
	n := copy(p, f.buf[f.pos:])
	f.pos += int64(n)
	return n, nil
}

// virtualFile streams a VirtualMediaFile over HTTP.
// It mirrors the FUSE handle session lifecycle so read-ahead works identically:
// StartSession on open, NotifyRead after each read, Seek on non-sequential
// access, StopSession on close.
type virtualFile struct {
	ctx           context.Context
	db            ContentProvider
	virtualFileID int64
	size          int64
	fi            os.FileInfo

	// vf is opened lazily, on the first real Read, not on OpenFile -- a
	// PROPFIND (issued continuously by rclone's dir-cache refresh and Plex
	// library scans) only ever calls Stat/Close on the file returned here,
	// never Read. Confirmed live via pprof (2026-08-10) that opening eagerly
	// meant every directory listing paid the full OpenVirtualMediaFile cost
	// (segment-span loading, a live NNTP boundary probe) and even started a
	// read-ahead session with an immediate NotifyRead(0), storming the whole
	// library's NNTP/CPU/memory on every listing and starving the connection
	// pool real playback depends on.
	vf      stream.VirtualMediaFile
	openErr error

	// readCtx/cancel are a private cancellation scope derived from ctx, not
	// ctx itself -- so a seek's new session can proactively tear down THIS
	// session's foreground Read loop rather than waiting for it to notice
	// the client stopped wanting it. Confirmed live (2026-08-10, Venom: Let
	// There Be Carnage): stopping only the stale session's read-ahead (the
	// original fix) left the OLD connection's actual serving loop running
	// for as long as its client (rclone) kept draining it -- measured up to
	// ~163s of real data transfer nobody needed anymore, continuing to hold
	// NNTP connection budget the new seek's fetch was waiting on the whole
	// time. RegisterMeta's stale-session cleanup now cancels this too.
	readCtx context.Context
	cancel  context.CancelFunc

	pos         int64
	sessionFile stream.SessionVirtualMediaFile // nil for inline/byte files, or before the first Read
	sessionID   string
	hasRead     bool  // true after the first actual data read
	lastEnd     int64 // position after the last read, for seek detection
}

// ensureOpen performs the real OpenVirtualMediaFile call and starts the
// read-ahead session, memoized so it only happens once and only when a Read
// actually needs the data.
func (f *virtualFile) ensureOpen() error {
	if f.vf != nil || f.openErr != nil {
		return f.openErr
	}
	vf, err := f.db.OpenVirtualMediaFile(f.ctx, f.virtualFileID)
	if err != nil {
		f.openErr = err
		return err
	}
	f.vf = vf
	f.readCtx, f.cancel = context.WithCancel(f.ctx)
	if sf, ok := vf.(stream.SessionVirtualMediaFile); ok {
		f.sessionFile = sf
		f.sessionID = fmt.Sprintf("dav-%d-%d", f.virtualFileID, time.Now().UnixNano())
		sf.StartSession(f.sessionID)
		sf.RegisterMeta(f.sessionID, stream.SessionMeta{
			VirtualFileID: f.virtualFileID,
			FileName:      f.fi.Name(),
			FileSizeBytes: f.size,
			OpenedAt:      time.Now().UTC(),
		})
	}
	return nil
}

func (f *virtualFile) Close() error {
	if f.sessionFile != nil {
		f.sessionFile.StopSession(f.sessionID)
	}
	if f.cancel != nil {
		f.cancel()
	}
	return nil
}

func (f *virtualFile) Write(_ []byte) (int, error)          { return 0, os.ErrPermission }
func (f *virtualFile) Stat() (os.FileInfo, error)           { return f.fi, nil }
func (f *virtualFile) Readdir(_ int) ([]os.FileInfo, error) { return nil, os.ErrInvalid }

func (f *virtualFile) Seek(offset int64, whence int) (int64, error) {
	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = f.pos + offset
	case io.SeekEnd:
		newPos = f.size + offset
	default:
		return 0, os.ErrInvalid
	}
	if newPos < 0 {
		return 0, os.ErrInvalid
	}
	// Only cancel read-ahead on a genuine seek (after first real data read).
	// Pre-read Seeks for size detection (Seek(0,End) → Seek(0,Start)) are
	// ignored because hasRead is still false at that point.
	if f.sessionFile != nil && f.hasRead && newPos != f.lastEnd {
		f.sessionFile.Seek(f.sessionID, newPos)
	}
	f.pos = newPos
	return newPos, nil
}

func (f *virtualFile) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := f.ensureOpen(); err != nil {
		return 0, err
	}
	size := f.size
	if f.pos >= size {
		return 0, io.EOF
	}
	remaining := size - f.pos
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := f.vf.ReadAt(f.readCtx, p, f.pos)
	if err != nil && err != io.EOF {
		slog.Debug("dav read error", "name", f.fi.Name(), "pos", f.pos, "n", n, "err", err)
	}
	if n > 0 {
		f.pos += int64(n)
		f.hasRead = true
		f.lastEnd = f.pos
		if f.sessionFile != nil {
			f.sessionFile.NotifyRead(f.sessionID, f.pos)
		}
		if err == io.EOF {
			err = nil // don't mix data with EOF; next Read returns EOF
		}
	}
	return n, err
}

// --- Filesystem read/write stubs ---

func (f *contentFS) Mkdir(_ context.Context, _ string, _ os.FileMode) error {
	return os.ErrPermission
}
func (f *contentFS) RemoveAll(_ context.Context, _ string) error {
	return os.ErrPermission
}
func (f *contentFS) Rename(_ context.Context, _, _ string) error {
	return os.ErrPermission
}

// --- Stat ---

func (f *contentFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	pp := splitPath(name)
	switch pp.section {
	case "":
		return &dirInfo{name: "/"}, nil
	case "content":
		return f.statContent(ctx, pp.rest)
	case "completed-symlinks":
		return f.statCompleted(ctx, pp.rest)
	}
	return nil, os.ErrNotExist
}

func (f *contentFS) statContent(ctx context.Context, rest string) (os.FileInfo, error) {
	rest = strings.Trim(rest, "/")
	if rest == "" || rest == "releases" {
		return &dirInfo{name: "content"}, nil
	}
	if !strings.HasPrefix(rest, "releases/") {
		return nil, os.ErrNotExist
	}
	rest = strings.TrimPrefix(rest, "releases/")
	rest = strings.Trim(rest, "/")

	idx, err := f.getContentIndex(ctx)
	if err != nil {
		return nil, err
	}
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		// /content/releases/{id}
		rid, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			return nil, os.ErrNotExist
		}
		if len(idx.byRelease[rid]) == 0 {
			return nil, os.ErrNotExist
		}
		return &dirInfo{name: rest}, nil
	}
	// /content/releases/{id}/{filename}
	rid, err := strconv.ParseInt(rest[:slash], 10, 64)
	if err != nil {
		return nil, os.ErrNotExist
	}
	filename := rest[slash+1:]
	for _, e := range idx.byRelease[rid] {
		if e.FileName == filename {
			return &fileInfo{name: e.FileName, size: e.SizeBytes}, nil
		}
	}
	return nil, os.ErrNotExist
}

func (f *contentFS) statCompleted(ctx context.Context, rest string) (os.FileInfo, error) {
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return &dirInfo{name: "completed-symlinks"}, nil
	}
	tree, err := f.getTree(ctx)
	if err != nil {
		return nil, err
	}
	node := treeNodeAt(tree, rest)
	if node == nil {
		return nil, os.ErrNotExist
	}
	if node.isFile {
		return &fileInfo{name: filepath.Base(rest), size: int64(len(node.content))}, nil
	}
	return &dirInfo{name: filepath.Base(rest)}, nil
}

// --- OpenFile ---

func (f *contentFS) OpenFile(ctx context.Context, name string, _ int, _ os.FileMode) (webdav.File, error) {
	pp := splitPath(name)
	switch pp.section {
	case "":
		return f.openRoot(ctx)
	case "content":
		return f.openContent(ctx, pp.rest)
	case "completed-symlinks":
		return f.openCompleted(ctx, pp.rest)
	}
	return nil, os.ErrNotExist
}

func (f *contentFS) openRoot(_ context.Context) (webdav.File, error) {
	children := []os.FileInfo{
		&dirInfo{name: "content"},
		&dirInfo{name: "completed-symlinks"},
	}
	return &dirFile{fi: &dirInfo{name: "/"}, children: children}, nil
}

func (f *contentFS) openContent(ctx context.Context, rest string) (webdav.File, error) {
	rest = strings.Trim(rest, "/")
	if rest == "" {
		// /content/ → one child: releases/
		return &dirFile{
			fi:       &dirInfo{name: "content"},
			children: []os.FileInfo{&dirInfo{name: "releases"}},
		}, nil
	}
	idx, err := f.getContentIndex(ctx)
	if err != nil {
		return nil, err
	}
	if rest == "releases" {
		// /content/releases/ → list all release IDs
		var kids []os.FileInfo
		for rid := range idx.byRelease {
			kids = append(kids, &dirInfo{name: strconv.FormatInt(rid, 10)})
		}
		return &dirFile{fi: &dirInfo{name: "releases"}, children: kids}, nil
	}
	if !strings.HasPrefix(rest, "releases/") {
		return nil, os.ErrNotExist
	}
	rest = strings.TrimPrefix(rest, "releases/")
	rest = strings.Trim(rest, "/")

	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		// /content/releases/{id}/ → list files
		rid, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			return nil, os.ErrNotExist
		}
		var kids []os.FileInfo
		for _, e := range idx.byRelease[rid] {
			kids = append(kids, &fileInfo{name: e.FileName, size: e.SizeBytes})
		}
		if len(kids) == 0 {
			return nil, os.ErrNotExist
		}
		return &dirFile{
			fi:       &dirInfo{name: rest},
			children: kids,
		}, nil
	}
	// /content/releases/{id}/{filename}
	rid, err := strconv.ParseInt(rest[:slash], 10, 64)
	if err != nil {
		return nil, os.ErrNotExist
	}
	filename := rest[slash+1:]
	for _, e := range idx.byRelease[rid] {
		if e.FileName == filename {
			return &virtualFile{
				ctx:           ctx,
				db:            f.db,
				virtualFileID: e.VirtualFileID,
				size:          e.SizeBytes,
				fi:            &fileInfo{name: e.FileName, size: e.SizeBytes},
			}, nil
		}
	}
	return nil, os.ErrNotExist
}

func (f *contentFS) openCompleted(ctx context.Context, rest string) (webdav.File, error) {
	rest = strings.Trim(rest, "/")
	tree, err := f.getTree(ctx)
	if err != nil {
		return nil, err
	}

	if rest == "" {
		// /completed-symlinks/ → list top-level children
		return dirFileFromNode(&dirInfo{name: "completed-symlinks"}, tree), nil
	}
	node := treeNodeAt(tree, rest)
	if node == nil {
		return nil, os.ErrNotExist
	}
	name := filepath.Base(rest)
	if node.isFile {
		return &bytesFile{
			fi:  &fileInfo{name: name, size: int64(len(node.content))},
			buf: node.content,
		}, nil
	}
	return dirFileFromNode(&dirInfo{name: name}, node), nil
}

// --- Completed-symlinks tree ---

type treeNode struct {
	isFile   bool
	content  []byte
	children map[string]*treeNode
}

// buildTree converts the flat list of symlink publications into a nested
// treeNode structure mirroring their library-relative directory layout, with
// each publication represented as a single "<name>.rclonelink" leaf file
// whose content is the rclone symlink target path.
func (f *contentFS) buildTree(pubs []database.SymlinkPublication) *treeNode {
	root := &treeNode{children: make(map[string]*treeNode)}
	for _, pub := range pubs {
		relPath := f.relPath(pub.LibraryPath)
		if relPath == "" {
			continue
		}
		parts := strings.Split(filepath.ToSlash(relPath), "/")
		node := root
		for i, part := range parts {
			if i == len(parts)-1 {
				linkName := part + ".rclonelink"
				node.children[linkName] = &treeNode{
					isFile:  true,
					content: []byte(pub.TargetPath),
				}
			} else {
				if _, ok := node.children[part]; !ok {
					node.children[part] = &treeNode{children: make(map[string]*treeNode)}
				}
				node = node.children[part]
			}
		}
	}
	return root
}

// relPath maps an absolute library path to its path relative to the
// /completed-symlinks root, rewriting known movie/TV library roots to their
// "movies/" or "tv/" prefix and falling back to just the base name for any
// other path. Returns "" when libraryPath has no usable base name.
func (f *contentFS) relPath(libraryPath string) string {
	if f.movieLibPath != "" && strings.HasPrefix(libraryPath, f.movieLibPath+"/") {
		return "movies/" + strings.TrimPrefix(libraryPath, f.movieLibPath+"/")
	}
	if f.tvLibPath != "" && strings.HasPrefix(libraryPath, f.tvLibPath+"/") {
		return "tv/" + strings.TrimPrefix(libraryPath, f.tvLibPath+"/")
	}
	base := filepath.Base(libraryPath)
	if base == "" || base == "." {
		return ""
	}
	return base
}

func treeNodeAt(root *treeNode, path string) *treeNode {
	if path == "" {
		return root
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	node := root
	for _, part := range parts {
		if part == "" {
			continue
		}
		child, ok := node.children[part]
		if !ok {
			return nil
		}
		node = child
	}
	return node
}

func dirFileFromNode(fi os.FileInfo, node *treeNode) *dirFile {
	kids := make([]os.FileInfo, 0, len(node.children))
	for name, child := range node.children {
		if child.isFile {
			kids = append(kids, &fileInfo{name: name, size: int64(len(child.content))})
		} else {
			kids = append(kids, &dirInfo{name: name})
		}
	}
	return &dirFile{fi: fi, children: kids}
}

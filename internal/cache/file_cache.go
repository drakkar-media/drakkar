package cache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/drakkar-media/drakkar/internal/metrics"
)

// FileCache is a disk-backed, size-bounded cache. Eviction order and total
// size are tracked in an in-memory index (seeded once from disk on first
// use) rather than recomputed by re-listing the directory on every call --
// confirmed live in production that the previous full-rescan-per-Put design
// consumed 58% of the whole process's CPU (per a live pprof profile) once the
// cache directory reached its steady-state ~31,500 files at the 20GB limit,
// almost entirely in stat() syscalls, because every single cache write did a
// full os.ReadDir + per-entry stat + sort over the entire directory just to
// decide whether (and what) to evict. That CPU cost, multiplied by Plex
// library scans opening many files in quick succession via WebDAV, was
// enough to push the whole host's load average well past its core count.
type FileCache struct {
	root     string
	maxBytes int64

	seedOnce sync.Once
	seedErr  error

	mu     sync.Mutex
	order  *list.List // front = least recently used, back = most recently used
	byPath map[string]*list.Element
	total  int64
}

type fileCacheEntry struct {
	path string
	size int64
}

// DirStats summarizes the current size of a FileCache directory.
type DirStats struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// PruneResult reports the effect of a manual FileCache.Prune call.
type PruneResult struct {
	Root         string `json:"root"`
	FilesBefore  int    `json:"filesBefore"`
	FilesAfter   int    `json:"filesAfter"`
	BytesBefore  int64  `json:"bytesBefore"`
	BytesAfter   int64  `json:"bytesAfter"`
	DeletedFiles int    `json:"deletedFiles"`
	DeletedBytes int64  `json:"deletedBytes"`
	LimitBytes   int64  `json:"limitBytes"`
}

// NewFileCache creates a FileCache rooted at the given directory with a
// total size budget of maxBytes. The directory's existing contents (if any)
// are not indexed until the first Get/Put/Stats call, via ensureSeeded.
func NewFileCache(root string, maxBytes int64) *FileCache {
	return &FileCache{
		root:     root,
		maxBytes: maxBytes,
		order:    list.New(),
		byPath:   make(map[string]*list.Element),
	}
}

// ensureSeeded populates the in-memory index from the existing on-disk
// contents exactly once (e.g. after a process restart, when files from a
// prior run already exist but haven't been indexed yet). Ordered oldest
// (front) to newest (back) by mtime, matching the eviction order the old
// full-rescan Trim() used.
func (c *FileCache) ensureSeeded() error {
	c.seedOnce.Do(func() {
		entries, err := os.ReadDir(c.root)
		if err != nil {
			if !os.IsNotExist(err) {
				c.seedErr = err
			}
			return
		}
		type item struct {
			path    string
			size    int64
			modTime int64
		}
		items := make([]item, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue // file removed concurrently; skip rather than fail startup
			}
			items = append(items, item{
				path:    filepath.Join(c.root, entry.Name()),
				size:    info.Size(),
				modTime: info.ModTime().UnixNano(),
			})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].modTime < items[j].modTime })
		c.mu.Lock()
		for _, it := range items {
			el := c.order.PushBack(&fileCacheEntry{path: it.path, size: it.size})
			c.byPath[it.path] = el
			c.total += it.size
		}
		c.mu.Unlock()
	})
	return c.seedErr
}

// Get reads the cached value for key from disk, if present. A cache hit
// updates the file's mtime and moves it to the most-recently-used end of
// the eviction order. A missing file is reported as (nil, false, nil), not
// an error.
func (c *FileCache) Get(key string) ([]byte, bool, error) {
	if err := c.ensureSeeded(); err != nil {
		return nil, false, err
	}
	path := c.pathFor(key)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	now := unixNow()
	_ = os.Chtimes(path, now, now)
	c.mu.Lock()
	if el, ok := c.byPath[path]; ok {
		c.order.MoveToBack(el)
	}
	c.mu.Unlock()
	return data, true, nil
}

// Put writes value to disk under key via a write-to-temp-then-rename
// sequence (so a concurrent Get never observes a partially written file),
// updates the in-memory size index, and triggers Trim to enforce maxBytes.
func (c *FileCache) Put(key string, value []byte) error {
	if err := c.ensureSeeded(); err != nil {
		return err
	}
	if err := os.MkdirAll(c.root, 0o755); err != nil {
		return err
	}
	path := c.pathFor(key)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, value, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	size := int64(len(value))
	c.mu.Lock()
	if el, ok := c.byPath[path]; ok {
		entry := el.Value.(*fileCacheEntry)
		c.total += size - entry.size
		entry.size = size
		c.order.MoveToBack(el)
	} else {
		el := c.order.PushBack(&fileCacheEntry{path: path, size: size})
		c.byPath[path] = el
		c.total += size
	}
	c.mu.Unlock()
	return c.Trim()
}

// Remove deletes key's cached file, if present, and updates the in-memory
// size index to match. A missing file is not an error -- callers use this to
// forget a cache entry that turned out to hold wrong data, and the entry may
// already be gone (evicted, or never written).
func (c *FileCache) Remove(key string) error {
	if err := c.ensureSeeded(); err != nil {
		return err
	}
	path := c.pathFor(key)
	c.mu.Lock()
	if el, ok := c.byPath[path]; ok {
		entry := el.Value.(*fileCacheEntry)
		c.total -= entry.size
		c.order.Remove(el)
		delete(c.byPath, path)
	}
	c.mu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Stats returns the current file count and total size tracked by the cache.
func (c *FileCache) Stats() (DirStats, error) {
	if err := c.ensureSeeded(); err != nil {
		return DirStats{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return DirStats{Files: c.order.Len(), Bytes: c.total}, nil
}

// Trim evicts least-recently-used entries until the tracked total is back
// under maxBytes. Unlike the old implementation, this never re-reads the
// directory -- eviction candidates come straight from the in-memory ordered
// index, so the cost is proportional to the (typically 0 or 1) entries
// actually evicted, not to the total number of files in the cache.
func (c *FileCache) Trim() error {
	if c.maxBytes <= 0 {
		return nil
	}
	if err := c.ensureSeeded(); err != nil {
		return err
	}
	for {
		c.mu.Lock()
		if c.total <= c.maxBytes {
			c.mu.Unlock()
			return nil
		}
		front := c.order.Front()
		if front == nil {
			c.mu.Unlock()
			return nil
		}
		entry := front.Value.(*fileCacheEntry)
		c.order.Remove(front)
		delete(c.byPath, entry.path)
		c.total -= entry.size
		c.mu.Unlock()

		if err := os.Remove(entry.path); err == nil {
			metrics.M.CacheEvictions.Add(1)
		}
	}
}

// Prune forces an eviction pass (via Trim) and reports the before/after
// size and file count. Intended for manual/administrative invocation
// outside the normal Put-triggered eviction path.
func (c *FileCache) Prune() (PruneResult, error) {
	before, err := c.Stats()
	if err != nil {
		return PruneResult{}, err
	}
	if err := c.Trim(); err != nil {
		return PruneResult{}, err
	}
	after, err := c.Stats()
	if err != nil {
		return PruneResult{}, err
	}
	return PruneResult{
		Root:         c.root,
		FilesBefore:  before.Files,
		FilesAfter:   after.Files,
		BytesBefore:  before.Bytes,
		BytesAfter:   after.Bytes,
		DeletedFiles: max(0, before.Files-after.Files),
		DeletedBytes: max(0, before.Bytes-after.Bytes),
		LimitBytes:   c.maxBytes,
	}, nil
}

func (c *FileCache) pathFor(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.root, hex.EncodeToString(sum[:])+".bin")
}

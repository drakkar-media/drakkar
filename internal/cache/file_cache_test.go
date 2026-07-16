package cache

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func TestFileCachePutGetTrim(t *testing.T) {
	cache := NewFileCache(t.TempDir(), 5)
	if err := cache.Put("a", []byte("123")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := cache.Put("b", []byte("45")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := cache.Put("c", []byte("67")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cache.Get("a"); err != nil || ok {
		t.Fatalf("expected a evicted, ok=%v err=%v", ok, err)
	}
	if data, ok, err := cache.Get("c"); err != nil || !ok || string(data) != "67" {
		t.Fatalf("unexpected c data=%q ok=%v err=%v", string(data), ok, err)
	}
}

// TestFileCacheSeedsFromExistingDiskContentsAfterRestart guards the new
// in-memory-index design (replacing the old per-call full-directory-rescan
// Trim, which cost 58% of the process's CPU in production once the cache hit
// its ~31,500-file steady state): a FileCache constructed against a directory
// that already has files on disk (e.g. after a process restart) must seed
// its index from those files and still enforce the byte limit correctly,
// evicting the oldest (by mtime) first.
func TestFileCacheSeedsFromExistingDiskContentsAfterRestart(t *testing.T) {
	dir := t.TempDir()
	first := NewFileCache(dir, 100)
	if err := first.Put("old", []byte("aaaaa")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := first.Put("new", []byte("bbbbb")); err != nil {
		t.Fatal(err)
	}

	// Simulate a restart: a fresh FileCache instance with no in-memory state,
	// pointed at the same directory.
	second := NewFileCache(dir, 8)
	if err := second.Trim(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := second.Get("old"); err != nil || ok {
		t.Fatalf("expected 'old' evicted after seeding from disk, ok=%v err=%v", ok, err)
	}
	if data, ok, err := second.Get("new"); err != nil || !ok || string(data) != "bbbbb" {
		t.Fatalf("expected 'new' to survive, data=%q ok=%v err=%v", string(data), ok, err)
	}
}

// TestFileCacheGetBumpsRecency guards the LRU-recency behavior of the
// in-memory index: a Get() on an entry must move it to most-recently-used,
// so it survives eviction over an entry that was merely Put earlier.
func TestFileCacheGetBumpsRecency(t *testing.T) {
	cache := NewFileCache(t.TempDir(), 5)
	if err := cache.Put("a", []byte("123")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := cache.Put("b", []byte("45")); err != nil {
		t.Fatal(err)
	}
	// Touch "a" so it becomes more-recently-used than "b".
	if _, ok, err := cache.Get("a"); err != nil || !ok {
		t.Fatalf("expected a present, ok=%v err=%v", ok, err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := cache.Put("c", []byte("67")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cache.Get("b"); err != nil || ok {
		t.Fatalf("expected b evicted (least recently used), ok=%v err=%v", ok, err)
	}
	if _, ok, err := cache.Get("a"); err != nil || !ok {
		t.Fatalf("expected a to survive (recently touched), ok=%v err=%v", ok, err)
	}
}

// TestFileCacheConcurrentPutGetStaysUnderBudget hammers Put/Get from many
// goroutines simultaneously (the real usage pattern -- many concurrent NNTP
// fetches sharing one block cache) to guard against races in the in-memory
// index introduced by the rewrite, and confirms the tracked total still
// converges to at-or-under maxBytes once concurrent writers settle.
func TestFileCacheConcurrentPutGetStaysUnderBudget(t *testing.T) {
	cache := NewFileCache(t.TempDir(), 2000)
	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				key := fmt.Sprintf("g%d-k%d", g, i%5)
				if err := cache.Put(key, make([]byte, 100)); err != nil {
					t.Error(err)
					return
				}
				_, _, _ = cache.Get(key)
			}
		}(g)
	}
	wg.Wait()
	stats, err := cache.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Bytes > 2000 {
		t.Fatalf("expected cache to stay under budget, got %d bytes across %d files", stats.Bytes, stats.Files)
	}
}

func TestFileCachePathStable(t *testing.T) {
	cache := NewFileCache(t.TempDir(), 10)
	path1 := cache.pathFor("msg")
	path2 := cache.pathFor("msg")
	if path1 != path2 {
		t.Fatal("path unstable")
	}
	if _, err := os.Stat(path1); !os.IsNotExist(err) {
		t.Fatal("cache file should not exist until write")
	}
}

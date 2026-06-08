package cache

import (
	"os"
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

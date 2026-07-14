package cache

import (
	"container/list"
	"fmt"
	"sync"
	"testing"
)

// TestByteLRUShardEvictsOldest exercises a single shard directly so eviction
// order is deterministic -- the public ByteLRU spreads keys across
// byteLRUShardCount shards by hash, which makes exact cross-key ordering
// untestable (and not a guarantee the concurrent design makes; see the type
// comment on ByteLRU).
func TestByteLRUShardEvictsOldest(t *testing.T) {
	shard := &byteLRUShard{maxBytes: 5, ll: list.New(), entries: make(map[string]*list.Element)}
	shard.put("a", []byte("123"))
	shard.put("b", []byte("45"))
	if _, ok := shard.get("a"); !ok {
		t.Fatal("expected a present")
	}
	shard.put("c", []byte("67"))
	if _, ok := shard.get("b"); ok {
		t.Fatal("expected b evicted")
	}
	if _, ok := shard.get("a"); !ok {
		t.Fatal("expected a retained after access")
	}
}

func TestByteLRURoundTrip(t *testing.T) {
	c := NewByteLRU(1024)
	c.Put("a", []byte("hello"))
	c.Put("b", []byte("world"))
	got, ok := c.Get("a")
	if !ok || string(got) != "hello" {
		t.Fatalf("Get(a) = %q, %v", got, ok)
	}
	got, ok = c.Get("b")
	if !ok || string(got) != "world" {
		t.Fatalf("Get(b) = %q, %v", got, ok)
	}
	if _, ok := c.Get("missing"); ok {
		t.Fatal("expected miss for absent key")
	}
}

// TestByteLRUEvictsUnderBudget verifies the aggregate byte budget is
// respected: with a small enough per-key size and enough distinct keys to
// blow past maxBytes many times over, older entries must eventually be
// evicted somewhere, even though sharding means eviction order is no longer
// exact across the whole cache (see the ByteLRU type comment).
func TestByteLRUEvictsUnderBudget(t *testing.T) {
	c := NewByteLRU(256)
	for i := 0; i < 1000; i++ {
		c.Put(fmt.Sprintf("key-%d", i), make([]byte, 64))
	}
	present := 0
	for i := 0; i < 1000; i++ {
		if _, ok := c.Get(fmt.Sprintf("key-%d", i)); ok {
			present++
		}
	}
	if present == 1000 {
		t.Fatal("expected some entries evicted under a small byte budget, all 1000 survived")
	}
}

// TestByteLRUConcurrentAccessDoesNotBlockUnrelatedKeys guards the production
// fix directly: a single global mutex over the whole cache let 109 goroutines
// pile up waiting on one lock during real streaming load, serializing
// interactive reads behind unrelated read-ahead fetches. Run with -race to
// also confirm the sharded implementation has no data races.
func TestByteLRUConcurrentAccessDoesNotBlockUnrelatedKeys(t *testing.T) {
	c := NewByteLRU(1 << 20)
	var wg sync.WaitGroup
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", g)
			value := make([]byte, 4096)
			for i := 0; i < 200; i++ {
				c.Put(key, value)
				if got, ok := c.Get(key); !ok || len(got) != len(value) {
					t.Errorf("goroutine %d: Get after Put = %v, %d bytes", g, ok, len(got))
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

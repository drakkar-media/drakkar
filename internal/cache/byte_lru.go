package cache

import (
	"container/list"
	"hash/maphash"
	"sync"
)

// byteLRUShardCount trades exact global LRU ordering for concurrency: a
// single mutex guarding one map+list was found live in production to be a
// severe bottleneck under real streaming load (109 goroutines queued on one
// mutex during a single 4K stream, each Get holding the lock for a ~700KB
// memcpy) -- confirmed via a pprof goroutine dump showing the interactive
// read that serves the player stuck behind that same pile of read-ahead
// fetches. Sharding by key hash spreads that contention across independent
// locks, same principle as .NET's ConcurrentDictionary (see reference
// nzbdav's ArticleCachingNntpClient, which never uses a single lock over its
// whole cache). Eviction becomes approximate (per-shard budget, not exact
// global least-recently-used order) -- an accepted tradeoff in every
// high-concurrency cache design for this reason.
const byteLRUShardCount = 32

type ByteLRU struct {
	seed   maphash.Seed
	shards [byteLRUShardCount]*byteLRUShard
}

type byteLRUShard struct {
	mu       sync.Mutex
	maxBytes int64
	size     int64
	ll       *list.List
	entries  map[string]*list.Element
}

type byteEntry struct {
	key   string
	value []byte
}

func NewByteLRU(maxBytes int64) *ByteLRU {
	perShard := maxBytes / byteLRUShardCount
	if maxBytes > 0 && perShard < 1 {
		perShard = 1
	}
	c := &ByteLRU{seed: maphash.MakeSeed()}
	for i := range c.shards {
		c.shards[i] = &byteLRUShard{
			maxBytes: perShard,
			ll:       list.New(),
			entries:  make(map[string]*list.Element),
		}
	}
	return c
}

func (c *ByteLRU) shardFor(key string) *byteLRUShard {
	return c.shards[maphash.String(c.seed, key)%byteLRUShardCount]
}

// Get returns the cache's own stored slice, not a copy: Put already clones
// its input on the way in (so the caller's buffer can't corrupt the cache),
// and every current caller only reads the result -- see the callers of
// CachedDecodedSource.cache/infoCache and DiskCachedDecodedSource.partInfo,
// none of which mutate the returned slice in place. Skipping a second,
// redundant ~700KB copy on every Get was the other half of the fix above:
// sharding alone still serializes each shard's callers behind a memcpy.
func (c *ByteLRU) Get(key string) ([]byte, bool) {
	return c.shardFor(key).get(key)
}

func (c *ByteLRU) Put(key string, value []byte) {
	c.shardFor(key).put(key, value)
}

func (s *byteLRUShard) get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ele, ok := s.entries[key]; ok {
		s.ll.MoveToFront(ele)
		return ele.Value.(*byteEntry).value, true
	}
	return nil, false
}

func (s *byteLRUShard) put(key string, value []byte) {
	clone := make([]byte, len(value))
	copy(clone, value)

	s.mu.Lock()
	defer s.mu.Unlock()
	if ele, ok := s.entries[key]; ok {
		item := ele.Value.(*byteEntry)
		s.size -= int64(len(item.value))
		item.value = clone
		s.size += int64(len(item.value))
		s.ll.MoveToFront(ele)
		s.trim()
		return
	}
	ele := s.ll.PushFront(&byteEntry{key: key, value: clone})
	s.entries[key] = ele
	s.size += int64(len(clone))
	s.trim()
}

func (s *byteLRUShard) trim() {
	for s.maxBytes > 0 && s.size > s.maxBytes && s.ll.Len() > 0 {
		ele := s.ll.Back()
		item := ele.Value.(*byteEntry)
		delete(s.entries, item.key)
		s.size -= int64(len(item.value))
		s.ll.Remove(ele)
	}
}

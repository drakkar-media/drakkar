package cache

import (
	"container/list"
	"sync"
)

type ByteLRU struct {
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
	return &ByteLRU{
		maxBytes: maxBytes,
		ll:       list.New(),
		entries:  make(map[string]*list.Element),
	}
}

func (c *ByteLRU) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ele, ok := c.entries[key]; ok {
		c.ll.MoveToFront(ele)
		item := ele.Value.(*byteEntry)
		out := make([]byte, len(item.value))
		copy(out, item.value)
		return out, true
	}
	return nil, false
}

func (c *ByteLRU) Put(key string, value []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	clone := make([]byte, len(value))
	copy(clone, value)

	if ele, ok := c.entries[key]; ok {
		item := ele.Value.(*byteEntry)
		c.size -= int64(len(item.value))
		item.value = clone
		c.size += int64(len(item.value))
		c.ll.MoveToFront(ele)
		c.trim()
		return
	}

	ele := c.ll.PushFront(&byteEntry{key: key, value: clone})
	c.entries[key] = ele
	c.size += int64(len(clone))
	c.trim()
}

func (c *ByteLRU) trim() {
	for c.maxBytes > 0 && c.size > c.maxBytes && c.ll.Len() > 0 {
		ele := c.ll.Back()
		item := ele.Value.(*byteEntry)
		delete(c.entries, item.key)
		c.size -= int64(len(item.value))
		c.ll.Remove(ele)
	}
}

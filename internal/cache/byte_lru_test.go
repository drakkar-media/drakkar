package cache

import "testing"

func TestByteLRUEvictsOldest(t *testing.T) {
	cache := NewByteLRU(5)
	cache.Put("a", []byte("123"))
	cache.Put("b", []byte("45"))
	if _, ok := cache.Get("a"); !ok {
		t.Fatal("expected a present")
	}
	cache.Put("c", []byte("67"))
	if _, ok := cache.Get("b"); ok {
		t.Fatal("expected b evicted")
	}
	if _, ok := cache.Get("a"); !ok {
		t.Fatal("expected a retained after access")
	}
}

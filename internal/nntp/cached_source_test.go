package nntp

import (
	"context"
	"sync/atomic"
	"testing"
)

type countingSource struct {
	body  []byte
	calls atomic.Int32
}

func (s *countingSource) DecodedBody(ctx context.Context, messageID string) ([]byte, error) {
	s.calls.Add(1)
	return s.body, nil
}

func TestCachedDecodedSourceCaches(t *testing.T) {
	src := &countingSource{body: []byte("hello")}
	cache := NewCachedDecodedSource(src, 1024)
	for range 2 {
		got, err := cache.DecodedBody(context.Background(), "<msg1>")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "hello" {
			t.Fatalf("got %q", string(got))
		}
	}
	if src.calls.Load() != 1 {
		t.Fatalf("expected 1 fetch, got %d", src.calls.Load())
	}
}

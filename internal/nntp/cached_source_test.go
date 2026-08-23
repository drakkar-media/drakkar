package nntp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/stream"
	"github.com/drakkar-media/drakkar/internal/yenc"
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

// TestCachedDecodedSourceInvalidateCachedForcesRefetch guards the same
// self-heal path as DiskCachedDecodedSource's equivalent test, one layer up:
// this in-memory hot cache must also forget a bad entry, or the very next
// fetch would just repopulate straight from it again even after a lower
// layer's cache was correctly cleared.
func TestCachedDecodedSourceInvalidateCachedForcesRefetch(t *testing.T) {
	src := &countingSource{body: []byte("hello")}
	cache := NewCachedDecodedSource(src, 1024)
	if _, err := cache.DecodedBody(context.Background(), "<msg1>"); err != nil {
		t.Fatal(err)
	}
	if src.calls.Load() != 1 {
		t.Fatalf("expected 1 fetch before invalidate, got %d", src.calls.Load())
	}
	cache.InvalidateCached("<msg1>")
	if _, err := cache.DecodedBody(context.Background(), "<msg1>"); err != nil {
		t.Fatal(err)
	}
	if src.calls.Load() != 2 {
		t.Fatalf("expected a fresh fetch after InvalidateCached, got %d total fetches", src.calls.Load())
	}
}

// invalidateTrackingSource records InvalidateCached calls so cascading
// behavior through a wrap chain can be asserted directly.
type invalidateTrackingSource struct {
	countingSource
	invalidated []string
}

func (s *invalidateTrackingSource) InvalidateCached(messageID string) {
	s.invalidated = append(s.invalidated, messageID)
}

// TestCachedDecodedSourceInvalidateCachedCascadesToWrappedSource guards the
// production wiring specifically: SegmentFetcher only ever holds a
// *CachedDecodedSource (the in-memory hot cache), which wraps
// *DiskCachedDecodedSource underneath. Clearing only the top layer and never
// telling the wrapped source would leave the disk-cache entry -- the one that
// actually persists across restarts -- still poisoned.
func TestCachedDecodedSourceInvalidateCachedCascadesToWrappedSource(t *testing.T) {
	src := &invalidateTrackingSource{countingSource: countingSource{body: []byte("hello")}}
	cache := NewCachedDecodedSource(src, 1024)
	cache.InvalidateCached("<msg1>")
	if len(src.invalidated) != 1 || src.invalidated[0] != "<msg1>" {
		t.Fatalf("expected wrapped source to receive InvalidateCached(<msg1>), got %v", src.invalidated)
	}
}

// countingInfoSource implements PriorityDecodedArticleInfoSource, matching
// the real production wiring (CachedDecodedSource wraps DiskCachedDecodedSource,
// which implements this interface). This is the shape that exposed the hot
// cache bypass: DecodedBodyInfoPriority used to type-assert this interface
// and call straight through, never touching CachedDecodedSource's own cache.
type countingInfoSource struct {
	body  []byte
	info  yenc.PartInfo
	delay time.Duration // simulates real NNTP fetch latency for concurrency tests
	calls atomic.Int32
}

func (s *countingInfoSource) DecodedBody(ctx context.Context, messageID string) ([]byte, error) {
	s.calls.Add(1)
	return s.body, nil
}

func (s *countingInfoSource) DecodedBodyInfoPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, yenc.PartInfo, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return s.body, s.info, nil
}

func (s *countingInfoSource) DecodedBodyInfo(ctx context.Context, messageID string) ([]byte, yenc.PartInfo, error) {
	return s.DecodedBodyInfoPriority(ctx, messageID, stream.PriorityInteractive)
}

func TestCachedDecodedSourceCachesInfoVariant(t *testing.T) {
	src := &countingInfoSource{
		body: []byte("hello"),
		info: yenc.PartInfo{TotalSize: 100, Begin: 1, End: 5},
	}
	cache := NewCachedDecodedSource(src, 1024)
	for range 3 {
		body, info, err := cache.DecodedBodyInfoPriority(context.Background(), "<msg1>", stream.PriorityInteractive)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "hello" {
			t.Fatalf("got body %q", string(body))
		}
		if info != src.info {
			t.Fatalf("got info %+v, want %+v", info, src.info)
		}
	}
	if src.calls.Load() != 1 {
		t.Fatalf("expected the underlying source to be hit exactly once (rest served from cache), got %d calls", src.calls.Load())
	}
}

func TestCachedDecodedSourceInfoVariantDeduplicatesConcurrentFetches(t *testing.T) {
	src := &countingInfoSource{
		body:  []byte("hello"),
		info:  yenc.PartInfo{TotalSize: 100, Begin: 1, End: 5},
		delay: 20 * time.Millisecond, // simulate real NNTP fetch latency
	}
	cache := NewCachedDecodedSource(src, 1024)

	const n = 8
	results := make(chan yenc.PartInfo, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, info, err := cache.DecodedBodyInfoPriority(context.Background(), "<msg-concurrent>", stream.PriorityInteractive)
			results <- info
			errs <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if info := <-results; info != src.info {
			t.Fatalf("follower got info %+v, want %+v", info, src.info)
		}
	}
	if src.calls.Load() != 1 {
		t.Fatalf("expected exactly 1 underlying fetch across %d concurrent callers, got %d", n, src.calls.Load())
	}
}

package nntp

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/yenc"
)

type countingBodySource struct {
	body  []byte
	calls atomic.Int32
}

func (s *countingBodySource) Body(ctx context.Context, messageID string) ([]byte, error) {
	s.calls.Add(1)
	return s.body, nil
}

func TestDiskCachedDecodedSourceCaches(t *testing.T) {
	src := &countingBodySource{body: []byte("=ybegin line=128 size=5 name=test\r\n" + encode([]byte("hello")) + "\r\n=yend size=5\r\n")}
	cache := NewDiskCachedDecodedSource(src, t.TempDir(), 1024)
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

// TestDiskCachedDecodedSourceBoundsDecodeConcurrency guards against
// unbounded concurrent yEnc decodes: the rapidyenc/CGO decoder isn't
// preemptible mid-call the way pure-Go code is, so many concurrent decodes
// (one per in-flight segment fetch, which can be 30+ for a single
// high-bitrate stream) can starve the Go scheduler of OS threads for
// unrelated work -- confirmed in production causing the app to fail its own
// health check under load. decodeArticle must serialize through decodeSem,
// sized to runtime.NumCPU(), independent of fetch parallelism.
func TestDiskCachedDecodedSourceBoundsDecodeConcurrency(t *testing.T) {
	src := &countingBodySource{body: []byte("=ybegin line=128 size=5 name=test\r\n" + encode([]byte("hello")) + "\r\n=yend size=5\r\n")}
	cache := NewDiskCachedDecodedSource(src, t.TempDir(), 1024)
	if cap(cache.decodeSem) != runtime.NumCPU() {
		t.Fatalf("expected decodeSem capacity %d, got %d", runtime.NumCPU(), cap(cache.decodeSem))
	}

	// Track observed concurrency directly: fill the semaphore, then confirm
	// one more acquire attempt genuinely blocks until a slot is released,
	// rather than just trusting the channel's capacity in isolation.
	held := 0
	for held < cap(cache.decodeSem) {
		cache.decodeSem <- struct{}{}
		held++
	}
	acquired := make(chan struct{})
	go func() {
		cache.decodeSem <- struct{}{}
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatal("acquired a slot while decodeSem was already full")
	case <-time.After(50 * time.Millisecond):
	}
	<-cache.decodeSem // release one slot
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("waiting acquire did not proceed after a slot was released")
	}
	for i := 0; i < held; i++ {
		<-cache.decodeSem
	}

	// Confirm decodeArticle itself still works correctly under concurrent load.
	var wg sync.WaitGroup
	for i := 0; i < runtime.NumCPU()*3; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			decoded, _, err := cache.decodeArticle(context.Background(), src.body)
			if err != nil {
				t.Errorf("decode %d: %v", n, err)
				return
			}
			if string(decoded) != "hello" {
				t.Errorf("decode %d: got %q", n, string(decoded))
			}
		}(i)
	}
	wg.Wait()
}

func TestDiskCachedDecodedSourceReturnsPartInfo(t *testing.T) {
	src := &countingBodySource{body: []byte("=ybegin line=128 size=10 name=test\r\n=ypart begin=11 end=15\r\n" + encode([]byte("hello")) + "\r\n=yend size=5\r\n")}
	cache := NewDiskCachedDecodedSource(src, t.TempDir(), 1024)
	got, info, err := cache.DecodedBodyInfo(context.Background(), "<msg1>")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q", string(got))
	}
	if !info.Valid() || info.Begin != 11 || info.End != 15 || info.DecodedStart() != 10 {
		t.Fatalf("unexpected part info %+v", info)
	}
}

func TestDiskCachedDecodedSourceBackfillsPartInfoFromRawWhenDiskHit(t *testing.T) {
	src := &countingBodySource{body: []byte("=ybegin line=128 size=10 name=test\r\n=ypart begin=11 end=15\r\n" + encode([]byte("hello")) + "\r\n=yend size=5\r\n")}
	cache := NewDiskCachedDecodedSource(src, t.TempDir(), 1024)
	if err := cache.cache.Put("<msg1>", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, info, err := cache.DecodedBodyInfo(context.Background(), "<msg1>")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q", string(got))
	}
	if info != (yenc.PartInfo{TotalSize: 10, Begin: 11, End: 15}) {
		t.Fatalf("unexpected part info %+v", info)
	}
	if src.calls.Load() != 1 {
		t.Fatalf("expected one raw backfill fetch, got %d", src.calls.Load())
	}
}

// countingStatBodySource additionally implements StatSource, matching the
// real production wiring (app.go stacks DiskCachedDecodedSource over a
// source that also implements Stat).
type countingStatBodySource struct {
	countingBodySource
	statCalls atomic.Int32
}

func (s *countingStatBodySource) Stat(ctx context.Context, messageID string) error {
	s.statCalls.Add(1)
	return nil
}

// TestDiskCachedDecodedSourceStatDoesNotRefetchOnDiskHit guards against a
// real gap found in the 2026-07-17 exhaustive audit: Stat() on a disk-cache
// hit whose in-memory partInfo companion was missing (e.g. after a process
// restart, since partInfo is in-memory only) called fillPartInfoFromRaw,
// which performs a full live article body fetch -- silently degrading the
// "quick/cheap NNTP STAT check" used as earlyChecker (a preflight gate
// before every selected-release fetch/retry) into a full download. The
// decoded body already being on disk is itself sufficient proof the article
// exists; Stat() must not fetch anything else to answer that.
func TestDiskCachedDecodedSourceStatDoesNotRefetchOnDiskHit(t *testing.T) {
	src := &countingStatBodySource{countingBodySource: countingBodySource{
		body: []byte("=ybegin line=128 size=10 name=test\r\n=ypart begin=11 end=15\r\n" + encode([]byte("hello")) + "\r\n=yend size=5\r\n"),
	}}
	cache := NewDiskCachedDecodedSource(src, t.TempDir(), 1024)
	if err := cache.cache.Put("<msg1>", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	// partInfo is intentionally left unpopulated -- simulating a process
	// that just restarted and lost its in-memory partInfo cache while the
	// on-disk decoded-body cache survived.

	if err := cache.Stat(context.Background(), "<msg1>"); err != nil {
		t.Fatal(err)
	}
	if got := src.calls.Load(); got != 0 {
		t.Fatalf("expected Stat on a disk-cache hit not to fetch the raw article body, got %d fetches", got)
	}
	if got := src.statCalls.Load(); got != 0 {
		t.Fatalf("expected Stat on a disk-cache hit not to issue a live NNTP STAT either, got %d", got)
	}
}

func encode(src []byte) string {
	out := make([]byte, 0, len(src)*2)
	for _, b := range src {
		enc := b + 42
		if enc == 0 || enc == '\n' || enc == '\r' || enc == '=' {
			out = append(out, '=')
			enc += 64
		}
		out = append(out, enc)
	}
	return string(out)
}

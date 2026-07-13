package nntp

import (
	"context"
	"sync/atomic"
	"testing"

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

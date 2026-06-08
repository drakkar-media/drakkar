package stream

import (
	"context"
	"testing"
)

type fetcherStub struct{}

func (fetcherStub) FetchRange(ctx context.Context, segment SegmentRange) ([]byte, error) {
	size := int(segment.RangeEnd - segment.RangeStart)
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte('A' + int(segment.SegmentID) - 1)
	}
	return buf, nil
}

type awareFetcherStub struct {
	data map[int64][]byte
	info map[int64]SegmentSpan
}

func (f awareFetcherStub) FetchRange(ctx context.Context, segment SegmentRange) ([]byte, error) {
	block, _, err := f.FetchRangeInfo(ctx, segment)
	return block, err
}

func (f awareFetcherStub) FetchRangeInfo(ctx context.Context, segment SegmentRange) ([]byte, SegmentSpan, error) {
	full := f.data[segment.SegmentID]
	actual := f.info[segment.SegmentID]
	start := int(segment.RangeStart - actual.Start)
	end := int(segment.RangeEnd - actual.Start)
	if start < 0 {
		start = 0
	}
	if end > len(full) {
		end = len(full)
	}
	out := make([]byte, end-start)
	copy(out, full[start:end])
	return out, actual, nil
}

func TestDirectNzbReaderReadAt(t *testing.T) {
	reader := NewDirectNzbReader("Dune.mkv", 300, []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 100},
		{SegmentID: 2, Start: 100, End: 200},
		{SegmentID: 3, Start: 200, End: 300},
	}, fetcherStub{}, nil)
	buf := make([]byte, 120)
	n, err := reader.ReadAt(context.Background(), buf, 90)
	if err != nil {
		t.Fatal(err)
	}
	if n != 120 {
		t.Fatalf("expected 120 bytes, got %d", n)
	}
	if buf[0] != 'A' || buf[15] != 'B' || buf[110] != 'C' {
		t.Fatalf("unexpected segment stitch %q", string(buf[:20]))
	}
}

func TestDirectNzbReaderRealignsEstimatedBoundaries(t *testing.T) {
	reader := NewDirectNzbReader("test.mkv", 22, []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 9},
		{SegmentID: 2, Start: 9, End: 18},
		{SegmentID: 3, Start: 18, End: 22},
	}, awareFetcherStub{
		data: map[int64][]byte{
			1: []byte("AAAAAAAAAA"),
			2: []byte("BBBBBBBBBB"),
			3: []byte("CC"),
		},
		info: map[int64]SegmentSpan{
			1: {SegmentID: 1, Start: 0, End: 10},
			2: {SegmentID: 2, Start: 10, End: 20},
			3: {SegmentID: 3, Start: 20, End: 22},
		},
	}, nil)

	buf := make([]byte, 12)
	n, err := reader.ReadAt(context.Background(), buf, 8)
	if err != nil {
		t.Fatal(err)
	}
	if n != 12 {
		t.Fatalf("expected 12 bytes, got %d", n)
	}
	if string(buf) != "AABBBBBBBBBB" {
		t.Fatalf("unexpected data %q", string(buf))
	}
}

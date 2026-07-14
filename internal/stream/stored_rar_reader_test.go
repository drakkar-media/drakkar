package stream

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestStoredRarReaderReadAt(t *testing.T) {
	reader := NewStoredRarReader("Movie.mkv", 200, []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 50},
		{SegmentID: 2, Start: 50, End: 150},
		{SegmentID: 3, Start: 150, End: 200},
	}, fetcherStub{}, nil)
	buf := make([]byte, 80)
	n, err := reader.ReadAt(context.Background(), buf, 40)
	if err != nil {
		t.Fatal(err)
	}
	if n != 80 {
		t.Fatalf("expected 80 bytes, got %d", n)
	}
	if buf[0] != 'A' || buf[15] != 'B' || buf[70] != 'B' {
		t.Fatalf("unexpected segment stitch %q", string(buf[:20]))
	}
}

func TestStoredRarReaderRejectsGap(t *testing.T) {
	reader := NewStoredRarReader("Movie.mkv", 120, []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 50},
		{SegmentID: 2, Start: 60, End: 120},
	}, fetcherStub{}, nil)
	_, err := reader.ReadAt(context.Background(), make([]byte, 16), 0)
	if !errors.Is(err, ErrStoredRarLayoutInvalid) {
		t.Fatalf("expected invalid layout, got %v", err)
	}
}

func TestStoredRarReaderRejectsWrongSizeCoverage(t *testing.T) {
	reader := NewStoredRarReader("Movie.mkv", 140, []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 50},
		{SegmentID: 2, Start: 50, End: 120},
	}, fetcherStub{}, nil)
	_, err := reader.ReadAt(context.Background(), make([]byte, 16), 0)
	if !errors.Is(err, ErrStoredRarLayoutInvalid) {
		t.Fatalf("expected invalid layout, got %v", err)
	}
}

// TestStoredRarReaderRealignsLastSegmentEstimate guards the production fix:
// a calibrated decoded_segment_size/last_decoded_size estimate for one NNTP
// segment that overshoots its true decoded size previously caused a hard
// "short fetch" error for any read touching that span -- confirmed live to
// hit almost exclusively the last segment of the last volume, since
// truncateSpans only reconciles the aggregate total against
// virtual_files.size_bytes, not each segment's own real boundaries. Real
// players probe near true EOF for trailing container metadata (MP4 moov,
// MKV cues), so this silently broke "video: none, audio: none" for every
// affected file even though the vast majority of the stream served fine.
//
// Segment 2 is declared as 9 bytes (VF span 10..19, matching
// virtual_files.size_bytes=19) but its real decoded content is only 8
// bytes -- exactly the shape of the confirmed real-world bug.
func TestStoredRarReaderRealignsLastSegmentEstimate(t *testing.T) {
	reader := NewStoredRarReader("Movie.mkv", 19, []SegmentSpan{
		{SegmentID: 1, MessageID: "<seg1>", Start: 0, End: 10, DecodedStart: 0, SegmentByteStart: 0},
		{SegmentID: 2, MessageID: "<seg2>", Start: 10, End: 19, DecodedStart: 10, SegmentByteStart: 0},
	}, awareFetcherStub{
		data: map[int64][]byte{
			1: []byte("AAAAAAAAAA"), // 10 bytes -- matches the estimate exactly
			2: []byte("BBBBBBBB"),   // 8 real bytes, short of the estimated 9-byte span
		},
		info: map[int64]SegmentSpan{
			1: {SegmentID: 1, MessageID: "<seg1>", Start: 0, End: 10},
			2: {SegmentID: 2, MessageID: "<seg2>", Start: 10, End: 18},
		},
	}, nil)

	buf := make([]byte, 9)
	n, err := reader.ReadAt(context.Background(), buf, 10)
	if err != io.EOF {
		t.Fatalf("expected io.EOF once the corrected size is exhausted, got %v", err)
	}
	if n != 8 {
		t.Fatalf("expected 8 real bytes delivered, got %d", n)
	}
	if string(buf[:8]) != "BBBBBBBB" {
		t.Fatalf("unexpected data %q", string(buf[:8]))
	}
	if got := reader.Size(); got != 18 {
		t.Fatalf("expected reader.Size() corrected to 18, got %d", got)
	}

	// A second read after the correction must see the corrected size and
	// boundaries directly, without needing to rediscover them.
	buf2 := make([]byte, 8)
	n2, err2 := reader.ReadAt(context.Background(), buf2, 10)
	if err2 != nil {
		t.Fatalf("unexpected error on re-read after correction: %v", err2)
	}
	if n2 != 8 || string(buf2) != "BBBBBBBB" {
		t.Fatalf("unexpected re-read result n=%d data=%q", n2, string(buf2))
	}
}

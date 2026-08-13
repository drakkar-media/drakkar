package stream

import "testing"

func TestResolveRangeAcrossSegments(t *testing.T) {
	spans := []SegmentSpan{
		{SegmentID: 1, MessageID: "a", Start: 0, End: 100},
		{SegmentID: 2, MessageID: "b", Start: 100, End: 200},
		{SegmentID: 3, MessageID: "c", Start: 200, End: 300},
	}
	got, err := ResolveRange(spans, 50, 175)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 spans, got %d", len(got))
	}
	if got[0].RangeStart != 50 || got[0].RangeEnd != 100 {
		t.Fatalf("unexpected first span %+v", got[0])
	}
	if got[2].RangeStart != 200 || got[2].RangeEnd != 225 {
		t.Fatalf("unexpected last span %+v", got[2])
	}
}

func TestResolveRangeOutsideFile(t *testing.T) {
	_, err := ResolveRange([]SegmentSpan{{SegmentID: 1, Start: 0, End: 100}}, 120, 10)
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestResolveRangeRejectsGapBetweenSpans guards a hazard found during the
// 2026-07-25 corruption audit: nothing previously checked that consecutive
// resolved spans are actually contiguous -- only that the LAST one reaches
// requestEnd. A gap between two middle spans (not expected today by
// construction, but not enforced here either) would have silently
// dropped/misaligned the bytes in the gap instead of surfacing an error --
// the same corruption class already fixed twice this session via other
// mechanisms (DirectNzbReader/StoredRarReader's stale-read races).
func TestResolveRangeRejectsGapBetweenSpans(t *testing.T) {
	spans := []SegmentSpan{
		{SegmentID: 1, MessageID: "a", Start: 0, End: 100},
		// Gap: 100-105 is not covered by any span.
		{SegmentID: 2, MessageID: "b", Start: 105, End: 200},
	}
	_, err := ResolveRange(spans, 50, 100) // requests [50,150), crossing the gap
	if err == nil {
		t.Fatal("expected an error for a request crossing a gap between spans, got none")
	}
}

func TestResolveRangeCapsCoveredPrefix(t *testing.T) {
	spans := []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 100},
		{SegmentID: 2, Start: 100, End: 200},
		{SegmentID: 3, Start: 200, End: 300},
	}
	ranges, err := resolveRange(spans, 50, 250, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 2 || ranges[0].RangeStart != 50 || ranges[1].RangeEnd != 200 {
		t.Fatalf("unexpected capped prefix: %+v", ranges)
	}
}

func TestResolveRangeRejectsOverflow(t *testing.T) {
	spans := []SegmentSpan{{SegmentID: 1, Start: 0, End: 100}}
	if _, err := ResolveRange(spans, 90, int64(^uint64(0)>>1)); err == nil {
		t.Fatal("expected overflowing range to be rejected")
	}
}

func BenchmarkResolveRangeCappedReadAheadWindow(b *testing.B) {
	const spanSize = int64(750 << 10)
	spans := make([]SegmentSpan, 2_800)
	for i := range spans {
		spans[i] = SegmentSpan{
			SegmentID: int64(i + 1),
			Start:     int64(i) * spanSize,
			End:       int64(i+1) * spanSize,
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		ranges, err := resolveRange(spans, 1_024, 512<<20, 40)
		if err != nil || len(ranges) != 40 {
			b.Fatalf("resolveRange = (%d ranges, %v), want (40, nil)", len(ranges), err)
		}
	}
}

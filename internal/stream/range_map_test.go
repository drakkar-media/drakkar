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

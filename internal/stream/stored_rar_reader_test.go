package stream

import (
	"context"
	"errors"
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

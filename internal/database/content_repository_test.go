package database

import (
	"context"
	"testing"

	"github.com/drakkar-media/drakkar/internal/stream"
)

type rangeInfoFetcherStub struct {
	actual stream.SegmentSpan
	err    error
}

func (f rangeInfoFetcherStub) FetchRange(ctx context.Context, segment stream.SegmentRange) ([]byte, error) {
	return nil, nil
}

func (f rangeInfoFetcherStub) FetchRangeInfo(ctx context.Context, segment stream.SegmentRange) ([]byte, stream.SegmentSpan, error) {
	return nil, f.actual, f.err
}

// TestVerifyLastSpanBoundaryShrinksOverestimatedSegment guards the fix for
// stored_rar files where a Content-Length computed before any read ever
// happens (the very first thing a fresh HTTP request or Plex's media
// analyzer does) reflected an over-estimated last-segment size -- confirmed
// live to make every player probe near true EOF (where MP4 moov / MKV cues
// live) fail, even though StoredRarReader's mid-read self-heal (a separate
// fix) worked fine for a read already in progress. This must correct the
// cached span BEFORE any reader is constructed, not just during one.
func TestVerifyLastSpanBoundaryShrinksOverestimatedSegment(t *testing.T) {
	db := &DB{SegmentFetcher: rangeInfoFetcherStub{
		actual: stream.SegmentSpan{SegmentID: 2, MessageID: "<seg2>", Start: 10, End: 18},
	}}
	spans := []stream.SegmentSpan{
		{SegmentID: 1, MessageID: "<seg1>", Start: 0, End: 10, DecodedStart: 0, SegmentByteStart: 0},
		{SegmentID: 2, MessageID: "<seg2>", Start: 10, End: 19, DecodedStart: 10, SegmentByteStart: 0},
	}
	corrected := db.verifyLastSpanBoundary(context.Background(), spans)
	if len(corrected) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(corrected))
	}
	if corrected[1].End != 18 {
		t.Fatalf("expected last span End corrected to 18, got %d", corrected[1].End)
	}
	if corrected[0] != spans[0] {
		t.Fatalf("expected first span untouched, got %+v", corrected[0])
	}
	// The original slice must not be mutated in place -- callers may still
	// hold a reference to the pre-correction spans elsewhere.
	if spans[1].End != 19 {
		t.Fatalf("expected original spans slice left untouched, got %+v", spans[1])
	}
}

// TestVerifyLastSpanBoundaryLeavesCorrectEstimateUnchanged guards against a
// false-positive correction: when the live measurement confirms the
// estimate was already right, nothing should change.
func TestVerifyLastSpanBoundaryLeavesCorrectEstimateUnchanged(t *testing.T) {
	db := &DB{SegmentFetcher: rangeInfoFetcherStub{
		actual: stream.SegmentSpan{SegmentID: 2, MessageID: "<seg2>", Start: 10, End: 19},
	}}
	spans := []stream.SegmentSpan{
		{SegmentID: 1, MessageID: "<seg1>", Start: 0, End: 10, DecodedStart: 0, SegmentByteStart: 0},
		{SegmentID: 2, MessageID: "<seg2>", Start: 10, End: 19, DecodedStart: 10, SegmentByteStart: 0},
	}
	result := db.verifyLastSpanBoundary(context.Background(), spans)
	if result[1].End != 19 {
		t.Fatalf("expected unchanged End=19, got %d", result[1].End)
	}
}

// TestVerifyLastSpanBoundaryFallsBackWithoutFetchCapability guards the case
// where the configured fetcher doesn't support FetchRangeInfo at all -- must
// return the input unchanged rather than panicking.
func TestVerifyLastSpanBoundaryFallsBackWithoutFetchCapability(t *testing.T) {
	db := &DB{SegmentFetcher: nil}
	spans := []stream.SegmentSpan{{SegmentID: 1, Start: 0, End: 10}}
	result := db.verifyLastSpanBoundary(context.Background(), spans)
	if result[0].End != 10 {
		t.Fatalf("expected unchanged spans, got %+v", result)
	}
}

func TestBuildStoredRarSpansAcrossVolumes(t *testing.T) {
	sources := map[string]storedRarNZBSource{
		"movie.part01.rar": {
			MessageIDs:         []string{"seg-a"},
			DecodedSegmentSize: 100,
			LastDecodedSize:    100,
		},
		"movie.part02.rar": {
			MessageIDs:         []string{"seg-b"},
			DecodedSegmentSize: 100,
			LastDecodedSize:    100,
		},
	}
	spans := buildStoredRarSpans(sources, []storedRarRangeSource{
		{VolumePath: "Movie.part01.rar", EntryOffset: 0, ArchiveOffset: 80, LengthBytes: 20},
		{VolumePath: "Movie.part02.rar", EntryOffset: 20, ArchiveOffset: 0, LengthBytes: 80},
	})
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %+v", spans)
	}
	if spans[0].Start != 0 || spans[0].End != 20 || spans[0].MessageID != "seg-a" {
		t.Fatalf("unexpected first span %+v", spans[0])
	}
	if spans[1].Start != 20 || spans[1].End != 100 || spans[1].MessageID != "seg-b" {
		t.Fatalf("unexpected second span %+v", spans[1])
	}
}

func TestReconstructStoredRarRangesFromLegacyFirstVolumeOnlyMapping(t *testing.T) {
	sources := map[string]storedRarNZBSource{
		"movie.part01.rar": {
			MessageIDs:         []string{"seg-a"},
			DecodedSegmentSize: 100,
			LastDecodedSize:    100,
		},
		"movie.r00": {
			MessageIDs:         []string{"seg-b"},
			DecodedSegmentSize: 100,
			LastDecodedSize:    100,
		},
		"movie.r01": {
			MessageIDs:         []string{"seg-c"},
			DecodedSegmentSize: 100,
			LastDecodedSize:    100,
		},
	}
	volumes := []storedRarVolumeMeta{
		{Path: "Movie.part01.rar", VolumeIndex: 0},
		{Path: "Movie.r00", VolumeIndex: 1},
		{Path: "Movie.r01", VolumeIndex: 2},
	}
	ranges := reconstructStoredRarRanges(sources, volumes, "Movie.part01.rar", 80, nil, 180)
	if len(ranges) != 3 {
		t.Fatalf("expected 3 ranges, got %+v", ranges)
	}
	if ranges[0].EntryOffset != 0 || ranges[0].ArchiveOffset != 80 || ranges[0].LengthBytes != 20 {
		t.Fatalf("unexpected first range %+v", ranges[0])
	}
	if ranges[1].EntryOffset != 20 || ranges[1].ArchiveOffset != 0 || ranges[1].LengthBytes != 100 {
		t.Fatalf("unexpected second range %+v", ranges[1])
	}
	if ranges[2].EntryOffset != 120 || ranges[2].ArchiveOffset != 0 || ranges[2].LengthBytes != 60 {
		t.Fatalf("unexpected third range %+v", ranges[2])
	}

	spans := buildStoredRarSpans(sources, ranges)
	if got := spanFileSize(spans); got != 180 {
		t.Fatalf("expected reconstructed spans to cover 180 bytes, got %d", got)
	}
}

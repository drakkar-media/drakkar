package database

import "testing"

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

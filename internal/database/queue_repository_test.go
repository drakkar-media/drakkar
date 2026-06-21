package database

import (
	"testing"
	"time"
)

func TestIsPlayableMedia(t *testing.T) {
	if !isPlayableMedia("Dune.mkv") {
		t.Fatal("expected mkv playable")
	}
	if !isPlayableMedia("clip.MP4") {
		t.Fatal("expected mp4 playable")
	}
	if isPlayableMedia("sample.rar") {
		t.Fatal("rar should not be marked playable")
	}
}


func TestIsHardRejectReason(t *testing.T) {
	if !isHardRejectReason("archive_video_not_found") {
		t.Fatal("expected archive reject to be hard")
	}
	if !isHardRejectReason(" Archive_Encrypted ") {
		t.Fatal("expected normalized archive reject to be hard")
	}
	if !isHardRejectReason("early preflight: Newshosting attempt 1: article missing") {
		t.Fatal("expected article missing to be hard")
	}
	if !isHardRejectReason("strict health: first segment abc unavailable: yenc crc mismatch") {
		t.Fatal("expected crc mismatch to be hard")
	}
	if !isHardRejectReason("publish failed: invalid media payload: file header does not match known media container") {
		t.Fatal("expected invalid media payload to be hard")
	}
	if isHardRejectReason("context deadline exceeded") {
		t.Fatal("network failure should not be a hard reject")
	}
}

func TestShouldPersistBlocklistReason(t *testing.T) {
	if !shouldPersistBlocklistReason("archive_encrypted") {
		t.Fatal("expected archive reject to persist in blocklist")
	}
	if !shouldPersistBlocklistReason(" manual_reject ") {
		t.Fatal("expected manual reject to persist in blocklist")
	}
	if !shouldPersistBlocklistReason("early preflight: Newshosting attempt 1: article missing") {
		t.Fatal("expected article missing to persist in blocklist")
	}
	if !shouldPersistBlocklistReason("strict health: first segment abc unavailable: yenc crc mismatch") {
		t.Fatal("expected crc mismatch to persist in blocklist")
	}
	if !shouldPersistBlocklistReason("publish failed: returned no readable bytes") {
		t.Fatal("expected invalid media payload to persist in blocklist")
	}
	if shouldPersistBlocklistReason("context deadline exceeded") {
		t.Fatal("transient failure should not persist in blocklist")
	}
}

func TestBlocklistReleaseSignatureKey(t *testing.T) {
	key := blocklistReleaseSignatureKey("RED.2010.1080p.BluRay.x264-GRP", "NZB Finder", 7340032000, time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC))
	if key == "" {
		t.Fatal("expected signature key")
	}
	if key != "release_signature:red 2010 1080p bluray x264 grp|nzb finder|7000|2026-06-06" {
		t.Fatalf("unexpected signature key %q", key)
	}
}

func TestBlockedReleaseReasonMatchesSignature(t *testing.T) {
	blocked := map[string]string{
		blocklistReleaseSignatureKey("RED.2010.1080p.BluRay.x264-GRP", "NZB Finder", 7340032000, time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)): "manual_reject",
	}
	reason, ok := blockedReleaseReason(blocked, SearchCandidateRecord{
		Title:       "RED.2010.1080p.BluRay.x264-GRP",
		IndexerName: "NZB Finder",
		SizeBytes:   7340032000,
		PostedAt:    time.Date(2026, 6, 6, 15, 0, 0, 0, time.UTC),
	})
	if !ok {
		t.Fatal("expected signature blocklist match")
	}
	if reason != "manual_reject" {
		t.Fatalf("unexpected reason %q", reason)
	}
}

func TestBlockedReleaseReasonMatchesFamilyAcrossIndexers(t *testing.T) {
	blocked := map[string]string{
		blocklistReleaseFamilyKey("RED.2010.1080p.BluRay.x264-GRP", 7340032000, time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)): "archive_headers_invalid",
	}
	reason, ok := blockedReleaseReason(blocked, SearchCandidateRecord{
		Title:       "RED.2010.1080p.BluRay.x264-GRP",
		IndexerName: "DrunkenSlug",
		SizeBytes:   7340032000,
		PostedAt:    time.Date(2026, 6, 6, 18, 0, 0, 0, time.UTC),
	})
	if !ok {
		t.Fatal("expected family blocklist match")
	}
	if reason != "archive_headers_invalid" {
		t.Fatalf("unexpected reason %q", reason)
	}
}

func TestBlocklistReleasePatternKey(t *testing.T) {
	key := blocklistReleasePatternKey("Yellowstone.S04E01.1080p.WEB-DL.x264-BADGRP", 1610612736, time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC))
	if key == "" {
		t.Fatal("expected pattern key")
	}
	if key != "release_pattern:yellowstone s04e01 1080p web|1536|2026-06-08" {
		t.Fatalf("unexpected pattern key %q", key)
	}
}

func TestBlockedReleaseReasonMatchesPatternAcrossGroups(t *testing.T) {
	blocked := map[string]string{
		blocklistReleasePatternKey("Yellowstone.S04E01.1080p.WEB-DL.x264-BADGRP", 1610612736, time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)): "invalid media payload",
	}
	reason, ok := blockedReleaseReason(blocked, SearchCandidateRecord{
		Title:       "Yellowstone.S04E01.1080p.WEBRip.x264-OTHERGRP",
		IndexerName: "DrunkenSlug",
		SizeBytes:   1610612736,
		PostedAt:    time.Date(2026, 6, 8, 18, 0, 0, 0, time.UTC),
	})
	if !ok {
		t.Fatal("expected pattern blocklist match")
	}
	if reason != "invalid media payload" {
		t.Fatalf("unexpected reason %q", reason)
	}
}

func TestSummarizeSearchFailureReason(t *testing.T) {
	if got := summarizeSearchFailureReason(nil); got != "no_releases" {
		t.Fatalf("expected no_releases, got %q", got)
	}
	if got := summarizeSearchFailureReason([]SearchCandidateRecord{
		{Rejected: true, RejectReason: "wrong_title"},
		{Rejected: true, RejectReason: "wrong_title"},
	}); got != "all_candidates_wrong_title" {
		t.Fatalf("unexpected single-reason summary %q", got)
	}
	if got := summarizeSearchFailureReason([]SearchCandidateRecord{
		{Rejected: true, RejectReason: "archive_headers_invalid"},
		{Rejected: true, RejectReason: "archive_video_not_found"},
	}); got != "all_candidates_archive_rejected" {
		t.Fatalf("unexpected archive summary %q", got)
	}
	if got := summarizeSearchFailureReason([]SearchCandidateRecord{
		{Rejected: true, RejectReason: "wrong_title"},
		{Rejected: true, RejectReason: "wrong_year"},
	}); got != "all_candidates_rejected" {
		t.Fatalf("unexpected mixed summary %q", got)
	}
}

func TestMatchesIgnoredPattern(t *testing.T) {
	patterns := []string{"*.nfo", "*.par2", "*sample.mkv", "*.jpg"}
	cases := map[string]bool{
		"movie.nfo":          true,
		"repair.PAR2":        true,
		"Cool.Sample.mkv":    true,
		"cover.JPG":          true,
		"movie.mkv":          false,
		"sample-not-mkv.mp4": false,
	}
	for name, want := range cases {
		if got := matchesIgnoredPattern(name, patterns); got != want {
			t.Fatalf("matchesIgnoredPattern(%q)=%v want %v", name, got, want)
		}
	}
}

func TestFilterImportedByPatterns(t *testing.T) {
	imported := ImportedNZB{
		FileName: "Test.nzb",
		Files: []ImportedNZBFile{
			{FileName: "Movie.mkv", Segments: []ImportedNZBSegment{{}, {}}},
			{FileName: "sample.mkv", Segments: []ImportedNZBSegment{{}}},
			{FileName: "notes.nfo", Segments: []ImportedNZBSegment{{}}},
		},
		FileCount:    3,
		SegmentCount: 4,
	}
	filtered := filterImportedByPatterns(imported, []string{"*.nfo", "*sample.mkv"})
	if len(filtered.Files) != 1 {
		t.Fatalf("expected 1 kept file, got %d", len(filtered.Files))
	}
	if filtered.Files[0].FileName != "Movie.mkv" {
		t.Fatalf("unexpected kept file %q", filtered.Files[0].FileName)
	}
	if filtered.FileCount != 1 {
		t.Fatalf("expected file count 1, got %d", filtered.FileCount)
	}
	if filtered.SegmentCount != 2 {
		t.Fatalf("expected segment count 2, got %d", filtered.SegmentCount)
	}
}

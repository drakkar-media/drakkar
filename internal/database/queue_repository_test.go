package database

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
)

func TestCreateImportedNZBConcurrentIdempotency(t *testing.T) {
	db := openTestDB(t)

	key := "queue-idempotency-test:" + strconv.FormatInt(time.Now().UnixNano(), 10)
	input := ImportedNZB{
		FileName:       "concurrent.nzb",
		XML:            []byte("<nzb/>"),
		IdempotencyKey: key,
	}

	const callers = 2
	start := make(chan struct{})
	results := make([]QueueSnapshot, callers)
	created := make([]bool, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], created[i], errs[i] = db.CreateImportedNZBIfAbsent(context.Background(), input)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if results[0].QueueItemID != results[1].QueueItemID {
		t.Fatalf("concurrent retries created different queue items: %d and %d", results[0].QueueItemID, results[1].QueueItemID)
	}
	if created[0] == created[1] {
		t.Fatalf("created flags = %v, want exactly one winner", created)
	}
	t.Cleanup(func() {
		_, _ = db.SQL.ExecContext(context.Background(), `delete from library_items where id = $1`, results[0].LibraryItemID)
	})
	var count int
	if err := db.SQL.QueryRowContext(context.Background(), `select count(*) from queue_items where idempotency_key = $1`, key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("idempotency key has %d queue rows, want 1", count)
	}
}

// openTestDB opens a connection to DRAKKAR_TEST_DATABASE_URL, skipping the
// test if it isn't set -- shared by every test in this package that needs a
// real database rather than a stub/mock.
func openTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	db, err := Open(config.DatabaseConfig{
		Host:     u.Hostname(),
		Port:     port,
		Name:     strings.TrimPrefix(u.Path, "/"),
		Username: u.User.Username(),
		Password: password,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestInsertImportedFilesBatchPreservesPerFileMessageIDsAndLinksVirtualFiles
// guards the batched multi-file insert introduced to replace a one-round-
// trip-per-file loop (a multi-file season-pack NZB used to pay one
// nzb_files + one virtual_files round trip per file). The regression this
// specifically targets: message_ids is a per-row text[] of variable length,
// which rules out the unnest()-backed batching pattern used by the sibling
// archive-import path -- unnest() on an array of arrays flattens every
// element across every row into one long sequence instead of preserving one
// sub-array per row, which would scramble each file's own message IDs
// across its siblings. Uses 3 files with DIFFERENT segment counts (2, 1, 3)
// specifically so an accidental flatten would be detectable: a subtly wrong
// batching would either miscount message_id_count per file or assign the
// wrong message IDs to the wrong file.
func TestInsertImportedFilesBatchPreservesPerFileMessageIDsAndLinksVirtualFiles(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	key := "queue-batch-import-test:" + strconv.FormatInt(time.Now().UnixNano(), 10)
	bigEnough := int64(60 * 1024 * 1024) // over minPlayableFileSizeBytes
	imported := ImportedNZB{
		FileName:       "batch-import.nzb",
		XML:            []byte("<nzb/>"),
		IdempotencyKey: key,
		Files: []ImportedNZBFile{
			{
				FileName:      "Episode.S01E01.mkv",
				Subject:       "subject-episode-1",
				Poster:        "poster-a",
				FileSizeBytes: bigEnough,
				Segments: []ImportedNZBSegment{
					{MessageID: "<e1-seg1@example>"},
					{MessageID: "<e1-seg2@example>"},
				},
			},
			{
				FileName:      "notes.txt",
				Subject:       "subject-notes",
				Poster:        "poster-b",
				FileSizeBytes: 512,
				Segments: []ImportedNZBSegment{
					{MessageID: "<nfo-seg1@example>"},
				},
			},
			{
				FileName:      "Episode.S01E02.mkv",
				Subject:       "subject-episode-2",
				Poster:        "poster-c",
				FileSizeBytes: bigEnough,
				Segments: []ImportedNZBSegment{
					{MessageID: "<e2-seg1@example>"},
					{MessageID: "<e2-seg2@example>"},
					{MessageID: "<e2-seg3@example>"},
				},
			},
		},
		FileCount:    3,
		SegmentCount: 6,
	}

	snapshot, err := db.CreateImportedNZB(ctx, imported)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.SQL.ExecContext(context.Background(), `delete from library_items where id = $1`, snapshot.LibraryItemID)
	})

	rows, err := db.SQL.QueryContext(ctx, `
		select nf.subject, nf.message_id_count, nf.message_ids
		from nzb_files nf
		join nzb_documents nd on nd.id = nf.nzb_document_id
		join selected_releases sr on sr.id = nd.selected_release_id
		where sr.id = (select selected_release_id from queue_items where id = $1)
		order by nf.subject`, snapshot.QueueItemID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type fileRow struct {
		subject   string
		msgCount  int
		messageID []string
	}
	byMsgCount := map[string]fileRow{}
	var got []fileRow
	for rows.Next() {
		var fr fileRow
		if err := rows.Scan(&fr.subject, &fr.msgCount, pgTextArrayScan(&fr.messageID)); err != nil {
			t.Fatal(err)
		}
		got = append(got, fr)
		byMsgCount[fr.subject] = fr
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 nzb_files rows, got %d: %+v", len(got), got)
	}

	want := map[string]struct {
		count int
		ids   []string
	}{
		"subject-episode-1": {2, []string{"<e1-seg1@example>", "<e1-seg2@example>"}},
		"subject-notes":     {1, []string{"<nfo-seg1@example>"}},
		"subject-episode-2": {3, []string{"<e2-seg1@example>", "<e2-seg2@example>", "<e2-seg3@example>"}},
	}
	for subject, w := range want {
		fr, ok := byMsgCount[subject]
		if !ok {
			t.Fatalf("missing nzb_files row for subject %q", subject)
		}
		if fr.msgCount != w.count {
			t.Fatalf("subject %q: message_id_count = %d, want %d (a flattened/scrambled batch insert would miscount this)", subject, fr.msgCount, w.count)
		}
		if strings.Join(fr.messageID, ",") != strings.Join(w.ids, ",") {
			t.Fatalf("subject %q: message_ids = %v, want %v (a flattened/scrambled batch insert would assign the wrong IDs to this file)", subject, fr.messageID, w.ids)
		}
	}

	var vfCount int
	if err := db.SQL.QueryRowContext(ctx, `
		select count(*)
		from virtual_files vf
		join nzb_files nf on nf.id = vf.nzb_file_id
		join nzb_documents nd on nd.id = nf.nzb_document_id
		join selected_releases sr on sr.id = nd.selected_release_id
		where sr.id = (select selected_release_id from queue_items where id = $1)`, snapshot.QueueItemID).Scan(&vfCount); err != nil {
		t.Fatal(err)
	}
	if vfCount != 2 {
		t.Fatalf("expected exactly 2 virtual_files rows (the 2 playable .mkv files, not the .nfo), got %d", vfCount)
	}

	var vfFileName string
	if err := db.SQL.QueryRowContext(ctx, `
		select vf.file_name
		from virtual_files vf
		join nzb_files nf on nf.id = vf.nzb_file_id
		where nf.subject = 'subject-episode-1'`).Scan(&vfFileName); err != nil {
		t.Fatal(err)
	}
	if vfFileName != "Episode.S01E01.mkv" {
		t.Fatalf("virtual_files row linked to the wrong nzb_files row: got file_name %q, want it linked via subject-episode-1's own nzb_file_id", vfFileName)
	}
}

func TestFirstNormalizedWord(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Daredevil", "daredevil"},
		{"Daredevil: Born Again", "daredevil"},
		{"Daredevil - Born Again", "daredevil"},
		{"Grey's Anatomy", "greys"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := firstNormalizedWord(tc.in); got != tc.want {
			t.Errorf("firstNormalizedWord(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsPlayableMedia(t *testing.T) {
	large := int64(200 * 1024 * 1024) // 200 MB — clearly a real video
	if !isPlayableMedia("Dune.mkv", large) {
		t.Fatal("expected mkv playable")
	}
	if !isPlayableMedia("clip.MP4", large) {
		t.Fatal("expected mp4 playable")
	}
	if isPlayableMedia("sample.rar", large) {
		t.Fatal("rar should not be marked playable")
	}
	if isPlayableMedia("Dune.mkv", 1024*1024) {
		t.Fatal("tiny mkv should not be playable (stub/extra)")
	}
}

func TestIsHardRejectReason(t *testing.T) {
	// archive_video_not_found and archive_headers_invalid were treated as
	// retryable until 2026-07-17: giving them extra chances (via the
	// now-removed "soft failure" leniency in
	// workflow.candidateFailurePenaltyProfile) let a candidate survive
	// well past a reasonable retry count, confirmed live as the direct
	// cause of a second NZB Finder account-termination warning. Both
	// describe a permanent, structural property of the actual posted
	// content -- the same segments produce the same result on every
	// retry -- so they must be hard rejects, same as archive_encrypted.
	if !isHardRejectReason("archive_video_not_found") {
		t.Fatal("archive_video_not_found is a permanent archive defect and must be a hard reject")
	}
	if !isHardRejectReason("archive_headers_invalid") {
		t.Fatal("archive_headers_invalid is a permanent archive defect and must be a hard reject")
	}
	if !isHardRejectReason(" Archive_Encrypted ") {
		t.Fatal("expected permanent archive reject to be hard")
	}
	if !isHardRejectReason("early preflight: Newshosting attempt 1: article missing") {
		t.Fatal("article missing (430) is confirmed gone per RFC 3977 -- must be a hard reject even at early preflight")
	}
	if !isHardRejectReason("preflight: first segment unavailable: article not found (cached)") {
		t.Fatal("article not found is confirmed gone per RFC 3977 -- must be a hard reject even at preflight")
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
		t.Fatal("expected permanent archive reject to persist in blocklist")
	}
	if !shouldPersistBlocklistReason("archive_headers_invalid") {
		t.Fatal("archive_headers_invalid is a permanent archive defect and must persist in blocklist")
	}
	if !shouldPersistBlocklistReason(" manual_reject ") {
		t.Fatal("expected manual reject to persist in blocklist")
	}
	if !shouldPersistBlocklistReason("early preflight: Newshosting attempt 1: article missing") {
		t.Fatal("article missing (430) is confirmed permanent -- must persist in blocklist even at early preflight")
	}
	if !shouldPersistBlocklistReason("preflight: first segment unavailable: article not found (cached)") {
		t.Fatal("article not found is confirmed permanent -- must persist in blocklist even at preflight")
	}
	if !shouldPersistBlocklistReason("strict health: first segment abc unavailable: yenc crc mismatch") {
		t.Fatal("expected crc mismatch to persist in blocklist")
	}
	if !shouldPersistBlocklistReason("publish failed: returned no readable bytes") {
		t.Fatal("expected invalid media payload to persist in blocklist")
	}
	// Regression for a gap found in the 2026-07-18 audit: isHardRejectReason
	// already treated missing_articles/nntp_article_unavailable (the
	// article-health-check policy's own classification of a confirmed-gone
	// article, internal/policy) as equivalent to "article missing"/"article
	// not found" -- but shouldPersistBlocklistReason's copy of that check
	// omitted these two, so a candidate durably rejected via this path was
	// never blocklisted, leaving a re-posted sibling free to hit the same
	// dead content again.
	if !shouldPersistBlocklistReason("article_health_check: missing_articles") {
		t.Fatal("missing_articles is confirmed permanent per the article-health-check policy -- must persist in blocklist")
	}
	if !shouldPersistBlocklistReason("preflight: nntp_article_unavailable") {
		t.Fatal("nntp_article_unavailable is confirmed permanent -- must persist in blocklist")
	}
	if shouldPersistBlocklistReason("context deadline exceeded") {
		t.Fatal("transient failure should not persist in blocklist")
	}
	if shouldPersistBlocklistReason("parse nzb xml: expected element type <nzb> but have <error>") {
		t.Fatal("indexer XML error should not persist in blocklist")
	}
	if shouldPersistBlocklistReason("indexer error 300: Invalid or outdated search result ID") {
		t.Fatal("stale indexer result IDs should not persist in blocklist")
	}
	if shouldPersistBlocklistReason("short archive header fetch") {
		t.Fatal("short archive header fetch should not persist in blocklist")
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
	const scopeKey = "movie:123"
	blocked := map[string]string{
		scopeKey + "|" + blocklistReleaseSignatureKey("RED.2010.1080p.BluRay.x264-GRP", "NZB Finder", 7340032000, time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)): "manual_reject",
	}
	reason, ok := blockedReleaseReason(scopeKey, blocked, SearchCandidateRecord{
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
	const scopeKey = "movie:123"
	blocked := map[string]string{
		scopeKey + "|" + blocklistReleaseFamilyKey("RED.2010.1080p.BluRay.x264-GRP", 7340032000, time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)): "archive_headers_invalid",
	}
	reason, ok := blockedReleaseReason(scopeKey, blocked, SearchCandidateRecord{
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
	const scopeKey = "show:45"
	blocked := map[string]string{
		scopeKey + "|" + blocklistReleasePatternKey("Yellowstone.S04E01.1080p.WEB-DL.x264-BADGRP", 1610612736, time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)): "invalid media payload",
	}
	reason, ok := blockedReleaseReason(scopeKey, blocked, SearchCandidateRecord{
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

// TestBlockedReleaseReasonScopedPerMediaNotGlobal guards a real production
// incident: a title-match bug (since fixed) let the base "NCIS" show wrongly
// select and fail on a "NCIS: New Orleans" release. Before this fix, blocklist
// keys were pure content signatures with no media scope, so blocklisting that
// release for the wrong show ("NCIS") would have ALSO permanently blocked the
// legitimate "NCIS: New Orleans" library item from ever using its own correct
// release again. Confirmed via Sonarr/Radarr reference source comparison:
// their BlocklistRepository always filters by SeriesId/MovieId first, so the
// same physical release blocklisted for one show never blocks a different
// show. blocklistKeysForRelease/blockedReleaseReason now take a scopeKey
// (resolved per library item via resolveMediaScopeKey) so the same release
// blocked under one show's scope key must not match under another's.
func TestBlockedReleaseReasonScopedPerMediaNotGlobal(t *testing.T) {
	candidate := SearchCandidateRecord{
		Title:       "NCIS.New.Orleans.S02E03.Touched.by.the.Sun.1080p.AMZN.WEB-DL.DDP5.1.x264-NTb",
		ExternalURL: "http://example/getnzb/abc123",
		IndexerName: "NZB Finder",
		SizeBytes:   3947154310,
		PostedAt:    time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC),
	}
	blockedForWrongShow := map[string]string{}
	for _, key := range blocklistKeysForRelease("show:50", candidate.Title, candidate.ExternalURL, candidate.IndexerName, candidate.SizeBytes, candidate.PostedAt) {
		blockedForWrongShow[key] = "early preflight: article missing"
	}
	if _, ok := blockedReleaseReason("show:50", blockedForWrongShow, candidate); !ok {
		t.Fatal("expected the release to be blocked under the show it was actually blocklisted for")
	}
	if _, ok := blockedReleaseReason("show:12", blockedForWrongShow, candidate); ok {
		t.Fatal("blocklisting a release under the wrong show's scope must not block the correct show (NCIS: New Orleans, show:12) from using it")
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

package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/drakkar-media/drakkar/internal/nntp"
	"github.com/drakkar-media/drakkar/internal/stream"
	"github.com/drakkar-media/drakkar/internal/yenc"
)

// mapSegmentChecker reports nntp.ErrArticleMissing for exactly the message
// IDs listed in missing, and success for everything else -- used to
// simulate a release whose first/last segments are fine but a scattered
// interior article is genuinely gone from the provider.
type mapSegmentChecker struct {
	missing map[string]bool
}

func (m *mapSegmentChecker) FetchRange(ctx context.Context, segment stream.SegmentRange) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mapSegmentChecker) Exists(ctx context.Context, messageID string) error {
	if m.missing[messageID] {
		return nntp.ErrArticleMissing
	}
	return nil
}

func insertPreflightFixture(t *testing.T, sqlDB *sql.DB, ctx context.Context, title string, files [][]string) (nzbDocID int64, cleanup func()) {
	t.Helper()
	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `insert into library_items (media_type, title) values ('tv', $1) returning id`, title).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `insert into release_candidates (library_item_id, title) values ($1, $2) returning id`, libID, title).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := sqlDB.QueryRowContext(ctx, `insert into selected_releases (library_item_id, release_candidate_id) values ($1, $2) returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `insert into nzb_documents (selected_release_id, file_name) values ($1, $2) returning id`, srID, title+".nzb").Scan(&nzbDocID); err != nil {
		t.Fatal(err)
	}
	for i, ids := range files {
		if _, err := sqlDB.ExecContext(ctx, `
			insert into nzb_files (nzb_document_id, subject, message_ids, decoded_segment_size)
			values ($1, $2, $3, 700000)`,
			nzbDocID, fmt.Sprintf("%s.part%d.mkv", title, i), pgTextArray(ids),
		); err != nil {
			t.Fatal(err)
		}
	}
	return nzbDocID, func() { sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID) }
}

func makeMessageIDs(n int, prefix string) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("<%s-%d>", prefix, i)
	}
	return ids
}

// TestPreflightCheckFirstSegmentsCatchesMissingInteriorSegment guards the
// 2026-08-11 production fix: a release can have a perfectly reachable first
// and last segment while a scattered article somewhere in the middle is
// genuinely gone from the provider (confirmed live for Landman S01E01/E02 --
// Newshosting returned "430 No Such Article" for a handful of interior
// segments while first/last both resolved fine). The old first/last-only
// preflight check gave these releases a false pass, and the failure was
// only ever discovered hours later, mid-playback, once a real read reached
// the missing offset. A single-file release must now also sample interior
// segments and fail preflight if any of them are missing.
func TestPreflightCheckFirstSegmentsCatchesMissingInteriorSegment(t *testing.T) {
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()

	ids := makeMessageIDs(100, "interior-missing")
	nzbDocID, cleanup := insertPreflightFixture(t, sqlDB, ctx, "preflight-interior-missing", [][]string{ids})
	defer cleanup()

	// Index 50 is well clear of both the first (0) and last (99) segments
	// already covered by loadNZBFirstLastSegmentPairs.
	checker := &mapSegmentChecker{missing: map[string]bool{ids[50]: true}}
	db := &DB{SQL: sqlDB, SegmentFetcher: checker}

	err = db.PreflightCheckFirstSegments(ctx, nzbDocID)
	if err == nil {
		t.Fatal("expected preflight to fail on a missing interior segment, got nil")
	}
}

// TestPreflightCheckFirstSegmentsPassesWhenInteriorSegmentsPresent is the
// mirror-image sanity check: with every segment reachable (including the
// interior sample), preflight must still pass.
func TestPreflightCheckFirstSegmentsPassesWhenInteriorSegmentsPresent(t *testing.T) {
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()

	ids := makeMessageIDs(100, "interior-ok")
	nzbDocID, cleanup := insertPreflightFixture(t, sqlDB, ctx, "preflight-interior-ok", [][]string{ids})
	defer cleanup()

	checker := &mapSegmentChecker{missing: map[string]bool{}}
	db := &DB{SQL: sqlDB, SegmentFetcher: checker}

	if err := db.PreflightCheckFirstSegments(ctx, nzbDocID); err != nil {
		t.Fatalf("expected preflight to pass when every segment is reachable, got: %v", err)
	}
}

// TestStrictCheckFirstSegmentsRequiresConfirmedCRCMismatch guards a real gap
// found 2026-08-11 while auditing why the blocklist held so many "yenc crc
// mismatch" entries: the deep health check's StrictCheckFirstSegments
// hard-failed (and blocklisted the release) on a SINGLE unconfirmed CRC
// mismatch, while the sibling calibration path (isArticlePermanentlyMissing)
// right in the same file already required confirmPermanentCRCMismatch's two
// independent, delayed, agreeing samples for the identical failure class --
// added after two confirmed live false positives on this provider. A CRC
// mismatch that doesn't reproduce on the confirmation re-fetch must not fail
// the check.
func TestStrictCheckFirstSegmentsRequiresConfirmedCRCMismatch(t *testing.T) {
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()
	withFastCRCConfirmation(t)

	t.Run("confirmed on every re-fetch -> fails", func(t *testing.T) {
		ids := makeMessageIDs(3, "strict-crc-confirmed")
		nzbDocID, cleanup := insertPreflightFixture(t, sqlDB, ctx, "strict-crc-confirmed", [][]string{ids})
		defer cleanup()

		sizer := &fakeSegmentSizer{sizeErr: yenc.ErrCRCMismatch}
		db := &DB{SQL: sqlDB, SegmentFetcher: sizer}
		if err := db.StrictCheckFirstSegments(ctx, nzbDocID); err == nil {
			t.Fatal("expected a confirmed CRC mismatch to fail the check")
		}
	})

	t.Run("not reproduced on re-fetch -> passes", func(t *testing.T) {
		ids := makeMessageIDs(3, "strict-crc-unconfirmed")
		nzbDocID, cleanup := insertPreflightFixture(t, sqlDB, ctx, "strict-crc-unconfirmed", [][]string{ids})
		defer cleanup()

		sizer := &fakeSegmentSizer{errsBySequence: []error{yenc.ErrCRCMismatch, nil}}
		db := &DB{SQL: sqlDB, SegmentFetcher: sizer}
		if err := db.StrictCheckFirstSegments(ctx, nzbDocID); err != nil {
			t.Fatalf("expected an unconfirmed CRC mismatch to pass, got: %v", err)
		}
	})
}

// TestPreflightCheckFirstSegmentsSkipsInteriorSamplingForMultiFileReleases
// locks in the deliberate scope limit: a multi-file release (e.g. a RAR
// volume set) must NOT get interior sampling, since checking every volume at
// that density is exactly the per-candidate check-volume burst that
// originally tripped the provider circuit breaker (see
// loadNZBFirstLastSegmentPairs). A missing interior segment in a non-first,
// non-last position of either file must NOT fail preflight here.
func TestPreflightCheckFirstSegmentsSkipsInteriorSamplingForMultiFileReleases(t *testing.T) {
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()

	part1 := makeMessageIDs(50, "multi-part1")
	part2 := makeMessageIDs(50, "multi-part2")
	nzbDocID, cleanup := insertPreflightFixture(t, sqlDB, ctx, "preflight-multi-file", [][]string{part1, part2})
	defer cleanup()

	// An interior segment of the FIRST file is missing -- not first/last of
	// either file, so it would only be caught by interior sampling, which
	// must not run for a multi-file release.
	checker := &mapSegmentChecker{missing: map[string]bool{part1[25]: true}}
	db := &DB{SQL: sqlDB, SegmentFetcher: checker}

	if err := db.PreflightCheckFirstSegments(ctx, nzbDocID); err != nil {
		t.Fatalf("expected preflight to pass (interior sampling must be skipped for multi-file releases), got: %v", err)
	}
}

// fakeSegmentPositionSizer implements SegmentSizer + SegmentPositionSizer so
// tests can control the declared yEnc decoded-start position
// StrictCheckFirstSegments' position check reads, independent of whether the
// segment "decodes" at all. startByMessageID holds the declared start for a
// given message ID; any message ID absent from it reports start=0 (correctly
// positioned), matching a real, unaffected article.
type fakeSegmentPositionSizer struct {
	startByMessageID map[string]int64
}

func (f *fakeSegmentPositionSizer) FetchRange(ctx context.Context, segment stream.SegmentRange) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeSegmentPositionSizer) DecodedSize(ctx context.Context, messageID string) (int64, error) {
	return 1024, nil
}

func (f *fakeSegmentPositionSizer) DecodedStart(ctx context.Context, messageID string) (start, size int64, valid bool, err error) {
	return f.startByMessageID[messageID], 1024, true, nil
}

// TestStrictCheckFirstSegmentsCatchesWrongFirstSegment guards the fix for the
// real incident this whole check exists to catch (2026-08-22): a library
// item's stored first segment message ID resolved, on live NNTP fetch, to a
// real, valid, successfully-decoding Usenet article that simply belonged to
// a different post entirely -- its own yEnc header honestly declared a
// decoded-start hundreds of megabytes into ITS source file. Every
// pre-existing check (article exists, decodes, has a plausible size) passed
// on this wrong article; only checking the declared position against
// maxPlausibleFirstSegmentStart catches it.
func TestStrictCheckFirstSegmentsCatchesWrongFirstSegment(t *testing.T) {
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()

	ids := makeMessageIDs(200, "wrong-first-segment")
	nzbDocID, cleanup := insertPreflightFixture(t, sqlDB, ctx, "strict-wrong-first-segment", [][]string{ids})
	defer cleanup()

	sizer := &fakeSegmentPositionSizer{startByMessageID: map[string]int64{
		ids[0]: 498121728, // matches the real corruption's order of magnitude
	}}
	db := &DB{SQL: sqlDB, SegmentFetcher: sizer}

	if err := db.StrictCheckFirstSegments(ctx, nzbDocID); err == nil {
		t.Fatal("expected a first segment whose own yEnc header places it far into an unrelated file to fail the check")
	}
}

// TestStrictCheckFirstSegmentsPassesCorrectlyPositionedFirstSegment is the
// mirror-image sanity check: a first segment whose declared position is
// exactly where it should be (byte 0) must not fail.
func TestStrictCheckFirstSegmentsPassesCorrectlyPositionedFirstSegment(t *testing.T) {
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()

	ids := makeMessageIDs(200, "correct-first-segment")
	nzbDocID, cleanup := insertPreflightFixture(t, sqlDB, ctx, "strict-correct-first-segment", [][]string{ids})
	defer cleanup()

	sizer := &fakeSegmentPositionSizer{startByMessageID: map[string]int64{ids[0]: 0}}
	db := &DB{SQL: sqlDB, SegmentFetcher: sizer}

	if err := db.StrictCheckFirstSegments(ctx, nzbDocID); err != nil {
		t.Fatalf("expected a correctly-positioned first segment to pass, got: %v", err)
	}
}

// TestStrictCheckFirstSegmentsDoesNotPositionCheckLastSegment locks in the
// deliberate scope limit: the last segment of a large file legitimately
// declares a decoded-start far beyond maxPlausibleFirstSegmentStart (it's
// near the END of the file) -- the position check must only ever apply to
// the first segment, never the last, or every large release would fail.
func TestStrictCheckFirstSegmentsDoesNotPositionCheckLastSegment(t *testing.T) {
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()

	ids := makeMessageIDs(200, "last-segment-far")
	nzbDocID, cleanup := insertPreflightFixture(t, sqlDB, ctx, "strict-last-segment-far", [][]string{ids})
	defer cleanup()

	sizer := &fakeSegmentPositionSizer{startByMessageID: map[string]int64{
		ids[0]:   0,         // first segment: correctly at the start
		ids[199]: 140000000, // last segment: legitimately far into the file
	}}
	db := &DB{SQL: sqlDB, SegmentFetcher: sizer}

	if err := db.StrictCheckFirstSegments(ctx, nzbDocID); err != nil {
		t.Fatalf("expected the last segment's large (but legitimate) position to be ignored, got: %v", err)
	}
}

// TestStrictCheckFirstSegmentsSkipsPositionCheckWithoutPositionSupport
// guards backward compatibility: a SegmentFetcher that only implements
// SegmentSizer (no SegmentPositionSizer) -- e.g. a source without yEnc
// PartInfo access -- must keep passing exactly as before this check was
// added, not fail or panic from a missing capability.
func TestStrictCheckFirstSegmentsSkipsPositionCheckWithoutPositionSupport(t *testing.T) {
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()

	ids := makeMessageIDs(200, "no-position-support")
	nzbDocID, cleanup := insertPreflightFixture(t, sqlDB, ctx, "strict-no-position-support", [][]string{ids})
	defer cleanup()

	// fakeSegmentSizer implements SegmentSizer only (see calibrate_test.go) --
	// no DecodedStart method, so it cannot satisfy SegmentPositionSizer.
	sizer := &fakeSegmentSizer{}
	db := &DB{SQL: sqlDB, SegmentFetcher: sizer}

	if err := db.StrictCheckFirstSegments(ctx, nzbDocID); err != nil {
		t.Fatalf("expected a fetcher without position support to pass (graceful degradation), got: %v", err)
	}
}

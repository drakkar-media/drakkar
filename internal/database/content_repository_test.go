package database

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/stream"
	_ "github.com/jackc/pgx/v5/stdlib"
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

// blockingRangeInfoFetcherStub never returns on its own -- it blocks until
// the context passed to FetchRangeInfo is done, then reports that context's
// error. Used to simulate a stalled/slow live probe fetch.
type blockingRangeInfoFetcherStub struct{}

func (blockingRangeInfoFetcherStub) FetchRange(ctx context.Context, segment stream.SegmentRange) ([]byte, error) {
	return nil, nil
}

func (blockingRangeInfoFetcherStub) FetchRangeInfo(ctx context.Context, segment stream.SegmentRange) ([]byte, stream.SegmentSpan, error) {
	<-ctx.Done()
	return nil, stream.SegmentSpan{}, ctx.Err()
}

// TestVerifyLastSpanBoundaryDoesNotBlockIndefinitely guards a real,
// caught-in-the-act production incident (2026-08-10): this probe runs
// synchronously on every uncached OpenVirtualMediaFile call -- i.e. on the
// critical path of a player's webdav OpenFile -- and a live goroutine dump
// showed it stalled on a slow/cold NNTP round trip for an unbounded time,
// hanging the entire open with it (reproduced live as "seeking gets stuck
// and video never starts"). The probe fetch must be bounded so a stalled
// fetch degrades to the pre-existing, already-safe fallback (uncorrected
// spans -- StoredRarReader.realignSpan self-corrects transparently during
// the real read that follows) instead of blocking the caller forever.
func TestVerifyLastSpanBoundaryDoesNotBlockIndefinitely(t *testing.T) {
	origTimeout := verifyLastSpanBoundaryTimeout
	verifyLastSpanBoundaryTimeout = 20 * time.Millisecond
	t.Cleanup(func() { verifyLastSpanBoundaryTimeout = origTimeout })

	db := &DB{SegmentFetcher: blockingRangeInfoFetcherStub{}}
	spans := []stream.SegmentSpan{
		{SegmentID: 1, MessageID: "<seg1>", Start: 0, End: 10, DecodedStart: 0, SegmentByteStart: 0},
		{SegmentID: 2, MessageID: "<seg2>", Start: 10, End: 19, DecodedStart: 10, SegmentByteStart: 0},
	}

	done := make(chan []stream.SegmentSpan, 1)
	go func() {
		done <- db.verifyLastSpanBoundary(context.Background(), spans)
	}()

	select {
	case result := <-done:
		if result[1].End != 19 {
			t.Fatalf("expected unchanged (uncorrected) End=19 on a bounded timeout, got %d", result[1].End)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("verifyLastSpanBoundary did not return within a reasonable multiple of its timeout -- it blocked on the stalled fetch")
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

// TestLoadVFCacheInitializesNilCacheMap guards a real CI failure
// (2026-08-10): several tests in this package construct *DB directly as
// &DB{SQL: sqlDB}, bypassing Open, to avoid a full pgxpool setup -- which
// left vfCache nil. loadVFCache's cache-fill write then panicked
// ("assignment to entry in nil map") the moment a test actually reached a
// real virtual_files row instead of erroring out earlier, surfacing only
// when shared CI test-database state happened to leave such a row behind.
// vfCache must self-initialize on first write regardless of how *DB was
// constructed.
func TestLoadVFCacheInitializesNilCacheMap(t *testing.T) {
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

	db := &DB{SQL: sqlDB}
	if db.vfCache != nil {
		t.Fatal("expected vfCache to start nil for this test to be meaningful")
	}

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('movie', 'vfcache-nil-map-check', true)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, selected)
		values ($1, 'vfcache-nil-map-check release', true)
		returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}

	var srID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}

	var vfID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, size_bytes, reader_kind, inline_bytes)
		values ($1, 'test.txt', 'test.txt', 3, 'inline', $2)
		returning id`, srID, []byte("abc")).Scan(&vfID); err != nil {
		t.Fatal(err)
	}

	vf, err := db.OpenVirtualMediaFile(ctx, vfID)
	if err != nil {
		t.Fatalf("expected no panic and no error, got: %v", err)
	}
	if vf.Size() != 3 {
		t.Fatalf("expected size 3, got %d", vf.Size())
	}
}

// TestGetCurrentFileDetailResolvesSeasonPackEpisodeViaSymlinkPublication
// covers a season pack: every episode's selected_releases row is created
// independently (see FulfillEpisodeLibraryItem/season_pack_fulfillment.go),
// but every virtual_files row for the pack still points at the single
// triggering selected_release_id -- so a naive join on
// vf.selected_release_id = sr.id (the per-episode row) never finds the
// actual file. symlink_publications is the only table that actually maps
// library_item_id -> the right virtual_file_id, and must be preferred.
func TestGetCurrentFileDetailResolvesSeasonPackEpisodeViaSymlinkPublication(t *testing.T) {
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
	db := &DB{SQL: sqlDB}

	var packLibID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('episode', 'season-pack-check-triggering', true)
		returning id`).Scan(&packLibID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, packLibID)

	var episodeLibID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('episode', 'season-pack-check-episode', true)
		returning id`).Scan(&episodeLibID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, episodeLibID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, selected, indexer_name, resolution, score)
		values ($1, 'season-pack-check release', true, 'NZB Finder', '1080p', 1398)
		returning id`, packLibID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}

	var packSrID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, packLibID, rcID).Scan(&packSrID); err != nil {
		t.Fatal(err)
	}

	// The per-episode selected_releases row FulfillEpisodeLibraryItem creates
	// -- deliberately has no virtual_files of its own.
	var episodeSrID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, episodeLibID, rcID).Scan(&episodeSrID); err != nil {
		t.Fatal(err)
	}

	var vfID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, size_bytes, reader_kind, inline_bytes)
		values ($1, 'Episode.mkv', 'Episode.mkv', 1887750951, 'inline', $2)
		returning id`, packSrID, []byte("x")).Scan(&vfID); err != nil {
		t.Fatal(err)
	}

	if err := db.UpsertSymlinkPublication(ctx, episodeLibID, vfID, "/library/Episode.mkv", "/downloads/Episode.mkv"); err != nil {
		t.Fatal(err)
	}

	detail, found, err := db.GetCurrentFileDetail(ctx, episodeLibID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected found = true")
	}
	if detail.FileName != "Episode.mkv" || detail.FileSizeBytes != 1887750951 {
		t.Fatalf("expected season-pack episode's real file via symlink_publications, got %+v", detail)
	}
}

package database

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"io"
	"os"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/rarcrypto"
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

// TestGetEmbeddedSubtitleLanguagesForLibraryItemResolvesSeasonPackEpisode
// mirrors TestGetCurrentFileDetailResolvesSeasonPackEpisodeViaSymlinkPublication:
// a season-pack episode's virtual_files row lives under the pack's own
// selected_release_id, not the per-episode one FulfillEpisodeLibraryItem
// creates, so symlink_publications must be consulted first here too, or
// every season-pack episode would silently report "nothing embedded" and
// download subtitles the file already has.
func TestGetEmbeddedSubtitleLanguagesForLibraryItemResolvesSeasonPackEpisode(t *testing.T) {
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
		values ('episode', 'embedded-subs-pack-triggering', true)
		returning id`).Scan(&packLibID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, packLibID)

	var episodeLibID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('episode', 'embedded-subs-pack-episode', true)
		returning id`).Scan(&episodeLibID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, episodeLibID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, selected)
		values ($1, 'embedded-subs-pack release', true)
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

	// Per-episode selected_releases row -- deliberately has no virtual_files
	// of its own, matching FulfillEpisodeLibraryItem's real behavior.
	var episodeSrID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, episodeLibID, rcID).Scan(&episodeSrID); err != nil {
		t.Fatal(err)
	}

	var vfID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, size_bytes, reader_kind, inline_bytes, embedded_subtitle_languages)
		values ($1, 'Episode.mkv', 'Episode.mkv', 3, 'inline', $2, $3)
		returning id`, packSrID, []byte("abc"), pgTextArray([]string{"en", "nl"})).Scan(&vfID); err != nil {
		t.Fatal(err)
	}

	if err := db.UpsertSymlinkPublication(ctx, episodeLibID, vfID, "/library/Episode.mkv", "/downloads/Episode.mkv"); err != nil {
		t.Fatal(err)
	}

	languages, err := db.GetEmbeddedSubtitleLanguagesForLibraryItem(ctx, episodeLibID)
	if err != nil {
		t.Fatal(err)
	}
	if len(languages) != 2 || languages[0] != "en" || languages[1] != "nl" {
		t.Fatalf("expected [en nl] resolved via symlink_publications, got %v", languages)
	}
}

// TestGetEmbeddedSubtitleLanguagesForLibraryItemDefaultsToEmpty covers the
// "never probed yet" case: a fresh virtual_files row (NULL column) must
// resolve to an empty slice, not an error or nil that callers would have to
// special-case.
func TestGetEmbeddedSubtitleLanguagesForLibraryItemDefaultsToEmpty(t *testing.T) {
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

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('movie', 'embedded-subs-never-probed', true)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, selected)
		values ($1, 'embedded-subs-never-probed release', true)
		returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, size_bytes, reader_kind, inline_bytes)
		values ($1, 'never-probed.mkv', 'never-probed.mkv', 3, 'inline', $2)`,
		srID, []byte("abc"),
	); err != nil {
		t.Fatal(err)
	}

	languages, err := db.GetEmbeddedSubtitleLanguagesForLibraryItem(ctx, libID)
	if err != nil {
		t.Fatal(err)
	}
	if len(languages) != 0 {
		t.Fatalf("expected empty slice for a never-probed file, got %v", languages)
	}
}

// TestSetContainerProbeResultAndGetContainerDurationForLibraryItem covers
// the write/read round trip the subtitle-sync framerate-mismatch check
// depends on: SetContainerProbeResult (written once by the embedded-
// subtitle probe) must be readable back via
// GetContainerDurationForLibraryItem, resolving the same
// symlink_publications-first / selected_releases-fallback file as
// GetEmbeddedSubtitleLanguagesForLibraryItem.
func TestSetContainerProbeResultAndGetContainerDurationForLibraryItem(t *testing.T) {
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

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('movie', 'container-duration-check', true)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, selected)
		values ($1, 'container-duration-check release', true)
		returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}
	var vfID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, size_bytes, reader_kind, inline_bytes)
		values ($1, 'container-duration.mkv', 'container-duration.mkv', 3, 'inline', $2)
		returning id`, srID, []byte("abc")).Scan(&vfID); err != nil {
		t.Fatal(err)
	}

	// Before any probe: duration unknown.
	if _, ok, err := db.GetContainerDurationForLibraryItem(ctx, libID); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("expected ok=false before any probe result is recorded")
	}

	if err := db.SetContainerProbeResult(ctx, vfID, []string{"en"}, 7230.5); err != nil {
		t.Fatal(err)
	}

	duration, ok, err := db.GetContainerDurationForLibraryItem(ctx, libID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true after recording a probe result")
	}
	if duration != 7230.5 {
		t.Fatalf("duration = %v, want 7230.5", duration)
	}

	languages, err := db.GetEmbeddedSubtitleLanguagesForLibraryItem(ctx, libID)
	if err != nil {
		t.Fatal(err)
	}
	if len(languages) != 1 || languages[0] != "en" {
		t.Fatalf("expected SetContainerProbeResult to also record languages, got %v", languages)
	}
}

// TestSetContainerProbeResultZeroDurationStaysUnknown covers the "ffprobe
// ran but couldn't determine duration from the truncated prefix" case: 0
// must be stored as NULL/unknown, not as a literal 0-second duration a
// caller might otherwise divide by.
func TestSetContainerProbeResultZeroDurationStaysUnknown(t *testing.T) {
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

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('movie', 'container-duration-zero-check', true)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, selected)
		values ($1, 'container-duration-zero-check release', true)
		returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}
	var vfID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, size_bytes, reader_kind, inline_bytes)
		values ($1, 'container-duration-zero.mkv', 'container-duration-zero.mkv', 3, 'inline', $2)
		returning id`, srID, []byte("abc")).Scan(&vfID); err != nil {
		t.Fatal(err)
	}

	if err := db.SetContainerProbeResult(ctx, vfID, nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := db.GetContainerDurationForLibraryItem(ctx, libID); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("expected ok=false when the probe recorded a 0 duration")
	}
}

// TestImportSelectedReleaseNZBPersistsArchivePassword guards the end-to-end
// wiring for the archive-password extraction feature: an ImportedNZB
// carrying ArchivePassword (from the NZB's <meta type="password">, see
// internal/nzb.Document.Password) must land in nzb_documents.archive_password,
// not be silently dropped -- the historical behavior before this session's
// fix (the password was never even parsed out of the NZB XML).
func TestImportSelectedReleaseNZBPersistsArchivePassword(t *testing.T) {
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

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('movie', 'archive-password-check', false)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, selected)
		values ($1, 'archive-password-check release', 'http://example/archive-password-check', true)
		returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into queue_items (library_item_id, state, idempotency_key, selected_release_id)
		values ($1, 'fetching_nzb', $2, $3)`, libID, "archive-password-check-key", srID); err != nil {
		t.Fatal(err)
	}

	imported := ImportedNZB{
		FileName:        "archive-password-check.nzb",
		ExternalURL:     "http://example/archive-password-check",
		ArchivePassword: "s3cr3t-from-nzb",
		Files: []ImportedNZBFile{
			{
				FileName:      "movie.mkv",
				FileSizeBytes: 1000,
				Segments: []ImportedNZBSegment{
					{Number: 1, MessageID: "<archive-password-check-1>", EncodedSizeBytes: 1030, DecodedStartOffset: 0, DecodedEndOffset: 1000},
				},
			},
		},
	}

	if _, err := db.ImportSelectedReleaseNZB(ctx, srID, imported); err != nil {
		t.Fatal(err)
	}

	var password sql.NullString
	if err := sqlDB.QueryRowContext(ctx, `
		select archive_password from nzb_documents where selected_release_id = $1`, srID,
	).Scan(&password); err != nil {
		t.Fatal(err)
	}
	if !password.Valid || password.String != "s3cr3t-from-nzb" {
		t.Fatalf("archive_password = %+v, want valid %q", password, "s3cr3t-from-nzb")
	}
}

// TestOpenVirtualMediaFileDecryptsPasswordProtectedRAR is the full
// end-to-end integration test for password-protected RAR streaming: a
// virtual_files row carrying real salt/IV/lg2Count (as insertArchiveVirtualFile
// would persist after inspectRAR5 verified the password) plus the release's
// stored nzb_documents.archive_password must make OpenVirtualMediaFile
// return a reader whose ReadAt transparently decrypts -- exactly like an
// unencrypted stored_rar file, with zero special-casing needed by any
// caller (health checks, FUSE reads, everything goes through this same
// OpenVirtualMediaFile choke point).
func TestOpenVirtualMediaFileDecryptsPasswordProtectedRAR(t *testing.T) {
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

	const password = "correct-password"
	var salt, iv [16]byte
	for i := range salt {
		salt[i] = byte(0x20 + i)
	}
	for i := range iv {
		iv[i] = byte(0x90 + i)
	}
	const lg2Count = 4

	plaintext := bytes.Repeat([]byte("ENCRYPTED-VIDEO!"), 4) // 64 bytes, block-aligned
	// EncryptedRarReader mandatorily verifies the decrypted output against
	// a real video container's magic number before trusting it (see its
	// doc comment) -- give it real MKV/EBML magic at the start so this
	// test exercises that check succeeding, not failing on unrelated
	// fixture data that was never meant to look like video.
	copy(plaintext, []byte{0x1a, 0x45, 0xdf, 0xa3})
	key, err := rarcrypto.DeriveKey(password, rarcrypto.EncryptionParams{Lg2Count: lg2Count, Salt: salt})
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(ciphertext, plaintext)

	db := &DB{SQL: sqlDB, SegmentFetcher: fetcherStub{data: ciphertext}}

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('movie', 'encrypted-rar-e2e-check', true)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, selected)
		values ($1, 'encrypted-rar-e2e-check release', true)
		returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}
	var nzbDocID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into nzb_documents (selected_release_id, file_name, archive_password)
		values ($1, 'encrypted-rar-e2e-check.nzb', $2) returning id`, srID, password).Scan(&nzbDocID); err != nil {
		t.Fatal(err)
	}
	var nzbFileID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into nzb_files (nzb_document_id, subject, message_ids, decoded_segment_size)
		values ($1, 'encrypted-part', $2, $3)
		returning id`, nzbDocID, "{<msg1>}", len(ciphertext)).Scan(&nzbFileID); err != nil {
		t.Fatal(err)
	}
	var vfID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into virtual_files (
			selected_release_id, path, file_name, size_bytes, reader_kind,
			nzb_file_id, segment_byte_offset,
			rar_encryption_salt, rar_encryption_iv, rar_encryption_lg2_count
		) values ($1, 'Movie.mkv', 'Movie.mkv', $2, 'stored_rar', $3, 0, $4, $5, $6)
		returning id`,
		srID, len(plaintext), nzbFileID, salt[:], iv[:], lg2Count,
	).Scan(&vfID); err != nil {
		t.Fatal(err)
	}

	vf, err := db.OpenVirtualMediaFile(ctx, vfID)
	if err != nil {
		t.Fatalf("OpenVirtualMediaFile: %v", err)
	}
	got := make([]byte, len(plaintext))
	n, err := vf.ReadAt(ctx, got, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAt: %v", err)
	}
	if n != len(plaintext) || !bytes.Equal(got, plaintext) {
		t.Fatalf("ReadAt returned %q, want %q", got[:n], plaintext)
	}
}

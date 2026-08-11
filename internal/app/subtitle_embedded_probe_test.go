package app

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/stream"
)

// countingFetcherStub records how many times FetchRange was called, so
// tests can assert on real NNTP-fetch activity rather than just on the
// probe's return value.
type countingFetcherStub struct {
	calls *int
}

func (f countingFetcherStub) FetchRange(ctx context.Context, segment stream.SegmentRange) ([]byte, error) {
	*f.calls++
	n := segment.RangeEnd - segment.RangeStart
	if n < 0 {
		n = 0
	}
	return make([]byte, n), nil
}

// TestProbeEmbeddedSubtitleLanguagesDoesNotReprobeOnceRecorded guards the
// resource-safety requirement behind this whole feature: probing a file's
// embedded subtitle languages means a real (background-priority) NNTP read,
// so it must happen at most once per virtual file, ever -- not on every
// publish/republish event that fires probeEmbeddedSubtitleLanguagesAsync.
func TestProbeEmbeddedSubtitleLanguagesDoesNotReprobeOnceRecorded(t *testing.T) {
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

	calls := 0
	db := &database.DB{SQL: sqlDB, SegmentFetcher: countingFetcherStub{calls: &calls}}

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('movie', 'embedded-subtitle-probe-no-reprobe', true)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, selected)
		values ($1, 'embedded-subtitle-probe-no-reprobe release', true)
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
		insert into nzb_documents (selected_release_id, file_name)
		values ($1, 'embedded-subtitle-probe-no-reprobe.nzb') returning id`, srID).Scan(&nzbDocID); err != nil {
		t.Fatal(err)
	}
	var nzbFileID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into nzb_files (nzb_document_id, subject, message_ids, decoded_segment_size)
		values ($1, 'embedded-subtitle-probe-part', $2, 10)
		returning id`, nzbDocID, "{<msg1>,<msg2>,<msg3>}").Scan(&nzbFileID); err != nil {
		t.Fatal(err)
	}
	var vfID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, size_bytes, reader_kind, nzb_file_id)
		values ($1, 'embedded-subtitle-probe.mkv', 'embedded-subtitle-probe.mkv', 25, 'direct_nzb', $2)
		returning id`, srID, nzbFileID).Scan(&vfID); err != nil {
		t.Fatal(err)
	}

	probeEmbeddedSubtitleLanguages(ctx, db, libID)
	if calls == 0 {
		t.Fatal("expected at least one NNTP fetch on the first probe")
	}
	firstCalls := calls

	probed, err := db.EmbeddedSubtitleLanguagesProbed(ctx, vfID)
	if err != nil {
		t.Fatal(err)
	}
	if !probed {
		t.Fatal("expected embedded_subtitle_languages to be recorded (non-NULL) after the first probe")
	}

	probeEmbeddedSubtitleLanguages(ctx, db, libID)
	if calls != firstCalls {
		t.Fatalf("expected no additional NNTP fetches on a second probe of the same file, calls went from %d to %d", firstCalls, calls)
	}
}

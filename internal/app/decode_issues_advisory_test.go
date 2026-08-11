package app

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/stream"
	"github.com/rs/zerolog"
)

// rangeServingFetcherStub serves byte ranges directly from a fixed backing
// buffer, unlike countingFetcherStub (which only returns zero-filled bytes)
// -- needed here since checkDecodeIssuesAdvisory's whole point is to run a
// real ffmpeg decode check against real bytes.
type rangeServingFetcherStub struct {
	data []byte
}

func (f rangeServingFetcherStub) FetchRange(ctx context.Context, segment stream.SegmentRange) ([]byte, error) {
	return append([]byte(nil), f.data[segment.RangeStart:segment.RangeEnd]...), nil
}

func setupDecodeAdvisoryFixture(t *testing.T, sqlDB *sql.DB, ctx context.Context, titleSuffix string, data []byte) (libID int64, vfID int64) {
	t.Helper()
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('movie', $1, true)
		returning id`, "decode-advisory-"+titleSuffix).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, selected)
		values ($1, 'decode-advisory release', true)
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
		values ($1, 'decode-advisory.nzb') returning id`, srID).Scan(&nzbDocID); err != nil {
		t.Fatal(err)
	}
	var nzbFileID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into nzb_files (nzb_document_id, subject, message_ids, decoded_segment_size)
		values ($1, 'decode-advisory-part', $2, $3)
		returning id`, nzbDocID, "{<msg1>}", len(data)).Scan(&nzbFileID); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, size_bytes, reader_kind, nzb_file_id)
		values ($1, 'decode-advisory.mkv', 'decode-advisory.mkv', $2, 'direct_nzb', $3)
		returning id`, srID, len(data), nzbFileID).Scan(&vfID); err != nil {
		t.Fatal(err)
	}
	return libID, vfID
}

func TestCheckDecodeIssuesAdvisoryLogsWarningForCorruptedFile(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed in this environment -- production-image-only dependency")
	}
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

	data := buildDecodeAdvisoryFixtureMKV(t)
	corrupted := append([]byte{}, data...)
	cut := len(corrupted) * 3 / 4
	for i := cut; i < len(corrupted); i++ {
		corrupted[i] = 0
	}

	db := &database.DB{SQL: sqlDB, SegmentFetcher: rangeServingFetcherStub{data: corrupted}}
	libID, vfID := setupDecodeAdvisoryFixture(t, sqlDB, ctx, "corrupted", corrupted)
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf)
	candidate := database.DeepHealthCandidate{LibraryItemID: libID, Title: "decode-advisory", VirtualFileID: vfID}

	checkDecodeIssuesAdvisory(ctx, db, logger, candidate)

	if !strings.Contains(logBuf.String(), "decode issues") {
		t.Fatalf("expected a decode-issues warning to be logged, got: %s", logBuf.String())
	}
}

func TestCheckDecodeIssuesAdvisorySilentForCleanFile(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed in this environment -- production-image-only dependency")
	}
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

	data := buildDecodeAdvisoryFixtureMKV(t)
	db := &database.DB{SQL: sqlDB, SegmentFetcher: rangeServingFetcherStub{data: data}}
	libID, vfID := setupDecodeAdvisoryFixture(t, sqlDB, ctx, "clean", data)
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf)
	candidate := database.DeepHealthCandidate{LibraryItemID: libID, Title: "decode-advisory", VirtualFileID: vfID}

	checkDecodeIssuesAdvisory(ctx, db, logger, candidate)

	if strings.Contains(logBuf.String(), "decode issues") {
		t.Fatalf("expected no decode-issues warning for a clean file, got: %s", logBuf.String())
	}
}

// buildDecodeAdvisoryFixtureMKV builds a small, real, valid MKV via ffmpeg
// (only called after confirming ffmpeg is on PATH).
func buildDecodeAdvisoryFixtureMKV(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	outPath := dir + "/out.mkv"
	cmd := exec.Command("ffmpeg",
		"-f", "lavfi", "-i", "testsrc=size=64x64:duration=2:rate=10",
		"-c:v", "libx264",
		outPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg fixture build failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

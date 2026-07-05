package database

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestInsertImportedArchivesBatchAgainstRealDB exercises the batched
// insertArchiveVolumes/insertArchiveEntries/insertArchiveRanges directly
// against a real Postgres instance (skipped unless one is reachable via
// DRAKKAR_TEST_DATABASE_URL) to confirm the unnest-based multi-row inserts
// produce identical rows to the old one-row-per-statement version: correct
// volume_index -> id mapping, correct path -> id mapping, and every range
// linked to the right entry and volume.
func TestInsertImportedArchivesBatchAgainstRealDB(t *testing.T) {
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

	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	var libID int64
	if err := tx.QueryRowContext(ctx, `insert into library_items (media_type, title) values ('tv','batch verify') returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	var rcID int64
	if err := tx.QueryRowContext(ctx, `insert into release_candidates (library_item_id, title) values ($1, 'batch verify') returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := tx.QueryRowContext(ctx, `insert into selected_releases (library_item_id, release_candidate_id) values ($1, $2) returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}
	var nzbDocID int64
	if err := tx.QueryRowContext(ctx, `insert into nzb_documents (selected_release_id, file_name) values ($1, 'show.nzb') returning id`, srID).Scan(&nzbDocID); err != nil {
		t.Fatal(err)
	}
	fileSegments := map[string]importedFileSegments{}
	for _, name := range []string{"show.r00", "show.r01"} {
		var nzbFileID int64
		if err := tx.QueryRowContext(ctx, `
			insert into nzb_files (nzb_document_id, subject, message_ids) values ($1, $2, '{}')
			returning id`, nzbDocID, name).Scan(&nzbFileID); err != nil {
			t.Fatal(err)
		}
		fileSegments[name] = importedFileSegments{fileName: name, nzbFileID: nzbFileID}
	}

	archives := []ImportedArchive{
		{
			Kind:   "rar",
			Status: "supported",
			Volumes: []ImportedArchiveVolume{
				{Path: "show.r00", VolumeIndex: 0},
				{Path: "show.r01", VolumeIndex: 1},
			},
			Entries: []ImportedArchiveEntry{
				{
					Path: "episode1.mkv", SizeBytes: 500 << 20, PackedSizeBytes: 480 << 20,
					CompressionMethod: "store", VolumeIndex: 0, ArchiveOffset: 100,
					Ranges: []ImportedArchiveRange{
						{VolumeIndex: 0, EntryOffset: 0, ArchiveOffset: 100, LengthBytes: 900},
						{VolumeIndex: 1, EntryOffset: 900, ArchiveOffset: 0, LengthBytes: 200},
					},
				},
				{
					Path: "episode2.mkv", SizeBytes: 600 << 20, PackedSizeBytes: 590 << 20,
					CompressionMethod: "store", VolumeIndex: 1, ArchiveOffset: 200,
					Ranges: []ImportedArchiveRange{
						{VolumeIndex: 1, EntryOffset: 0, ArchiveOffset: 200, LengthBytes: 800},
					},
				},
			},
		},
	}

	if err := insertImportedArchives(ctx, tx, srID, archives, fileSegments); err != nil {
		t.Fatal(err)
	}

	var archiveID int64
	if err := tx.QueryRowContext(ctx, `select id from archives where selected_release_id = $1`, srID).Scan(&archiveID); err != nil {
		t.Fatal(err)
	}

	var volCount int
	if err := tx.QueryRowContext(ctx, `select count(*) from archive_volumes where archive_id = $1`, archiveID).Scan(&volCount); err != nil {
		t.Fatal(err)
	}
	if volCount != 2 {
		t.Fatalf("expected 2 volumes, got %d", volCount)
	}

	var vol0ID, vol1ID int64
	if err := tx.QueryRowContext(ctx, `select id from archive_volumes where archive_id = $1 and volume_index = 0`, archiveID).Scan(&vol0ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `select id from archive_volumes where archive_id = $1 and volume_index = 1`, archiveID).Scan(&vol1ID); err != nil {
		t.Fatal(err)
	}

	var entryCount int
	if err := tx.QueryRowContext(ctx, `select count(*) from archive_entries where archive_id = $1`, archiveID).Scan(&entryCount); err != nil {
		t.Fatal(err)
	}
	if entryCount != 2 {
		t.Fatalf("expected 2 entries, got %d", entryCount)
	}

	var episode1ID int64
	var episode1Size int64
	if err := tx.QueryRowContext(ctx, `select id, size_bytes from archive_entries where archive_id = $1 and path = 'episode1.mkv'`, archiveID).Scan(&episode1ID, &episode1Size); err != nil {
		t.Fatal(err)
	}
	if episode1Size != 500<<20 {
		t.Fatalf("expected episode1.mkv size_bytes=%d, got %d", 500<<20, episode1Size)
	}

	rows, err := tx.QueryContext(ctx, `
		select archive_volume_id, entry_offset, archive_offset, length_bytes
		from archive_ranges where archive_entry_id = $1 order by entry_offset`, episode1ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type rangeRow struct {
		volumeID                       int64
		entryOffset, archiveOffset, ln int64
	}
	var got []rangeRow
	for rows.Next() {
		var r rangeRow
		if err := rows.Scan(&r.volumeID, &r.entryOffset, &r.archiveOffset, &r.ln); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 ranges for episode1.mkv, got %d: %+v", len(got), got)
	}
	if got[0].volumeID != vol0ID || got[0].entryOffset != 0 || got[0].archiveOffset != 100 || got[0].ln != 900 {
		t.Fatalf("range[0] mismatch: %+v (want volumeID=%d)", got[0], vol0ID)
	}
	if got[1].volumeID != vol1ID || got[1].entryOffset != 900 || got[1].archiveOffset != 0 || got[1].ln != 200 {
		t.Fatalf("range[1] mismatch: %+v (want volumeID=%d)", got[1], vol1ID)
	}

	var totalRanges int
	if err := tx.QueryRowContext(ctx, `
		select count(*) from archive_ranges ar join archive_entries ae on ae.id = ar.archive_entry_id
		where ae.archive_id = $1`, archiveID).Scan(&totalRanges); err != nil {
		t.Fatal(err)
	}
	if totalRanges != 3 {
		t.Fatalf("expected 3 total ranges (2 + 1), got %d", totalRanges)
	}

	var vfCount int
	if err := tx.QueryRowContext(ctx, `
		select count(*) from virtual_files where selected_release_id = $1 and reader_kind = 'stored_rar'`, srID).Scan(&vfCount); err != nil {
		t.Fatal(err)
	}
	if vfCount != 2 {
		t.Fatalf("expected 2 stored_rar virtual_files (episode1, episode2), got %d", vfCount)
	}
}

package database

import (
	"context"
	"database/sql"
	"os"
	"slices"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestArchiveRangeRepairQueries(t *testing.T) {
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

	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	var libID int64
	if err := tx.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('movie', 'archive-range-repair-check', true)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(context.Background(), `delete from library_items where id = $1`, libID)

	newSelectedRelease := func(title string) int64 {
		var rcID int64
		if err := tx.QueryRowContext(ctx, `
			insert into release_candidates (library_item_id, title, selected)
			values ($1, $2, true)
			returning id`, libID, title).Scan(&rcID); err != nil {
			t.Fatal(err)
		}
		var srID int64
		if err := tx.QueryRowContext(ctx, `
			insert into selected_releases (library_item_id, release_candidate_id)
			values ($1, $2)
			returning id`, libID, rcID).Scan(&srID); err != nil {
			t.Fatal(err)
		}
		return srID
	}

	// A single-volume archive can never hit the reconcileStoreMethodSize
	// ordering bug (it only runs when len(archive.Volumes) > 1), so it must
	// never appear as a repair candidate.
	srSingleVolume := newSelectedRelease("single-volume")
	var archiveSingleVolume int64
	if err := tx.QueryRowContext(ctx, `
		insert into archives (selected_release_id, kind, status)
		values ($1, 'rar', 'supported')
		returning id`, srSingleVolume).Scan(&archiveSingleVolume); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		insert into archive_volumes (archive_id, path, volume_index)
		values ($1, 'single.part01.rar', 0)`, archiveSingleVolume); err != nil {
		t.Fatal(err)
	}

	// A multi-volume archive is exactly the shape the repair sweep targets.
	srMultiVolume := newSelectedRelease("multi-volume")
	var archiveMultiVolume int64
	if err := tx.QueryRowContext(ctx, `
		insert into archives (selected_release_id, kind, status)
		values ($1, 'rar', 'supported')
		returning id`, srMultiVolume).Scan(&archiveMultiVolume); err != nil {
		t.Fatal(err)
	}
	var volumeIDs [2]int64
	for i := 0; i < 2; i++ {
		if err := tx.QueryRowContext(ctx, `
			insert into archive_volumes (archive_id, path, volume_index)
			values ($1, $2, $3)
			returning id`, archiveMultiVolume, "multi.partNN.rar", i).Scan(&volumeIDs[i]); err != nil {
			t.Fatal(err)
		}
	}
	var entryID int64
	if err := tx.QueryRowContext(ctx, `
		insert into archive_entries (archive_id, path, size_bytes, compression_method)
		values ($1, 'a.mkv', 1000, 'm0')
		returning id`, archiveMultiVolume).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		insert into archive_ranges (archive_entry_id, archive_volume_id, entry_offset, archive_offset, length_bytes)
		values ($1, $2, 0, 156, 700),
		       ($1, $3, 700, 0, 300)`, entryID, volumeIDs[0], volumeIDs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, size_bytes, reader_kind)
		values ($1, 'releases/archive-range-repair-check/a.mkv', 'a.mkv', 700, 'stored_rar'),
		       ($1, 'releases/archive-range-repair-check/b.mkv', 'b.mkv', 300, 'stored_rar')`,
		srMultiVolume); err != nil {
		t.Fatal(err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	upperBound, err := db.ArchiveRangeRepairSweepUpperBound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if upperBound < archiveMultiVolume {
		t.Fatalf("sweep upper bound %d excludes archive %d", upperBound, archiveMultiVolume)
	}

	page, err := db.ListArchiveRangeRepairCandidatesPage(ctx, archiveSingleVolume-1, upperBound, 100)
	if err != nil {
		t.Fatal(err)
	}
	sawSingle, sawMulti := false, false
	for _, c := range page {
		if c.ArchiveID == archiveSingleVolume {
			sawSingle = true
		}
		if c.ArchiveID == archiveMultiVolume {
			sawMulti = true
			if c.SelectedReleaseID != srMultiVolume {
				t.Fatalf("candidate selected_release_id = %d, want %d", c.SelectedReleaseID, srMultiVolume)
			}
		}
	}
	if sawSingle {
		t.Fatalf("single-volume archive %d was returned as a repair candidate: %+v", archiveSingleVolume, page)
	}
	if !sawMulti {
		t.Fatalf("multi-volume archive %d was not returned as a repair candidate: %+v", archiveMultiVolume, page)
	}

	exclusivePage, err := db.ListArchiveRangeRepairCandidatesPage(ctx, archiveMultiVolume, upperBound, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range exclusivePage {
		if c.ArchiveID == archiveMultiVolume {
			t.Fatalf("exclusive cursor still returned the just-processed archive: %+v", exclusivePage)
		}
	}

	total, err := db.SumVirtualFileSizeForRelease(ctx, srMultiVolume)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1000 {
		t.Fatalf("SumVirtualFileSizeForRelease = %d, want 1000", total)
	}
	emptyTotal, err := db.SumVirtualFileSizeForRelease(ctx, srSingleVolume)
	if err != nil {
		t.Fatal(err)
	}
	if emptyTotal != 0 {
		t.Fatalf("SumVirtualFileSizeForRelease for a release with no virtual files = %d, want 0", emptyTotal)
	}

	snapshotBefore, err := db.SnapshotArchiveRangesForRelease(ctx, srMultiVolume)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshotBefore) != 2 {
		t.Fatalf("unexpected range snapshot %+v", snapshotBefore)
	}
	snapshotAgain, err := db.SnapshotArchiveRangesForRelease(ctx, srMultiVolume)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(snapshotBefore, snapshotAgain) {
		t.Fatalf("identical range rows produced different snapshots: %+v vs %+v", snapshotBefore, snapshotAgain)
	}

	// Simulate exactly the bug this repair fixes: volume 0 wrongly claims
	// bytes that belong to volume 1, with the release's grand total
	// (entry.size_bytes / sum of length_bytes) completely unchanged --
	// SumVirtualFileSizeForRelease alone would miss this entirely.
	if _, err := sqlDB.ExecContext(ctx, `
		update archive_ranges set length_bytes = 750
		where archive_entry_id = $1 and entry_offset = 0`, entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		update archive_ranges set entry_offset = 750, length_bytes = 250
		where archive_entry_id = $1 and entry_offset = 700`, entryID); err != nil {
		t.Fatal(err)
	}
	snapshotAfter, err := db.SnapshotArchiveRangesForRelease(ctx, srMultiVolume)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(snapshotBefore, snapshotAfter) {
		t.Fatalf("moving 50 bytes from volume 1 to volume 0 was not detected as a change: %+v", snapshotAfter)
	}
	totalAfter, err := db.SumVirtualFileSizeForRelease(ctx, srMultiVolume)
	if err != nil {
		t.Fatal(err)
	}
	if totalAfter != total {
		t.Fatalf("test setup invalid: expected the grand total to stay masked at %d, got %d", total, totalAfter)
	}
}

package database

import (
	"context"
	"database/sql"
	"os"
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
	for i := 0; i < 2; i++ {
		if _, err := tx.ExecContext(ctx, `
			insert into archive_volumes (archive_id, path, volume_index)
			values ($1, $2, $3)`, archiveMultiVolume, "multi.partNN.rar", i); err != nil {
			t.Fatal(err)
		}
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
}

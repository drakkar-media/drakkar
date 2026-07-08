package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestListDeepHealthCandidatesUsesPublishedReleaseSource(t *testing.T) {
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

	db := &DB{SQL: sqlDB}

	var libID int64
	if err := tx.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('tv', 'published-source-check', true)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	var rcOldID, rcCurrentID int64
	if err := tx.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, selected)
		values ($1, 'old published release', true)
		returning id`, libID).Scan(&rcOldID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, selected)
		values ($1, 'current queue release', false)
		returning id`, libID).Scan(&rcCurrentID); err != nil {
		t.Fatal(err)
	}
	var srOldID, srCurrentID int64
	if err := tx.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, libID, rcOldID).Scan(&srOldID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, libID, rcCurrentID).Scan(&srCurrentID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		insert into queue_items (library_item_id, state, idempotency_key, selected_release_id)
		values ($1, 'available', 'published-source-check', $2)`, libID, srCurrentID); err != nil {
		t.Fatal(err)
	}
	var ndOldID int64
	if err := tx.QueryRowContext(ctx, `
		insert into nzb_documents (selected_release_id, file_name)
		values ($1, 'published-source-check.nzb')
		returning id`, srOldID).Scan(&ndOldID); err != nil {
		t.Fatal(err)
	}
	var vfOldID int64
	if err := tx.QueryRowContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, reader_kind)
		values ($1, 'releases/424242/published-source-check.mkv', 'published-source-check.mkv', 'direct_nzb')
		returning id`, srOldID).Scan(&vfOldID); err != nil {
		t.Fatal(err)
	}

	libraryPath := filepath.Join(t.TempDir(), "published-source-check.mkv")
	targetPath := "/mnt/drakkar/vfs/content/releases/424242/published-source-check.mkv"
	if _, err := tx.ExecContext(ctx, `
		insert into symlink_publications (library_item_id, virtual_file_id, library_path, target_path, created_at, last_checked_at, health_ok)
		values ($1, $2, $3, $4, $5, $6, true)`,
		libID, vfOldID, libraryPath, targetPath, time.Now().Add(-time.Hour), time.Now().Add(-time.Minute),
	); err != nil {
		t.Fatal(err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	candidates, err := db.ListDeepHealthCandidates(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range candidates {
		if c.LibraryItemID != libID {
			continue
		}
		if c.SelectedReleaseID != srOldID {
			t.Fatalf("expected published selected_release_id %d, got %d", srOldID, c.SelectedReleaseID)
		}
		if c.NZBDocumentID != ndOldID {
			t.Fatalf("expected published nzb_document_id %d, got %d", ndOldID, c.NZBDocumentID)
		}
		sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)
		return
	}

	t.Fatalf("library item %d not returned by ListDeepHealthCandidates", libID)
}

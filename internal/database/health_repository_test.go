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
		if c.VirtualFileID != vfOldID {
			t.Fatalf("expected published virtual_file_id %d, got %d", vfOldID, c.VirtualFileID)
		}
		upperBound, err := db.DeepHealthSweepUpperBound(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if upperBound < libID {
			t.Fatalf("sweep upper bound %d excludes library item %d", upperBound, libID)
		}
		page, err := db.ListDeepHealthCandidatesPage(ctx, libID-1, libID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) != 1 || page[0].LibraryItemID != libID {
			t.Fatalf("unexpected keyset page: %+v", page)
		}
		nextPage, err := db.ListDeepHealthCandidatesPage(ctx, libID, libID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(nextPage) != 0 {
			t.Fatalf("exclusive cursor returned completed item: %+v", nextPage)
		}
		sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)
		return
	}

	t.Fatalf("library item %d not returned by ListDeepHealthCandidates", libID)
}

// TestTouchHealthCheckTimestampPreservesHealthOK guards the actual fix:
// TouchHealthCheckTimestamp must bump last_checked_at (so
// ListDeepHealthCandidates' "ORDER BY last_checked_at ASC NULLS FIRST"
// stops permanently selecting this row first) without asserting a health_ok
// verdict the calling check couldn't actually confirm -- checked both from
// a NULL starting health_ok (never yet conclusively checked) and from an
// existing true/false value, neither of which this call should disturb.
func TestTouchHealthCheckTimestampPreservesHealthOK(t *testing.T) {
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

	setup := func(t *testing.T, healthOK *bool) (pubID int64, cleanup func()) {
		t.Helper()
		var libID int64
		if err := sqlDB.QueryRowContext(ctx, `
			insert into library_items (media_type, title, available)
			values ('movie', 'touch-health-check-check', true)
			returning id`).Scan(&libID); err != nil {
			t.Fatal(err)
		}
		var rcID int64
		if err := sqlDB.QueryRowContext(ctx, `
			insert into release_candidates (library_item_id, title)
			values ($1, 'touch-health-check-check')
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
			insert into virtual_files (selected_release_id, path, file_name, reader_kind)
			values ($1, 'releases/0/touch-health.mkv', 'touch-health.mkv', 'direct_nzb')
			returning id`, srID).Scan(&vfID); err != nil {
			t.Fatal(err)
		}
		if err := sqlDB.QueryRowContext(ctx, `
			insert into symlink_publications (library_item_id, virtual_file_id, library_path, target_path, health_ok)
			values ($1, $2, '/tmp/touch-health.mkv', '/mnt/drakkar/vfs/content/releases/0/touch-health.mkv', $3)
			returning id`, libID, vfID, healthOK).Scan(&pubID); err != nil {
			t.Fatal(err)
		}
		return pubID, func() {
			sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)
		}
	}

	t.Run("preserves NULL health_ok", func(t *testing.T) {
		pubID, cleanup := setup(t, nil)
		defer cleanup()

		if err := db.TouchHealthCheckTimestamp(ctx, pubID); err != nil {
			t.Fatal(err)
		}

		var healthOK sql.NullBool
		var lastCheckedAt sql.NullTime
		if err := sqlDB.QueryRowContext(ctx, `select health_ok, last_checked_at from symlink_publications where id = $1`, pubID).Scan(&healthOK, &lastCheckedAt); err != nil {
			t.Fatal(err)
		}
		if healthOK.Valid {
			t.Fatalf("expected health_ok to remain NULL, got %v", healthOK.Bool)
		}
		if !lastCheckedAt.Valid || time.Since(lastCheckedAt.Time) > time.Minute {
			t.Fatalf("expected last_checked_at to be bumped to now, got %+v", lastCheckedAt)
		}
	})

	t.Run("preserves existing true health_ok", func(t *testing.T) {
		trueVal := true
		pubID, cleanup := setup(t, &trueVal)
		defer cleanup()

		if err := db.TouchHealthCheckTimestamp(ctx, pubID); err != nil {
			t.Fatal(err)
		}

		var healthOK sql.NullBool
		if err := sqlDB.QueryRowContext(ctx, `select health_ok from symlink_publications where id = $1`, pubID).Scan(&healthOK); err != nil {
			t.Fatal(err)
		}
		if !healthOK.Valid || !healthOK.Bool {
			t.Fatalf("expected health_ok to remain true, got %+v", healthOK)
		}
	})
}

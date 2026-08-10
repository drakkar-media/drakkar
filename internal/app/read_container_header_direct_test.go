package app

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/drakkar-media/drakkar/internal/database"
)

// TestReadContainerHeaderDirectReadsFromBackendNotFilesystem guards the
// 2026-08-11 production fix: the periodic deep health check used to validate
// a published file's container header via os.Open on the real host symlink
// path -- the same kernel-FUSE-mount -> rclone -> WebDAV round trip a real
// player uses, which registers a real tracked read-ahead session and
// competes with actual playback for the same connection budget. Confirmed
// live: two entirely unrelated files (one health-check candidate, one real
// concurrent playback stream) hit hard EOF from rclone at the same instant.
// readContainerHeaderDirect must validate the same magic bytes by reading
// straight from the backend (db.OpenVirtualMediaFile), with no filesystem/
// FUSE/rclone involvement and no session registered at all.
func TestReadContainerHeaderDirectReadsFromBackendNotFilesystem(t *testing.T) {
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
	db := &database.DB{SQL: sqlDB}

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('movie', 'container-header-direct-check', true)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, selected)
		values ($1, 'container-header-direct-check release', true)
		returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}

	t.Run("valid MKV header reads successfully", func(t *testing.T) {
		mkvHeader := []byte{0x1a, 0x45, 0xdf, 0xa3, 0, 0, 0, 0, 0, 0, 0, 0}
		var vfID int64
		if err := sqlDB.QueryRowContext(ctx, `
			insert into virtual_files (selected_release_id, path, file_name, size_bytes, reader_kind, inline_bytes)
			values ($1, 'valid.mkv', 'valid.mkv', $2, 'inline', $3)
			returning id`, srID, len(mkvHeader), mkvHeader).Scan(&vfID); err != nil {
			t.Fatal(err)
		}
		defer sqlDB.ExecContext(ctx, `delete from virtual_files where id = $1`, vfID)

		if err := readContainerHeaderDirect(ctx, db, vfID); err != nil {
			t.Fatalf("expected a valid MKV header to pass, got: %v", err)
		}
	})

	t.Run("garbage content is definitively rejected, not treated as transient", func(t *testing.T) {
		garbage := []byte("this is definitely not a video file at all")
		var vfID int64
		if err := sqlDB.QueryRowContext(ctx, `
			insert into virtual_files (selected_release_id, path, file_name, size_bytes, reader_kind, inline_bytes)
			values ($1, 'garbage.mkv', 'garbage.mkv', $2, 'inline', $3)
			returning id`, srID, len(garbage), garbage).Scan(&vfID); err != nil {
			t.Fatal(err)
		}
		defer sqlDB.ExecContext(ctx, `delete from virtual_files where id = $1`, vfID)

		err := readContainerHeaderDirect(ctx, db, vfID)
		if err == nil {
			t.Fatal("expected garbage content to be rejected")
		}
		if isTransientHealthCheckErr(err) {
			t.Fatalf("expected a definitive rejection (real bytes read, wrong magic number), not a transient/retryable error, got: %v", err)
		}
	})
}

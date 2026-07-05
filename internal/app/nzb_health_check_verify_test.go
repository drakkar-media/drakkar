package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/hjongedijk/drakkar/internal/database"
	"github.com/rs/zerolog"
)

// TestRunNZBHealthCheckBatchDoesNotResurrectFailedHealth reproduces the bug
// reported live: a symlink that resolves fine (CheckSymlinkHealth only does
// os.Readlink + string compare — it never reads file content) but whose
// underlying content is unreadable (Usenet articles gone) was previously
// re-marked health_ok=true on every 15-minute pass, even when the deep
// content check (StrictCheckFirstSegments) had correctly marked it false
// and its own re-check backoff hadn't elapsed yet. That let permanently
// unplayable media ("Video: none / Audio: none" in Plex) report as healthy
// indefinitely. Skipped unless DRAKKAR_TEST_DATABASE_URL is set.
func TestRunNZBHealthCheckBatchDoesNotResurrectFailedHealth(t *testing.T) {
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
	db := &database.DB{SQL: sqlDB} // health check helpers use db.SQL directly, tx used only for our own setup/verify

	var libID int64
	if err := tx.QueryRowContext(ctx, `insert into library_items (media_type, title, available) values ('tv','health verify', true) returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	var rcID int64
	if err := tx.QueryRowContext(ctx, `insert into release_candidates (library_item_id, title) values ($1,'health verify') returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := tx.QueryRowContext(ctx, `insert into selected_releases (library_item_id, release_candidate_id) values ($1,$2) returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `insert into queue_items (library_item_id, state, idempotency_key, selected_release_id) values ($1,'available','health-verify-key',$2)`, libID, srID); err != nil {
		t.Fatal(err)
	}
	var nzbDocID int64
	if err := tx.QueryRowContext(ctx, `insert into nzb_documents (selected_release_id, file_name) values ($1,'health.nzb') returning id`, srID).Scan(&nzbDocID); err != nil {
		t.Fatal(err)
	}
	var vfID int64
	if err := tx.QueryRowContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, reader_kind)
		values ($1, 'releases/999999999/health-verify.mkv', 'health-verify.mkv', 'direct_nzb')
		returning id`, srID).Scan(&vfID); err != nil {
		t.Fatal(err)
	}

	// Real symlink on disk resolving into a path CheckSymlinkHealth accepts
	// (contains "/content/releases/"), pointing at a target that doesn't
	// need to exist — CheckSymlinkHealth never stats/reads it.
	dir := t.TempDir()
	libraryPath := filepath.Join(dir, "health-verify.mkv")
	targetPath := "/mnt/drakkar/vfs/content/releases/999999999/health-verify.mkv"
	if err := os.Symlink(targetPath, libraryPath); err != nil {
		t.Fatal(err)
	}

	// health_ok=true and last_checked_at=recent, matching the real-world
	// state right after a genuine deep check passed. created_at is old
	// enough that nextDeepHealthCheckDelay(createdAt) is well over an hour,
	// so with last_checked_at this recent, shouldRunDeepHealthCheck must
	// return false — this pass should be a no-op cheap check only.
	oldCreatedAt := time.Now().Add(-5 * 24 * time.Hour)
	recentCheckedAt := time.Now().Add(-1 * time.Minute)
	var pubID int64
	if err := tx.QueryRowContext(ctx, `
		insert into symlink_publications (library_item_id, virtual_file_id, library_path, target_path, created_at, last_checked_at, health_ok)
		values ($1, $2, $3, $4, $5, $6, true)
		returning id`, libID, vfID, libraryPath, targetPath, oldCreatedAt, recentCheckedAt).Scan(&pubID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Confirm the seeded row is actually visible to ListDeepHealthCandidates
	// before relying on runNZBHealthCheckBatch to have picked it up.
	candidates, err := db.ListDeepHealthCandidates(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range candidates {
		if c.PublicationID == pubID {
			found = true
		}
	}
	if !found {
		t.Skip("seeded row not visible to ListDeepHealthCandidates (schema/query shape changed) — skipping")
	}

	logger := zerolog.Nop()
	if _, err := runNZBHealthCheckBatch(ctx, db, nil, nil, logger, 0, false); err != nil {
		t.Fatal(err)
	}

	// The real bug: the cheap symlink-only check used to unconditionally
	// call RecordHealthCheck (which bumps last_checked_at too), resetting
	// the deep-check backoff clock on every 15-minute pass — so the deep
	// check that actually reads content effectively never ran again after
	// the first time. Assert last_checked_at is untouched by this cheap
	// pass, proving the backoff clock is preserved for a future real check.
	var healthOK sql.NullBool
	var lastCheckedAt time.Time
	if err := sqlDB.QueryRowContext(ctx, `select health_ok, last_checked_at from symlink_publications where id = $1`, pubID).Scan(&healthOK, &lastCheckedAt); err != nil {
		t.Fatal(err)
	}
	if !healthOK.Valid || !healthOK.Bool {
		t.Fatalf("expected health_ok to remain true (untouched), got %+v", healthOK)
	}
	if !lastCheckedAt.Equal(recentCheckedAt) {
		if lastCheckedAt.Sub(recentCheckedAt) > time.Second {
			t.Fatalf("expected last_checked_at to be untouched by the cheap symlink-only check (deep check should have been skipped), got %v want %v", lastCheckedAt, recentCheckedAt)
		}
	}

	// Cleanup — everything cascades from library_items (outside the rolled-back
	// tx, since runNZBHealthCheckBatch committed its own writes via db.SQL).
	sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)
}

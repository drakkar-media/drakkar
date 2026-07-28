package catalog

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/drakkar-media/drakkar/internal/database"
)

// TestRecentlyAddedBlanksNonFetchQueueState guards a real UX bug reported
// live (2026-07-28): a background upgrade check queued against an
// already-available, just-published movie left its latest queue_items row
// in a pre-download state (e.g. "selected"), and the frontend's shared
// itemStatus() helper deliberately lets an in-progress queue state override
// availability -- so a fully watchable, just-added title displayed as
// "Missing" in the one dashboard rail whose entire point is "this is ready
// to watch". recentlyAdded() must blank any queue state that isn't an
// actual in-flight fetch, so the frontend falls back to availability.
func TestRecentlyAddedBlanksNonFetchQueueState(t *testing.T) {
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
	svc := NewService(db, nil)

	var movieID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into movies (title) values ('recently-added-upgrade-check-movie') returning id`,
	).Scan(&movieID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from movies where id = $1`, movieID)

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, movie_id, available)
		values ('movie', 'recently-added-upgrade-check-movie', $1, true)
		returning id`, movieID,
	).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	if _, err := sqlDB.ExecContext(ctx, `
		with rc as (
			insert into release_candidates (library_item_id, title) values ($1, 'placeholder') returning id
		), sr as (
			insert into selected_releases (library_item_id, release_candidate_id)
			select $1, rc.id from rc returning id
		), vf as (
			insert into virtual_files (selected_release_id, path, file_name, reader_kind)
			select sr.id, 'placeholder.mkv', 'placeholder.mkv', 'inline' from sr returning id
		)
		insert into symlink_publications (library_item_id, virtual_file_id, library_path, target_path)
		select $1, vf.id, '/test/recently-added-upgrade-check-movie.mkv', '/test/target.mkv' from vf`, libID,
	); err != nil {
		t.Fatal(err)
	}

	// Simulate a background upgrade check that queued a fresh "selected"
	// queue_items row against this already-available item -- this is the
	// exact scenario that caused the bug.
	if _, err := sqlDB.ExecContext(ctx, `
		insert into queue_items (library_item_id, state, idempotency_key)
		values ($1, 'selected', $2)`, libID, "test-upgrade-check-key",
	); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from queue_items where library_item_id = $1`, libID)

	home, err := svc.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *MediaCard
	for i := range home.RecentlyAdded {
		if home.RecentlyAdded[i].ID == libID {
			found = &home.RecentlyAdded[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected library item %d in Recently Added, got %d entries", libID, len(home.RecentlyAdded))
	}
	if found.QueueState != "" {
		t.Errorf("expected QueueState blanked (available item, non-fetch queue state), got %q", found.QueueState)
	}
	if !found.Available {
		t.Error("expected Available to remain true")
	}
}

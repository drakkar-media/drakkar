package database

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestResetLibraryItemStateCreatesQueueItemWhenNoneExists guards the
// 2026-08-11 production fix: a season-pack-fulfilled episode never gets its
// own queue_items row (see FulfillEpisodeLibraryItem), so ResetLibraryItemState's
// lookup hit sql.ErrNoRows and silently no-op'd -- library_items.available
// stayed stuck at true forever, with nothing left to ever reset it. The
// Health page's "Reset Orphaned Available" action correctly re-selected such
// an item as unrecoverable on every single pass but could never actually fix
// it. Confirmed live on "The Boys" library item 53193.
func TestResetLibraryItemStateCreatesQueueItemWhenNoneExists(t *testing.T) {
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

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('episode', 'reset-no-queue-item-check', true)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var count int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from queue_items where library_item_id = $1`, libID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected the fixture to start with no queue_items row, got %d", count)
	}

	if err := db.ResetLibraryItemState(ctx, libID); err != nil {
		t.Fatal(err)
	}

	var available bool
	if err := sqlDB.QueryRowContext(ctx, `select available from library_items where id = $1`, libID).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if available {
		t.Fatal("expected available to be reset to false")
	}

	var state string
	if err := sqlDB.QueryRowContext(ctx, `select state from queue_items where library_item_id = $1`, libID).Scan(&state); err != nil {
		t.Fatalf("expected a queue_items row to have been created: %v", err)
	}
	if state != string(QueueRequested) {
		t.Fatalf("expected state 'requested', got %q", state)
	}
}

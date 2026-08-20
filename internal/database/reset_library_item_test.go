package database

import (
	"context"
	"database/sql"
	"fmt"
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

// TestResetLibraryItemStateClearsSearchCooldowns guards a live production
// gap (confirmed 2026-08-20): ResetLibraryItemState reset the selection and
// queue state, but left last_searched_at, consecutive_failure_searches, and
// dispatch_attempt_count/dispatch_backoff_until untouched on an existing
// queue_items row. An item that had escalated consecutive_failure_searches
// to 10+ (the 7-day cooldown tier used by both
// ListPendingLibrarySearchTargets and ListFailedQueueRetryTargets) with a
// recent last_searched_at stayed silently throttled for up to 7 days after
// a user-triggered reset -- despite both of those functions' own doc
// comments promising the item re-enters the search cycle "as if newly
// added". Confirmed via direct code reading, not yet observed live in this
// exact form (unlike the sibling queue_items-row-missing gap this file's
// other test guards), but the mechanism is the same class of bug.
func TestResetLibraryItemStateClearsSearchCooldowns(t *testing.T) {
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
		values ('episode', 'reset-clears-cooldowns-check', true)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	if _, err := sqlDB.ExecContext(ctx, `
		insert into queue_items (
			library_item_id, state, idempotency_key,
			last_searched_at, consecutive_failure_searches,
			dispatch_attempt_count, dispatch_backoff_until
		) values ($1, $2, $3, now(), 12, 30, now() + interval '24 hours')`,
		libID, QueueFailed, fmt.Sprintf("reset-cooldowns-check-%d", libID),
	); err != nil {
		t.Fatal(err)
	}

	if err := db.ResetLibraryItemState(ctx, libID); err != nil {
		t.Fatal(err)
	}

	var (
		lastSearchedAt                    sql.NullTime
		failureSearches, dispatchAttempts int
		backoffUntil                      sql.NullTime
	)
	if err := sqlDB.QueryRowContext(ctx, `
		select last_searched_at, consecutive_failure_searches, dispatch_attempt_count, dispatch_backoff_until
		from queue_items where library_item_id = $1`, libID,
	).Scan(&lastSearchedAt, &failureSearches, &dispatchAttempts, &backoffUntil); err != nil {
		t.Fatal(err)
	}
	if lastSearchedAt.Valid {
		t.Fatalf("expected last_searched_at to be cleared, got %v", lastSearchedAt.Time)
	}
	if failureSearches != 0 {
		t.Fatalf("expected consecutive_failure_searches reset to 0, got %d", failureSearches)
	}
	if dispatchAttempts != 0 {
		t.Fatalf("expected dispatch_attempt_count reset to 0, got %d", dispatchAttempts)
	}
	if backoffUntil.Valid {
		t.Fatalf("expected dispatch_backoff_until to be cleared, got %v", backoffUntil.Time)
	}
}

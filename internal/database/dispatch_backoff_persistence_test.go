package database

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestRecordDispatchAttemptPersistsAcrossListPendingLibrarySearchTargets
// guards the 2026-08-11 fix: the passive-resume dispatch sweep's escalating
// per-item backoff used to live only in an in-memory map on workflow.Service,
// reset to zero by every process restart -- confirmed live, a small cluster
// of library items with hundreds of rejected release_candidates each kept
// getting re-dispatched every ~1-2 minutes indefinitely (flooding the
// indexer's download history) because that day's several redeploys kept
// resetting the counter before it could climb past its first couple of
// tiers. RecordDispatchAttempt must durably persist
// dispatch_attempt_count/dispatch_backoff_until, and a fresh
// ListPendingLibrarySearchTargets call (as a new process, after a restart,
// would make) must read them back.
func TestRecordDispatchAttemptPersistsAcrossListPendingLibrarySearchTargets(t *testing.T) {
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
		values ('movie', 'dispatch-backoff-persistence-check', false)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)
	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name, selected)
		values ($1, 'Dispatch Backoff Persistence Check', 'http://example/dispatch-backoff-persistence', 'test-indexer', true)
		returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into queue_items (library_item_id, state, idempotency_key, selected_release_id)
		values ($1, 'selected', 'dispatch-backoff-persistence-check', $2)`, libID, srID,
	); err != nil {
		t.Fatal(err)
	}

	targets, err := db.ListPendingLibrarySearchTargets(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := findPendingTarget(targets, libID)
	if found == nil {
		t.Fatal("expected the freshly-inserted selected item to appear as a pending target")
	}
	if found.DispatchAttemptCount != 0 || found.DispatchBackoffUntil != nil {
		t.Fatalf("expected a brand-new item to start with no recorded attempts, got %+v", found)
	}

	until := time.Now().Add(6 * time.Hour).Truncate(time.Millisecond)
	if err := db.RecordDispatchAttempt(ctx, libID, 12, until); err != nil {
		t.Fatal(err)
	}

	// Fresh read -- simulates a new process (e.g. after a restart) picking
	// this item back up, not a cached in-memory value.
	targets2, err := db.ListPendingLibrarySearchTargets(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	found2 := findPendingTarget(targets2, libID)
	if found2 == nil {
		t.Fatal("expected the item to still appear as a pending target after recording an attempt")
	}
	if found2.DispatchAttemptCount != 12 {
		t.Fatalf("expected the persisted attempt count 12, got %d", found2.DispatchAttemptCount)
	}
	if found2.DispatchBackoffUntil == nil || !found2.DispatchBackoffUntil.Equal(until) {
		t.Fatalf("expected the persisted backoff-until %v, got %+v", until, found2.DispatchBackoffUntil)
	}
}

func findPendingTarget(targets []PendingLibrarySearchTarget, libraryItemID int64) *PendingLibrarySearchTarget {
	for i := range targets {
		if targets[i].LibraryItemID == libraryItemID {
			return &targets[i]
		}
	}
	return nil
}

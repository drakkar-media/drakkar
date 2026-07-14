package database

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestActivateReleaseCandidateSerializesConcurrentSelection guards against a
// real production bug: activateReleaseCandidate (shared by
// SelectReleaseCandidate and promoteRetryCandidate) deleted-then-inserted
// selected_releases rows with no row lock, so two concurrent callers for the
// same library item (e.g. a manual retry racing the scheduled
// queue_housekeeping retry pass) could each see zero existing rows before
// either committed, and both insert -- leaving two live selected_releases
// rows for the identical release_candidate. Confirmed live across 3,741
// library items, each a wasted duplicate NZB fetch from the indexer.
func TestActivateReleaseCandidateSerializesConcurrentSelection(t *testing.T) {
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
		values ('tv', 'race-condition-check', false)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	if _, err := sqlDB.ExecContext(ctx, `
		insert into queue_items (library_item_id, state, idempotency_key)
		values ($1, 'requested', 'race-condition-check')`, libID); err != nil {
		t.Fatal(err)
	}

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'Race Test Release', 'http://example/race', 'test-indexer')
		returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}

	const attempts = 8
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = db.SelectReleaseCandidate(ctx, rcID)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d returned error: %v", i, err)
		}
	}

	var count int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from selected_releases where library_item_id = $1`, libID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 selected_releases row to survive %d concurrent selections of the same candidate, got %d", attempts, count)
	}

	var rcSelectedCount int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from release_candidates where library_item_id = $1 and selected = true`, libID).Scan(&rcSelectedCount); err != nil {
		t.Fatal(err)
	}
	if rcSelectedCount != 1 {
		t.Fatalf("expected exactly 1 release_candidates row marked selected, got %d", rcSelectedCount)
	}
}

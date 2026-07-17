package database

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

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

// setupRaceTestLibraryItem inserts a library item + queue_items row for the
// given media type/title and returns the library item's id. The caller is
// responsible for deleting it (cascades clean up everything else).
func setupRaceTestLibraryItem(t *testing.T, ctx context.Context, sqlDB *sql.DB, title, state string) int64 {
	t.Helper()
	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('tv', $1, false)
		returning id`, title).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into queue_items (library_item_id, state, idempotency_key)
		values ($1, $2, $3)`, libID, state, title,
	); err != nil {
		t.Fatal(err)
	}
	return libID
}

// TestFailSelectedReleaseAndPromoteNextSerializesConcurrentFailure guards
// against a real gap found during the 2026-07-17 exhaustive audit: two
// concurrent callers failing the SAME originally-selected release (e.g. a
// background download-failure handler racing a manual reject/skip on that
// same release) each read a stale releaseCandidateID/externalURL before
// either transaction committed. Without a post-lock recheck, the loser
// would proceed on that stale premise and independently re-derive and
// re-promote the same "next" candidate the winner already picked --
// producing a second selected_releases row for it, the same defect class
// fixed for activateReleaseCandidate in c4ead9f, via a call-site pair that
// fix's lock ordering alone didn't cover.
func TestFailSelectedReleaseAndPromoteNextSerializesConcurrentFailure(t *testing.T) {
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

	libID := setupRaceTestLibraryItem(t, ctx, sqlDB, "fail-race-check", "selected")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var rcA, rcB int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name, selected)
		values ($1, 'Fail Race Original', 'http://example/fail-race-a', 'test-indexer', true)
		returning id`, libID).Scan(&rcA); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'Fail Race Alternative', 'http://example/fail-race-b', 'test-indexer')
		returning id`, libID).Scan(&rcB); err != nil {
		t.Fatal(err)
	}

	var selectedReleaseID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, libID, rcA).Scan(&selectedReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `update queue_items set selected_release_id = $2 where library_item_id = $1`, libID, selectedReleaseID); err != nil {
		t.Fatal(err)
	}

	const attempts = 8
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = db.FailSelectedReleaseAndPromoteNext(ctx, selectedReleaseID, "test_concurrent_failure")
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
		t.Fatalf("expected exactly 1 selected_releases row to survive %d concurrent failures of the same release, got %d", attempts, count)
	}

	var rcSelectedCount int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from release_candidates where library_item_id = $1 and selected = true`, libID).Scan(&rcSelectedCount); err != nil {
		t.Fatal(err)
	}
	if rcSelectedCount != 1 {
		t.Fatalf("expected exactly 1 release_candidates row marked selected, got %d", rcSelectedCount)
	}

	var survivingCandidateID int64
	if err := sqlDB.QueryRowContext(ctx, `select release_candidate_id from selected_releases where library_item_id = $1`, libID).Scan(&survivingCandidateID); err != nil {
		t.Fatal(err)
	}
	if survivingCandidateID != rcB {
		t.Fatalf("expected the surviving selection to be the promoted alternative candidate (%d), got %d", rcB, survivingCandidateID)
	}
}

// TestRejectReleaseCandidateSerializesConcurrentRejection is the same
// regression as above, exercised through RejectReleaseCandidate instead of
// FailSelectedReleaseAndPromoteNext (e.g. a manual reject racing a
// concurrent background failure on the same currently-selected release).
func TestRejectReleaseCandidateSerializesConcurrentRejection(t *testing.T) {
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

	libID := setupRaceTestLibraryItem(t, ctx, sqlDB, "reject-race-check", "selected")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var rcA, rcB int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name, selected)
		values ($1, 'Reject Race Original', 'http://example/reject-race-a', 'test-indexer', true)
		returning id`, libID).Scan(&rcA); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'Reject Race Alternative', 'http://example/reject-race-b', 'test-indexer')
		returning id`, libID).Scan(&rcB); err != nil {
		t.Fatal(err)
	}

	var selectedReleaseID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, libID, rcA).Scan(&selectedReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `update queue_items set selected_release_id = $2 where library_item_id = $1`, libID, selectedReleaseID); err != nil {
		t.Fatal(err)
	}

	const attempts = 8
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = db.RejectReleaseCandidate(ctx, rcA, "test_concurrent_reject")
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
		t.Fatalf("expected exactly 1 selected_releases row to survive %d concurrent rejections of the same release, got %d", attempts, count)
	}

	var rcSelectedCount int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from release_candidates where library_item_id = $1 and selected = true`, libID).Scan(&rcSelectedCount); err != nil {
		t.Fatal(err)
	}
	if rcSelectedCount != 1 {
		t.Fatalf("expected exactly 1 release_candidates row marked selected, got %d", rcSelectedCount)
	}

	var survivingCandidateID int64
	if err := sqlDB.QueryRowContext(ctx, `select release_candidate_id from selected_releases where library_item_id = $1`, libID).Scan(&survivingCandidateID); err != nil {
		t.Fatal(err)
	}
	if survivingCandidateID != rcB {
		t.Fatalf("expected the surviving selection to be the promoted alternative candidate (%d), got %d", rcB, survivingCandidateID)
	}
}

// TestPromoteRetryCandidateIgnoresStaleDecisionAcrossConcurrentReject guards
// against the promoteRetryCandidate lock-ordering gap found in the
// 2026-07-17 audit: it used to run its "pick best candidate" decision query
// before acquiring lockLibraryItemQueueRow -- the one function in this file
// that didn't follow the lock-then-decide pattern every sibling selection
// function uses (PromoteExistingCandidate, RejectReleaseCandidate,
// FailSelectedReleaseAndPromoteNext, ReplaceSearchCandidates).
//
// Because activateReleaseCandidate (which promoteRetryCandidate calls with
// its decided candidate) already acquires the same row lock internally,
// simply asserting that PromoteBestRetryCandidate blocks while a concurrent
// transaction holds the lock does not distinguish old from new code -- that
// was already true either way. The actual defect is that the OLD code
// decided *before* waiting on the lock, so it could activate a candidate
// that a concurrent transaction invalidated (e.g. rejected) while
// PromoteBestRetryCandidate was blocked waiting to acquire the lock inside
// activateReleaseCandidate. This test reproduces exactly that: the only
// eligible candidate is rejected by a concurrent transaction that commits
// while PromoteBestRetryCandidate is in flight, and asserts nothing gets
// selected/activated as a result.
func TestPromoteRetryCandidateIgnoresStaleDecisionAcrossConcurrentReject(t *testing.T) {
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

	libID := setupRaceTestLibraryItem(t, ctx, sqlDB, "promote-retry-stale-check", "failed")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'Promote Retry Stale Candidate', 'http://example/promote-retry-stale', 'test-indexer')
		returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}

	holderTx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := lockLibraryItemQueueRow(ctx, holderTx, libID); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := db.PromoteBestRetryCandidate(ctx, libID)
		done <- err
	}()

	// Give the goroutine a moment to reach (and, pre-fix, race past) its
	// decision point before we invalidate the only eligible candidate.
	time.Sleep(150 * time.Millisecond)

	if _, err := holderTx.ExecContext(ctx, `
		update release_candidates set rejected = true, reject_reason = 'concurrent_reject' where id = $1`, rcID,
	); err != nil {
		t.Fatal(err)
	}
	if err := holderTx.Commit(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("PromoteBestRetryCandidate returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PromoteBestRetryCandidate did not complete after the concurrent lock was released")
	}

	var count int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from selected_releases where library_item_id = $1`, libID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected no selected_releases row -- the only candidate was rejected by a concurrent transaction before promoteRetryCandidate's decision should have been made, so it must not be selected/activated; got %d rows", count)
	}
}

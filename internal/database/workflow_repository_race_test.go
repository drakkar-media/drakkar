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

// TestMarkLibrarySearchFailedIsAtomicAcrossBothStatements guards against a
// gap found in the 2026-07-19 follow-up audit: MarkLibrarySearchFailed used
// to run as two bare ExecContext calls with no explicit transaction at all.
// Each bare statement autocommits (and releases its implicit row lock)
// the instant it completes, so there was a real window between the UPDATE
// and the DELETE where nothing serialized this function against a
// concurrent locked writer (e.g. FailSelectedReleaseAndPromoteNext
// installing a fresh selection after an unrelated fetch failure): that
// writer could commit a brand new selected_releases row + queue_items
// pointer to it inside the gap, and the DELETE would then remove that
// fresh row. queue_items.selected_release_id has an ON DELETE SET NULL FK
// to selected_releases, so Postgres itself prevents a literally-dangling
// pointer -- but nothing prevents queue_items.state from being left at
// 'selected' (set by the concurrent writer) with selected_release_id
// silently nulled back out from under it (by the FK's own cascade once the
// legacy shape's DELETE runs) -- a queue item claiming to have a live
// selection with nothing behind it, silently discarding the concurrent
// writer's legitimate selection with no error anywhere.
//
// A single bare UPDATE still naturally blocks on another transaction's held
// row lock (Postgres locks rows on any write, transactional or not), so a
// test that merely holds the lock and calls MarkLibrarySearchFailed cannot
// distinguish the old shape from the fix -- both block identically on the
// first statement. The actual gap is *between* the two statements, which
// requires precisely timing a third actor into a nanosecond-scale window
// that goroutine scheduling alone won't reliably hit. So this test first
// reproduces the old two-bare-statement shape directly (widening its gap
// with a short sleep, the same proven technique used elsewhere in this file
// for this class of bug) to confirm the window is real and would corrupt
// state, then runs the identical concurrent-writer harness against the
// actual, fixed MarkLibrarySearchFailed and confirms no corruption results.
func TestMarkLibrarySearchFailedIsAtomicAcrossBothStatements(t *testing.T) {
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

	// concurrentWriterInstallsFreshSelection waits a moment (targeting the
	// legacy shape's widened gap), then installs a brand new
	// selected_releases row for libID and points queue_items at it --
	// exactly what FailSelectedReleaseAndPromoteNext does after a fetch
	// failure promotes the next candidate.
	concurrentWriterInstallsFreshSelection := func(libID, rcID int64, startedInstalling chan<- struct{}) {
		time.Sleep(75 * time.Millisecond)
		var freshSRID int64
		if err := sqlDB.QueryRowContext(ctx, `
			insert into selected_releases (library_item_id, release_candidate_id)
			values ($1, $2)
			returning id`, libID, rcID,
		).Scan(&freshSRID); err != nil {
			t.Errorf("concurrent writer: insert selected_releases: %v", err)
			close(startedInstalling)
			return
		}
		if _, err := sqlDB.ExecContext(ctx, `
			update queue_items set selected_release_id = $2, state = 'selected', updated_at = now()
			where library_item_id = $1`, libID, freshSRID,
		); err != nil {
			t.Errorf("concurrent writer: update queue_items: %v", err)
		}
		close(startedInstalling)
	}

	t.Run("legacy shape corrupts state", func(t *testing.T) {
		libID := setupRaceTestLibraryItem(t, ctx, sqlDB, "mark-search-failed-legacy-shape", "selected")
		defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)
		var rcID int64
		if err := sqlDB.QueryRowContext(ctx, `
			insert into release_candidates (library_item_id, title, external_url, indexer_name)
			values ($1, 'Legacy Shape Fresh Candidate', 'http://example/legacy-shape-fresh', 'test-indexer')
			returning id`, libID,
		).Scan(&rcID); err != nil {
			t.Fatal(err)
		}

		installed := make(chan struct{})
		go concurrentWriterInstallsFreshSelection(libID, rcID, installed)

		// Replicates the pre-fix MarkLibrarySearchFailed body exactly: two
		// bare, separately-autocommitted statements with a gap between them
		// (widened here so the concurrent writer above reliably lands in it;
		// the real pre-fix gap was nanosecond-scale but structurally identical).
		if _, err := sqlDB.ExecContext(ctx, `
			update queue_items
			set state = $2, failure_reason = $3, selected_release_id = null, updated_at = now()
			where library_item_id = $1`, libID, QueueFailed, "test_search_failed",
		); err != nil {
			t.Fatal(err)
		}
		time.Sleep(150 * time.Millisecond) // the gap the fix closes
		if _, err := sqlDB.ExecContext(ctx, `
			delete from selected_releases where library_item_id = $1`, libID,
		); err != nil {
			t.Fatal(err)
		}
		<-installed

		var srCount int
		if err := sqlDB.QueryRowContext(ctx, `select count(*) from selected_releases where library_item_id = $1`, libID).Scan(&srCount); err != nil {
			t.Fatal(err)
		}
		var state string
		var pointer sql.NullInt64
		if err := sqlDB.QueryRowContext(ctx, `select state, selected_release_id from queue_items where library_item_id = $1`, libID).Scan(&state, &pointer); err != nil {
			t.Fatal(err)
		}
		// Expected corruption: the concurrent writer's insert+queue_items
		// update (state='selected', pointer=freshSRID) landed in the gap
		// between the legacy shape's two statements; the second statement
		// then deleted that fresh row. queue_items.selected_release_id has
		// an ON DELETE SET NULL FK, so Postgres itself nulls the pointer as
		// part of that same DELETE -- but nothing touches queue_items.state
		// again, leaving it stuck at 'selected' with no selected release
		// behind it: the legitimate concurrent selection is gone without a
		// trace, and the queue item is left in a self-contradictory state.
		if srCount != 0 {
			t.Fatalf("expected the concurrent writer's row to have been deleted by the legacy shape's second statement, got %d rows -- harness timing didn't land in the gap as intended", srCount)
		}
		if pointer.Valid {
			t.Fatalf("expected the FK's ON DELETE SET NULL to have cleared the pointer, got %v -- harness timing didn't land in the gap as intended", pointer.Int64)
		}
		if state != "selected" {
			t.Fatalf("expected queue_items.state to still (wrongly) read 'selected' with no selection behind it, got %q -- harness timing didn't land in the gap as intended", state)
		}
		// Confirmed: queue_items ends up with state='selected' and
		// selected_release_id=NULL -- a self-contradictory state that
		// silently discarded the concurrent writer's legitimate selection,
		// exactly the corruption the fix (wrapping both statements in one
		// locked transaction) exists to prevent.
	})

	t.Run("fixed MarkLibrarySearchFailed stays consistent", func(t *testing.T) {
		libID := setupRaceTestLibraryItem(t, ctx, sqlDB, "mark-search-failed-fixed-shape", "selected")
		defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)
		var rcID int64
		if err := sqlDB.QueryRowContext(ctx, `
			insert into release_candidates (library_item_id, title, external_url, indexer_name)
			values ($1, 'Fixed Shape Fresh Candidate', 'http://example/fixed-shape-fresh', 'test-indexer')
			returning id`, libID,
		).Scan(&rcID); err != nil {
			t.Fatal(err)
		}

		installed := make(chan struct{})
		go concurrentWriterInstallsFreshSelection(libID, rcID, installed)

		if err := db.MarkLibrarySearchFailed(ctx, libID, "test_search_failed"); err != nil {
			t.Fatal(err)
		}
		<-installed

		// Whichever operation's single atomic transaction commits last wins
		// outright -- either outcome must be fully self-consistent, unlike
		// the legacy shape's "state says selected, but nothing is" corruption.
		var srCount int
		if err := sqlDB.QueryRowContext(ctx, `select count(*) from selected_releases where library_item_id = $1`, libID).Scan(&srCount); err != nil {
			t.Fatal(err)
		}
		var state string
		var pointer sql.NullInt64
		if err := sqlDB.QueryRowContext(ctx, `select state, selected_release_id from queue_items where library_item_id = $1`, libID).Scan(&state, &pointer); err != nil {
			t.Fatal(err)
		}
		switch {
		case srCount == 0 && !pointer.Valid && state == string(QueueFailed):
			// MarkLibrarySearchFailed's transaction committed last (or the
			// concurrent writer never got a chance to land before it) --
			// fully consistent: no selection, and the state agrees.
		case srCount == 1 && pointer.Valid && state == "selected":
			// The concurrent writer's transaction committed last -- also
			// fully consistent, and the pointer must reference the row that
			// actually exists.
			var pointedRowExists bool
			if err := sqlDB.QueryRowContext(ctx, `select exists(select 1 from selected_releases where id = $1 and library_item_id = $2)`, pointer.Int64, libID).Scan(&pointedRowExists); err != nil {
				t.Fatal(err)
			}
			if !pointedRowExists {
				t.Fatalf("queue_items.selected_release_id=%d does not reference an existing selected_releases row for library_item_id=%d -- dangling reference", pointer.Int64, libID)
			}
		default:
			t.Fatalf("inconsistent end state: %d selected_releases row(s), queue_items.state=%q, selected_release_id=%+v", srCount, state, pointer)
		}
	})
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

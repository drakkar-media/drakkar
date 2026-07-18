package database

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestClaimURLDispatchPersistedBasicBehavior guards the persisted backstop
// added in the 2026-07-17 exhaustive audit for workflow.Service's in-memory
// recentURLHits cooldown, which resets on every process restart.
// recent_url_fetches is a Postgres-backed equivalent that survives a
// restart.
func TestClaimURLDispatchPersistedBasicBehavior(t *testing.T) {
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

	const testURL = "http://example/recent-url-fetches-test.nzb"
	defer sqlDB.ExecContext(ctx, `delete from recent_url_fetches where external_url = $1`, testURL)

	claimed, err := db.ClaimURLDispatchPersisted(ctx, testURL, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("expected the first claim on an unseen URL to succeed")
	}

	claimed, err = db.ClaimURLDispatchPersisted(ctx, testURL, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("expected a second claim within the cooldown window to fail (someone already holds it)")
	}

	// A zero-length cooldown must always let a new claim through (even a
	// dispatch from this instant is not "recently dispatched" under a
	// 0-duration window).
	claimed, err = db.ClaimURLDispatchPersisted(ctx, testURL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("expected a 0-duration cooldown to always succeed in claiming")
	}

	// Backdate the row past a 1-hour prune threshold and confirm
	// PruneRecentURLFetches removes it.
	if _, err := sqlDB.ExecContext(ctx, `
		update recent_url_fetches set dispatched_at = now() - interval '2 hours' where external_url = $1`, testURL,
	); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.PruneRecentURLFetches(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if deleted < 1 {
		t.Fatalf("expected PruneRecentURLFetches to delete at least the backdated test row, got %d", deleted)
	}
	claimed, err = db.ClaimURLDispatchPersisted(ctx, testURL, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("expected the pruned row to no longer block a fresh claim")
	}
}

// TestClaimURLDispatchPersistedIsAtomicUnderConcurrency guards the exact
// gap found in the 2026-07-18 "make it a fact" audit: the two functions
// this replaced (a plain SELECT followed by a separate, unconditional
// UPSERT) let two concurrent callers racing on the identical URL both read
// "not recently dispatched" before either committed its mark -- confirmed
// live-reachable via season-pack search, which legitimately creates one
// selected_releases row per missing episode, all referencing the identical
// URL, picked up by multiple concurrent download workers. This test fires
// many real goroutines at the real database, all claiming the same URL at
// once, and asserts exactly one wins.
func TestClaimURLDispatchPersistedIsAtomicUnderConcurrency(t *testing.T) {
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

	const testURL = "http://example/season-pack-race-test.nzb"
	defer sqlDB.ExecContext(ctx, `delete from recent_url_fetches where external_url = $1`, testURL)

	const attempts = 16
	var wins atomic.Int32
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claimed, err := db.ClaimURLDispatchPersisted(ctx, testURL, 30*time.Minute)
			errs[i] = err
			if claimed {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d returned error: %v", i, err)
		}
	}
	if got := wins.Load(); got != 1 {
		t.Fatalf("expected exactly 1 of %d concurrent claims on the identical URL to win, got %d", attempts, got)
	}
}

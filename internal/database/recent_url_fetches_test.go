package database

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestRecentURLFetchesPersistAcrossCooldownWindow guards the persisted
// backstop added in the 2026-07-17 exhaustive audit for
// workflow.Service's in-memory recentURLHits cooldown, which resets on
// every process restart. recent_url_fetches is a Postgres-backed
// equivalent that survives a restart.
func TestRecentURLFetchesPersistAcrossCooldownWindow(t *testing.T) {
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

	hit, err := db.RecentlyDispatchedURLPersisted(ctx, testURL, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("expected no persisted dispatch record before any mark")
	}

	if err := db.MarkURLDispatchedPersisted(ctx, testURL); err != nil {
		t.Fatal(err)
	}

	hit, err = db.RecentlyDispatchedURLPersisted(ctx, testURL, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("expected a persisted dispatch record to be found within the cooldown window")
	}

	// A zero-length cooldown must never match (even a fetch dispatched this
	// instant is not "recently dispatched" under a 0-duration window).
	hit, err = db.RecentlyDispatchedURLPersisted(ctx, testURL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("expected a 0-duration cooldown to never report a hit")
	}

	// Re-marking (simulating a second dispatch) must upsert, not error on
	// the primary key conflict.
	if err := db.MarkURLDispatchedPersisted(ctx, testURL); err != nil {
		t.Fatalf("expected re-marking the same URL to upsert cleanly, got error: %v", err)
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
	hit, err = db.RecentlyDispatchedURLPersisted(ctx, testURL, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("expected the pruned row to no longer be found")
	}
}

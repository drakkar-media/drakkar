package app

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/drakkar-media/drakkar/internal/database"
)

// TestShouldRunRecentOnStartup guards the fix for a gap found live in
// production (2026-07-19): taskNZBHealthCheck's 168-hour (7-day) interval
// used a bare runOnStartup=false with no catch-up check at all, unlike this
// file's other long-interval tasks. Its in-process timer resets to zero on
// every container restart, and this app gets redeployed far more often
// than every 7 days -- so the task could go without a single completed run
// indefinitely. shouldRunRecentOnStartup is the general mechanism (already
// used by the RSS feed tasks) that fixes this: check the persisted cursor
// at startup and only run immediately if actually overdue.
func TestShouldRunRecentOnStartup(t *testing.T) {
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
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

	t.Run("no cursor at all -- never run before, run now", func(t *testing.T) {
		const task = "scheduler-startup-test-no-cursor"
		if shouldRunRecentOnStartup(ctx, db, task, 168*time.Hour, 0, now) != true {
			t.Fatal("expected true when no cursor row exists yet")
		}
	})

	t.Run("cursor well within interval -- not overdue, do not run now", func(t *testing.T) {
		const task = "scheduler-startup-test-fresh"
		defer sqlDB.ExecContext(ctx, `delete from maintenance_cursors where task_name = $1`, task)
		if err := db.TouchMaintenanceCursor(ctx, task, now.Add(-1*time.Hour).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		if shouldRunRecentOnStartup(ctx, db, task, 168*time.Hour, 0, now) != false {
			t.Fatal("expected false when the last run was well within the interval")
		}
	})

	t.Run("cursor older than interval -- overdue, run now", func(t *testing.T) {
		const task = "scheduler-startup-test-stale"
		defer sqlDB.ExecContext(ctx, `delete from maintenance_cursors where task_name = $1`, task)
		if err := db.TouchMaintenanceCursor(ctx, task, now.Add(-10*24*time.Hour).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		if shouldRunRecentOnStartup(ctx, db, task, 168*time.Hour, 0, now) != true {
			t.Fatal("expected true when the last run is older than the interval (the exact scenario found live: repeated restarts kept resetting the in-process timer before it ever reached 7 days)")
		}
	})
}

// TestLacksSchedulerLaneHeadroom guards the gap found in the 2026-07-19
// audit: the NNTP scheduler's foreground and background lanes each draw
// maxDownloadConnections workers from the SAME shared per-provider
// connection pool (sized at totalWorkers, the raw account ceiling) -- if
// that pool doesn't have room for both lanes at once, a background
// calibration/health-check burst can delay foreground playback fetches,
// defeating the point of having separate lanes at all.
func TestLacksSchedulerLaneHeadroom(t *testing.T) {
	cases := []struct {
		name                   string
		totalWorkers           int
		maxDownloadConnections int
		want                   bool
	}{
		{"comfortable headroom (this repo's production config: 100 vs 15)", 100, 15, false},
		{"exactly zero headroom (checked-in sample config: 30 vs 15)", 30, 15, true},
		{"less than zero headroom", 20, 15, true},
		{"just barely enough headroom", 31, 15, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lacksSchedulerLaneHeadroom(tc.totalWorkers, tc.maxDownloadConnections); got != tc.want {
				t.Fatalf("lacksSchedulerLaneHeadroom(%d, %d) = %v, want %v", tc.totalWorkers, tc.maxDownloadConnections, got, tc.want)
			}
		})
	}
}

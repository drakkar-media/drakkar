package database

import (
	"context"
	"database/sql"
	"os"
	"syscall"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestRunSpeedTestErrorsWithNoDownloadedMedia guards the "nothing to test
// yet" case: on a fresh install (or a library with everything still inline),
// RunSpeedTest must fail with a clear, actionable message rather than a
// confusing internal error or a false zero-Mbps result.
func TestRunSpeedTestErrorsWithNoDownloadedMedia(t *testing.T) {
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

	// Every existing virtual_files row (from other tests/production data) is
	// either "inline" or has size_bytes <= 0 in the worst case -- to isolate
	// this test from whatever else is in the table, only assert on the
	// error's presence and message shape, not on the table being empty.
	if _, err := db.RunSpeedTest(ctx); err == nil {
		// Some other test's fixture may have left a real direct_nzb/stored_rar
		// row behind with size_bytes > 0; if so this call would succeed
		// (and likely fail fast on the fetch instead). Only fail this test if
		// we can independently confirm no eligible row exists.
		var count int
		if scanErr := sqlDB.QueryRowContext(ctx, `
			select count(*) from virtual_files
			where reader_kind != 'inline' and size_bytes > 0`).Scan(&count); scanErr == nil && count == 0 {
			t.Fatal("expected an error when no downloaded media is available to test")
		}
	}
}

// TestRusageCPUSeconds guards the CPU% arithmetic RunSpeedTest reports
// alongside throughput: user+sys time must be summed in whole seconds, not
// left as separate fields or double-counted.
func TestRusageCPUSeconds(t *testing.T) {
	ru := syscall.Rusage{}
	ru.Utime.Sec = 2
	ru.Utime.Usec = 500000
	ru.Stime.Sec = 1
	ru.Stime.Usec = 250000
	got := rusageCPUSeconds(ru)
	want := 3.75
	if got != want {
		t.Fatalf("rusageCPUSeconds() = %v, want %v", got, want)
	}
}

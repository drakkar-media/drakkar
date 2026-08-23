package app

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/database"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rs/zerolog"
)

// TestRepairArchiveRangeForReleaseRespectsSharedGate confirms
// RepairArchiveRangeForRelease reports started=false when the sweep already
// holds the shared repairRun gate, without needing a real db/publisher --
// the gate check happens before either dependency is touched.
func TestRepairArchiveRangeForReleaseRespectsSharedGate(t *testing.T) {
	svc := &archiveRangeRepairService{}
	svc.repairRun.Store(true)
	changed, started, err := svc.RepairArchiveRangeForRelease(context.Background(), 1)
	if started {
		t.Fatal("acquired an already-held repair gate")
	}
	if changed || err != nil {
		t.Fatalf("expected no-op result while busy, got changed=%v err=%v", changed, err)
	}
	svc.repairRun.Store(false)
	if !svc.repairRun.CompareAndSwap(false, true) {
		t.Fatal("gate did not release after Store(false)")
	}
}

func TestTryStartArchiveRangeRepairSweepIsSingleFlightAndReleases(t *testing.T) {
	svc := &archiveRangeRepairService{}
	run, started := svc.TryStartArchiveRangeRepairSweep()
	if !started || run == nil {
		t.Fatal("expected first sweep reservation")
	}
	if _, started := svc.TryStartArchiveRangeRepairSweep(); started {
		t.Fatal("overlapping sweep reservation acquired shared worker")
	}
	// Release without invoking run (which needs a real db) — exercise the
	// same underlying gate repairRun directly, matching how run's own
	// deferred release works.
	svc.repairRun.Store(false)
	if _, started := svc.TryStartArchiveRangeRepairSweep(); !started {
		t.Fatal("worker reservation was not released")
	}
}

func TestDecodeArchiveRangeRepairProgressValidatesCursor(t *testing.T) {
	valid := `{"startedAt":"2026-08-13T10:00:00Z","afterArchiveId":12,"throughArchiveId":99,"scannedRows":7,"repairedItems":2}`
	progress, err := decodeArchiveRangeRepairProgress(valid)
	if err != nil {
		t.Fatal(err)
	}
	if progress.AfterArchiveID != 12 || progress.ThroughArchiveID != 99 || progress.ScannedRows != 7 || progress.RepairedItems != 2 {
		t.Fatalf("unexpected progress: %+v", progress)
	}

	for _, raw := range []string{
		`not-json`,
		`{"afterArchiveId":1,"throughArchiveId":2}`,
		`{"startedAt":"2026-08-13T10:00:00Z","afterArchiveId":3,"throughArchiveId":2}`,
		`{"startedAt":"2026-08-13T10:00:00Z","afterArchiveId":1,"throughArchiveId":2,"scannedRows":-1}`,
	} {
		if _, err := decodeArchiveRangeRepairProgress(raw); err == nil {
			t.Fatalf("expected invalid progress error for %q", raw)
		}
	}
}

func TestArchiveRangeRepairProgressPersistsAcrossRuns(t *testing.T) {
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db := &database.DB{SQL: sqlDB}
	ctx := context.Background()

	original, err := db.GetMaintenanceCursor(ctx, taskArchiveRangeRepairProgress)
	if err != nil {
		t.Fatal(err)
	}
	originalCompletion, err := db.GetMaintenanceCursor(ctx, taskArchiveRangeRepair)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if original == "" {
			_ = db.DeleteMaintenanceCursor(ctx, taskArchiveRangeRepairProgress)
		} else {
			_ = db.TouchMaintenanceCursor(ctx, taskArchiveRangeRepairProgress, original)
		}
		if originalCompletion == "" {
			_ = db.DeleteMaintenanceCursor(ctx, taskArchiveRangeRepair)
		} else {
			_ = db.TouchMaintenanceCursor(ctx, taskArchiveRangeRepair, originalCompletion)
		}
	}()
	if err := db.DeleteMaintenanceCursor(ctx, taskArchiveRangeRepairProgress); err != nil {
		t.Fatal(err)
	}

	progress, err := loadOrStartArchiveRangeRepairSweep(ctx, db, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if progress.StartedAt.IsZero() || progress.ThroughArchiveID < 0 {
		t.Fatalf("invalid initial progress: %+v", progress)
	}
	progress.AfterArchiveID = progress.ThroughArchiveID
	progress.RepairedItems = 3
	if err := saveArchiveRangeRepairProgress(ctx, db, progress); err != nil {
		t.Fatal(err)
	}

	resumed, err := loadOrStartArchiveRangeRepairSweep(ctx, db, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if resumed.AfterArchiveID != progress.AfterArchiveID ||
		resumed.ThroughArchiveID != progress.ThroughArchiveID ||
		resumed.RepairedItems != progress.RepairedItems ||
		!resumed.StartedAt.Equal(progress.StartedAt) {
		t.Fatalf("resume changed persisted progress: got %+v want %+v", resumed, progress)
	}
	if err := db.DeleteMaintenanceCursor(ctx, taskArchiveRangeRepairProgress); err != nil {
		t.Fatal(err)
	}
	fresh, err := loadOrStartArchiveRangeRepairSweep(ctx, db, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if fresh.AfterArchiveID != 0 || fresh.RepairedItems != 0 {
		t.Fatalf("expected a fresh sweep after deleting the cursor, got %+v", fresh)
	}
}

func TestWaitForArchiveRangeRepairPacingStopsForCancellationAndBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if waitForArchiveRangeRepairPacing(ctx, time.Time{}) {
		t.Fatal("canceled pacing wait reported success")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("canceled pacing wait took %v", elapsed)
	}
	if waitForArchiveRangeRepairPacing(context.Background(), time.Now().Add(archiveRangeRepairPacingDelay/2)) {
		t.Fatal("pacing wait exceeded remaining sweep budget")
	}
}

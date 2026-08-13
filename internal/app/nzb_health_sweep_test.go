package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/maintenance"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rs/zerolog"
)

func TestReserveDeepHealthRunIsSingleFlightAndReleases(t *testing.T) {
	svc := &maintenanceOpsService{}
	started := make(chan struct{})
	release := make(chan struct{})
	wantErr := errors.New("finished")

	run, ok := svc.reserveDeepHealthRun(func(context.Context) (maintenance.Result, error) {
		close(started)
		<-release
		return maintenance.Result{}, wantErr
	})
	if !ok || run == nil {
		t.Fatal("expected first deep-health reservation")
	}
	done := make(chan error, 1)
	go func() {
		_, err := run(context.Background())
		done <- err
	}()
	<-started

	if _, ok := svc.reserveDeepHealthRun(func(context.Context) (maintenance.Result, error) {
		return maintenance.Result{}, nil
	}); ok {
		t.Fatal("overlapping deep-health run acquired shared worker")
	}
	close(release)
	if err := <-done; !errors.Is(err, wantErr) {
		t.Fatalf("unexpected first run error: %v", err)
	}

	next, ok := svc.reserveDeepHealthRun(func(context.Context) (maintenance.Result, error) {
		return maintenance.Result{ScannedRows: 1}, nil
	})
	if !ok {
		t.Fatal("worker reservation was not released after run exit")
	}
	result, err := next(context.Background())
	if err != nil || result.ScannedRows != 1 {
		t.Fatalf("unexpected second run result: %+v, %v", result, err)
	}

	panicRun, ok := svc.reserveDeepHealthRun(func(context.Context) (maintenance.Result, error) {
		panic("test panic")
	})
	if !ok {
		t.Fatal("expected panic-path reservation")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected reserved run panic")
			}
		}()
		_, _ = panicRun(context.Background())
	}()
	afterPanic, ok := svc.reserveDeepHealthRun(func(context.Context) (maintenance.Result, error) {
		return maintenance.Result{}, nil
	})
	if !ok {
		t.Fatal("worker reservation was not released after panic")
	}
	_, _ = afterPanic(context.Background())
}

func TestDecodeDeepHealthSweepProgressValidatesCursor(t *testing.T) {
	valid := `{"startedAt":"2026-08-13T10:00:00Z","afterLibraryItemId":12,"throughLibraryItemId":99,"scannedRows":7}`
	progress, err := decodeDeepHealthSweepProgress(valid)
	if err != nil {
		t.Fatal(err)
	}
	if progress.AfterLibraryItemID != 12 || progress.ThroughLibraryItemID != 99 || progress.ScannedRows != 7 {
		t.Fatalf("unexpected progress: %+v", progress)
	}

	for _, raw := range []string{
		`not-json`,
		`{"afterLibraryItemId":1,"throughLibraryItemId":2}`,
		`{"startedAt":"2026-08-13T10:00:00Z","afterLibraryItemId":3,"throughLibraryItemId":2}`,
		`{"startedAt":"2026-08-13T10:00:00Z","afterLibraryItemId":1,"throughLibraryItemId":2,"scannedRows":-1}`,
	} {
		if _, err := decodeDeepHealthSweepProgress(raw); err == nil {
			t.Fatalf("expected invalid progress error for %q", raw)
		}
	}
}

func TestDeepHealthSweepProgressPersistsAcrossRuns(t *testing.T) {
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

	original, err := db.GetMaintenanceCursor(ctx, taskNZBHealthCheckProgress)
	if err != nil {
		t.Fatal(err)
	}
	originalCompletion, err := db.GetMaintenanceCursor(ctx, taskNZBHealthCheck)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if original == "" {
			_ = db.DeleteMaintenanceCursor(ctx, taskNZBHealthCheckProgress)
		} else {
			_ = db.TouchMaintenanceCursor(ctx, taskNZBHealthCheckProgress, original)
		}
		if originalCompletion == "" {
			_ = db.DeleteMaintenanceCursor(ctx, taskNZBHealthCheck)
		} else {
			_ = db.TouchMaintenanceCursor(ctx, taskNZBHealthCheck, originalCompletion)
		}
	}()
	if err := db.DeleteMaintenanceCursor(ctx, taskNZBHealthCheckProgress); err != nil {
		t.Fatal(err)
	}

	progress, err := loadOrStartDeepHealthSweep(ctx, db, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if progress.StartedAt.IsZero() || progress.ThroughLibraryItemID < 0 {
		t.Fatalf("invalid initial progress: %+v", progress)
	}
	progress.AfterLibraryItemID = progress.ThroughLibraryItemID
	if err := saveDeepHealthSweepProgress(ctx, db, progress); err != nil {
		t.Fatal(err)
	}

	resumed, err := loadOrStartDeepHealthSweep(ctx, db, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if resumed.AfterLibraryItemID != progress.AfterLibraryItemID ||
		resumed.ThroughLibraryItemID != progress.ThroughLibraryItemID ||
		!resumed.StartedAt.Equal(progress.StartedAt) {
		t.Fatalf("resume changed persisted progress: got %+v want %+v", resumed, progress)
	}
	if err := db.TouchMaintenanceCursor(ctx, taskNZBHealthCheck, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	due, err := shouldRunDeepHealthSweep(ctx, db, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !due {
		t.Fatal("unfinished progress did not make coordinator runnable")
	}
	if err := db.DeleteMaintenanceCursor(ctx, taskNZBHealthCheckProgress); err != nil {
		t.Fatal(err)
	}
	due, err = shouldRunDeepHealthSweep(ctx, db, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if due {
		t.Fatal("fresh completed sweep was scheduled without unfinished progress")
	}
}

func TestWaitForDeepHealthPacingStopsForCancellationAndBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if waitForDeepHealthPacing(ctx, time.Time{}) {
		t.Fatal("canceled pacing wait reported success")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("canceled pacing wait took %v", elapsed)
	}
	if waitForDeepHealthPacing(context.Background(), time.Now().Add(deepHealthPacingDelay/2)) {
		t.Fatal("pacing wait exceeded remaining sweep budget")
	}
}

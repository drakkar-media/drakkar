package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/drakkar-media/drakkar/internal/stream"
)

// hangingSegmentSizer simulates a document whose every segment fetch hangs
// until its context is cancelled -- standing in for a real NNTP fetch that
// never returns within a reasonable time (e.g. a provider issue, or a very
// large run of confirmCRCMismatchAttempts*confirmCRCMismatchDelay delays
// across many consecutive corrupt segments).
type hangingSegmentSizer struct {
	calls int
}

func (h *hangingSegmentSizer) FetchRange(ctx context.Context, segment stream.SegmentRange) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (h *hangingSegmentSizer) DecodedSize(ctx context.Context, messageID string) (int64, error) {
	h.calls++
	<-ctx.Done()
	return 0, ctx.Err()
}

// TestCalibrateNZBOffsetsBatchRespectsPerDocumentBudget guards the gap found
// in the 2026-07-19 audit: CalibrateNZBOffsetsBatch had no per-document time
// budget on the periodic health-check path (unlike the import-time path's
// asyncCalibrateBudget), so a document whose segments never resolve could
// stall the whole batch indefinitely, delaying every other document waiting
// behind it and the maintenance pass's own cursor touch. This test uses a
// segment fetcher that hangs forever unless its context is cancelled, and
// confirms the batch call returns promptly once perDocumentCalibrateBudget
// elapses rather than hanging for the test's own timeout.
func TestCalibrateNZBOffsetsBatchRespectsPerDocumentBudget(t *testing.T) {
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

	original := perDocumentCalibrateBudget
	perDocumentCalibrateBudget = 100 * time.Millisecond
	t.Cleanup(func() { perDocumentCalibrateBudget = original })

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `insert into library_items (media_type, title) values ('tv','calibrate-budget-test') returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)
	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `insert into release_candidates (library_item_id, title) values ($1, 'calibrate-budget-test') returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := sqlDB.QueryRowContext(ctx, `insert into selected_releases (library_item_id, release_candidate_id) values ($1, $2) returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}
	var nzbDocID int64
	if err := sqlDB.QueryRowContext(ctx, `insert into nzb_documents (selected_release_id, file_name) values ($1, 'hangs.nzb') returning id`, srID).Scan(&nzbDocID); err != nil {
		t.Fatal(err)
	}
	// Several files so a single-file early exit wouldn't mask the budget --
	// if the budget didn't apply, this many hanging fetches would block far
	// longer than the test's own generous ceiling below.
	for i := 0; i < 5; i++ {
		if _, err := sqlDB.ExecContext(ctx, `
			insert into nzb_files (nzb_document_id, subject, message_ids, decoded_segment_size)
			values ($1, $2, $3, 700000)`,
			nzbDocID, "hangs-part", pgTextArray([]string{"<hang-msg>"}),
		); err != nil {
			t.Fatal(err)
		}
	}

	fetcher := &hangingSegmentSizer{}
	db := &DB{SQL: sqlDB, SegmentFetcher: fetcher}

	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := db.CalibrateNZBOffsetsBatch(context.Background(), 10); err != nil {
			t.Error(err)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CalibrateNZBOffsetsBatch did not return within 5s -- per-document budget was not enforced")
	}
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("expected the batch to return shortly after the 100ms per-document budget elapsed, took %v", elapsed)
	}
	if fetcher.calls == 0 {
		t.Fatal("expected at least one DecodedSize call to have been attempted")
	}

	var pending int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from nzb_files where nzb_document_id = $1 and calibrated_at is null`, nzbDocID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending == 0 {
		t.Fatal("expected the hung document's files to remain uncalibrated (retried on a later pass), not falsely marked done")
	}
}

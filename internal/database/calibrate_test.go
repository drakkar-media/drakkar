package database

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/drakkar-media/drakkar/internal/nntp"
	"github.com/drakkar-media/drakkar/internal/stream"
)

type fakeSegmentChecker struct {
	checkErr error
}

func (f *fakeSegmentChecker) FetchRange(ctx context.Context, segment stream.SegmentRange) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeSegmentChecker) Exists(ctx context.Context, messageID string) error {
	return f.checkErr
}

// TestIsArticlePermanentlyMissing guards the calibration fix: previously,
// CalibrateNZBOffsets treated ANY DecodedSize error (timeout, provider
// throttle, connection reset, or the async import-time calibration budget
// simply expiring) as a permanent "article gone" signal and froze
// nzb_files.calibrated_at forever, leaving virtual_files.size_bytes stuck at
// the pre-calibration estimate -- which then silently truncated the served
// file (confirmed in production: 246 library items with a real, undersized
// virtual_files.size_bytes causing Plex to report "video: none, audio:
// none"). Only a confirmed NNTP STAT 430 (nntp.ErrArticleMissing) should be
// treated as permanent; everything else must fall through to "retry later".
func TestIsArticlePermanentlyMissing(t *testing.T) {
	tests := []struct {
		name     string
		checkErr error
		want     bool
	}{
		{"confirmed missing via STAT 430", nntp.ErrArticleMissing, true},
		{"wrapped confirmed missing", fmt.Errorf("stat failed: %w", nntp.ErrArticleMissing), true},
		{"context deadline exceeded", context.DeadlineExceeded, false},
		{"context canceled", context.Canceled, false},
		{"provider circuit open (throttled)", nntp.ErrProviderCircuitOpen, false},
		{"generic connection error", errors.New("connection reset by peer"), false},
		{"exists reports no error", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &DB{SegmentFetcher: &fakeSegmentChecker{checkErr: tt.checkErr}}
			got := db.isArticlePermanentlyMissing(context.Background(), "<msg1>")
			if got != tt.want {
				t.Fatalf("isArticlePermanentlyMissing() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsArticlePermanentlyMissingWithoutChecker guards the fallback when the
// configured SegmentFetcher doesn't support existence checks at all (e.g. a
// test double or a source with no STAT support): must default to "not
// confirmed missing" rather than panicking or assuming permanence.
func TestIsArticlePermanentlyMissingWithoutChecker(t *testing.T) {
	db := &DB{SegmentFetcher: nil}
	if db.isArticlePermanentlyMissing(context.Background(), "<msg1>") {
		t.Fatal("expected false when SegmentFetcher is unavailable")
	}
}

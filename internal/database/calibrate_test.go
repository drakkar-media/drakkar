package database

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/drakkar-media/drakkar/internal/nntp"
	"github.com/drakkar-media/drakkar/internal/stream"
	"github.com/drakkar-media/drakkar/internal/yenc"
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
			// decodeErr is a generic decode failure here (distinct from the
			// CRC-mismatch case below) so these cases exercise the STAT-based
			// fallback path exactly as before.
			got := db.isArticlePermanentlyMissing(context.Background(), "<msg1>", errors.New("decode failed"))
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
	if db.isArticlePermanentlyMissing(context.Background(), "<msg1>", errors.New("decode failed")) {
		t.Fatal("expected false when SegmentFetcher is unavailable")
	}
}

// TestIsArticlePermanentlyMissingCRCMismatch guards a gap found in the
// 2026-07-19 production incident investigation: a yEnc CRC mismatch means
// the article exists (a STAT would succeed) but its posted content is
// corrupt. Usenet articles are immutable, so re-fetching the identical
// message-id decodes the identical bytes and fails the identical CRC check
// forever -- but isArticlePermanentlyMissing used to discard decodeErr
// entirely and run only the STAT-based check, which trivially succeeds for
// a corrupt-but-present article, so this was misclassified as transient and
// retried on every 15-minute health-check pass, forever, each one a real
// live NNTP fetch+decode of a segment already known to be corrupt.
func TestIsArticlePermanentlyMissingCRCMismatch(t *testing.T) {
	// Exists() would succeed (nil) for a CRC-corrupt-but-present article --
	// confirm the CRC check alone is decisive without ever reaching Exists().
	db := &DB{SegmentFetcher: &fakeSegmentChecker{checkErr: nil}}
	if !db.isArticlePermanentlyMissing(context.Background(), "<msg1>", yenc.ErrCRCMismatch) {
		t.Fatal("expected a yEnc CRC mismatch to be treated as permanent")
	}
	if !db.isArticlePermanentlyMissing(context.Background(), "<msg1>", fmt.Errorf("decode: %w", yenc.ErrCRCMismatch)) {
		t.Fatal("expected a wrapped yEnc CRC mismatch to be treated as permanent")
	}
}

// fakeCachedMissError mimics internal/nntp's articleNotFoundError: a cache
// HIT for an already-confirmed-missing article, returned as a distinct type
// from nntp.ErrArticleMissing that reports itself as that sentinel via the
// errors.Is Is(error) bool protocol rather than wrapping it directly (see
// articleNotFoundError.Is in internal/nntp/article_cache.go for the real
// production type this stands in for).
type fakeCachedMissError string

func (e fakeCachedMissError) Error() string        { return "article not found (cached): " + string(e) }
func (e fakeCachedMissError) Is(target error) bool { return target == nntp.ErrArticleMissing }

// TestIsArticlePermanentlyMissingCachedMiss guards the other half of the
// 2026-07-19 incident: CachedFallbackSource.Stat/BodyPriority serve a cache
// HIT for an already-confirmed-missing article as a distinct error type
// from nntp.ErrArticleMissing. Without that type implementing the
// errors.Is Is(error) bool contract, errors.Is(cachedErr, ErrArticleMissing)
// fails, so a cached "definitely missing" result was misclassified as
// transient and retried forever instead of being marked calibrated_at once.
func TestIsArticlePermanentlyMissingCachedMiss(t *testing.T) {
	db := &DB{SegmentFetcher: &fakeSegmentChecker{checkErr: fakeCachedMissError("<msg1>")}}
	if !db.isArticlePermanentlyMissing(context.Background(), "<msg1>", errors.New("decode failed")) {
		t.Fatal("expected a cached-missing article (reported via the Is(error) bool contract) to be treated as permanent")
	}
}

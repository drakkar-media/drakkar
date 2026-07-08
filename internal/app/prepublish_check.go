package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/hjongedijk/drakkar/internal/config"
	"github.com/hjongedijk/drakkar/internal/database"
	"github.com/rs/zerolog"
)

// verifyContentBeforePublish validates every playable video file of a
// freshly-imported release before it's ever exposed via a symlink — mirroring
// nzbdav's model of validating full content before the file ever becomes
// visible, rather than publishing first and discovering corruption later via
// the periodic health check (which is how "Video: none / Audio: none" items
// were reaching Plex in the first place).
//
// A fresh import must become readable before publish proceeds. We allow only a
// short warm-up window here: enough for the content VFS/cache to settle, but
// not so long that every bad candidate stalls the download queue for nearly a
// minute before Drakkar tries the next release.
func verifyContentBeforePublish(ctx context.Context, db *database.DB, rt config.Runtime, selectedReleaseID int64, logger zerolog.Logger) error {
	entries, err := db.ListContentMountEntriesForRelease(ctx, selectedReleaseID)
	if err != nil {
		// Can't determine the file list — don't block publish on our own
		// query failure; the periodic health check will still catch a
		// genuine problem afterward.
		return nil
	}
	for _, e := range entries {
		if !database.IsPlayableMediaFile(e.FileName, e.SizeBytes) {
			continue
		}
		target := filepath.Join(rt.FuseMountPath, "content", e.Path)
		if err := verifyOneFileBeforePublish(ctx, target, e.FileName); err != nil {
			logger.Warn().
				Int64("selectedReleaseId", selectedReleaseID).
				Str("path", target).
				Err(err).
				Msg("pre-publish content check inconclusive — publishing anyway, periodic health check will re-verify")
			if !errors.Is(err, errContainerHeaderUnreadable) {
				return err
			}
		}
	}
	return nil
}

// verifyOneFileBeforePublish checks a single file's container header before
// publish. It returns nil for a genuinely valid container. A wrapped
// errContainerHeaderUnreadable means the header bytes never became readable
// (provider throttling, momentary VFS cache lag) — inconclusive, not proof of
// corruption, so callers should let publish proceed and defer to the
// periodic health check's much longer retry window. Any other non-nil error
// means real bytes were read and they are not a valid video container —
// that's definitive and should block publish.
func verifyOneFileBeforePublish(ctx context.Context, target, fileName string) error {
	if err := waitForReadableVideoContainer(ctx, target, 3, 2*time.Second); err != nil {
		if errors.Is(err, errContainerHeaderUnreadable) {
			return err
		}
		return fmt.Errorf("invalid video container for %s: %w", fileName, err)
	}
	return nil
}

func waitForReadableVideoContainer(ctx context.Context, path string, attempts int, delay time.Duration) error {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := readContainerHeader(path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if !errors.Is(lastErr, errContainerHeaderUnreadable) || attempt == attempts {
			return lastErr
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w: %v", errContainerHeaderUnreadable, ctx.Err())
		case <-timer.C:
		}
	}
	return lastErr
}

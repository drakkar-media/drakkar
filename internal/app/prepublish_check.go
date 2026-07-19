package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/rclone"
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
func verifyContentBeforePublish(ctx context.Context, db *database.DB, rt config.Runtime, rc *rclone.Client, selectedReleaseID int64, logger zerolog.Logger) error {
	entries, err := db.ListContentMountEntriesForRelease(ctx, selectedReleaseID)
	if err != nil {
		// Can't determine the file list — don't block publish on our own
		// query failure; the periodic health check will still catch a
		// genuine problem afterward.
		return nil
	}
	// Refresh just the release's own directory -- cheap, since it only lists
	// this release's handful of files. Deliberately does NOT also refresh the
	// "releases" parent directory: that one lists every release the library
	// has (9,000+ here), and golang.org/x/net/webdav's PROPFIND handler Stats
	// every child to build the response regardless of requested depth, so
	// refreshing "releases" means one DB query per release, every time --
	// confirmed live at ~8-11s and heavy sustained CPU/DB load per call. Doing
	// that on every publish saturated the webdav server that Plex playback
	// also depends on. Without this parent refresh, a release created only
	// moments ago can still occasionally hit the "no such file or directory"
	// race this check already treats as inconclusive-not-fatal -- an
	// acceptable, much cheaper tradeoff than the alternative.
	releaseDir := filepath.Join(rt.FuseMountPath, "content", "releases", fmt.Sprintf("%d", selectedReleaseID))
	_ = rc.RefreshMountPath(ctx, rt.FuseMountPath, releaseDir)
	for _, e := range entries {
		if !database.IsPlayableMediaFile(e.FileName, e.SizeBytes) {
			logger.Debug().Str("file", e.FileName).Int64("sizeBytes", e.SizeBytes).
				Msg("pre-publish check: skipping non-playable file")
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
		} else {
			logger.Debug().Str("file", e.FileName).Msg("pre-publish check: container header valid")
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

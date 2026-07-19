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
	// rclone's VFS resolves a path by walking its own cached directory tree
	// (--dir-cache-time, 20s, see docker-compose.yml): to open
	// .../content/releases/{id}/{file} it must first find "{id}" listed as a
	// child of "releases". For a release that was only just created, the
	// "releases" listing rclone already has cached predates that child's
	// existence, so the lookup fails at that path component with "no such
	// file or directory" -- even though the file itself is already served
	// correctly by any direct request to the backend. Refreshing only the
	// release's own directory (as the post-publish path in
	// internal/library/publisher.go does, safely, since by then the release
	// has existed for a while) doesn't fix this: there's nothing stale to
	// invalidate in a directory listing that was never fetched in the first
	// place. Refresh the "releases" parent too, so the new child becomes
	// visible before the release's own directory is ever listed.
	releaseDir := filepath.Join(rt.FuseMountPath, "content", "releases", fmt.Sprintf("%d", selectedReleaseID))
	_ = rc.RefreshPath(ctx, filepath.Dir(releaseDir))
	_ = rc.RefreshPath(ctx, releaseDir)
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

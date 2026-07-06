package app

import (
	"context"
	"fmt"
	"path/filepath"

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
// Only a definitive "read real bytes, they're not a valid video container"
// verdict blocks publish. An inconclusive read (open/read failure — provider
// throttling, momentary VFS cache lag) is logged and allowed through, exactly
// like the periodic health check's own classification, so a transient hiccup
// during import can't wrongly reject a perfectly good release.
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
		if magicErr := checkVFSContainerMagic(target); magicErr != nil {
			if isTransientHealthCheckErr(magicErr) {
				logger.Warn().
					Int64("selectedReleaseId", selectedReleaseID).
					Str("path", target).
					Err(magicErr).
					Msg("pre-publish content check inconclusive — publishing anyway, periodic health check will re-verify")
				continue
			}
			return fmt.Errorf("invalid video container for %s: %w", e.FileName, magicErr)
		}
	}
	return nil
}

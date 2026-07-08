package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/hjongedijk/drakkar/internal/database"
	"github.com/hjongedijk/drakkar/internal/library"
	"github.com/hjongedijk/drakkar/internal/maintenance"
	"github.com/hjongedijk/drakkar/internal/workflow"
	"github.com/rs/zerolog"
)

type maintenanceOpsService struct {
	base           *maintenance.Service
	db             *database.DB
	workflowSvc    *workflow.Service
	publicationSvc *library.Publisher
	logger         zerolog.Logger
}

func (s *maintenanceOpsService) RemoveBrokenMediaSymlinks(ctx context.Context) (maintenance.Result, error) {
	return s.base.RemoveBrokenMediaSymlinks(ctx)
}

func (s *maintenanceOpsService) RemoveOrphanedCompletedSymlinks(ctx context.Context) (maintenance.Result, error) {
	return s.base.RemoveOrphanedCompletedSymlinks(ctx)
}

func (s *maintenanceOpsService) RemoveOrphanedContent(ctx context.Context) (maintenance.Result, error) {
	return s.base.RemoveOrphanedContent(ctx)
}

func (s *maintenanceOpsService) PruneStaleReleaseCandidates(ctx context.Context) (maintenance.Result, error) {
	return s.base.PruneStaleReleaseCandidates(ctx)
}

func (s *maintenanceOpsService) DeepNZBHealthCheck(ctx context.Context) (maintenance.Result, error) {
	return runNZBHealthCheck(ctx, s.db, s.workflowSvc, s.publicationSvc, s.logger)
}

func nextDeepHealthCheckDelay(createdAt time.Time) time.Duration {
	age := time.Since(createdAt)
	if age < time.Hour {
		return time.Hour
	}
	if age > 30*24*time.Hour {
		return 30 * 24 * time.Hour
	}
	return age
}

func shouldRunDeepHealthCheck(now time.Time, item database.DeepHealthCandidate) bool {
	if item.LastCheckedAt == nil || item.HealthOK == nil {
		return true
	}
	if !*item.HealthOK {
		return true
	}
	return now.Sub(*item.LastCheckedAt) >= nextDeepHealthCheckDelay(item.CreatedAt)
}

// isTransientHealthCheckErr reports whether err indicates a temporary
// condition (timeout, cancellation, NNTP throttle) rather than genuine
// content corruption/unavailability, so callers can avoid blocklisting a
// perfectly good release over a provider hiccup.
func isTransientHealthCheckErr(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, errContainerHeaderUnreadable) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "status 430") || strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "provider circuit open")
}

func runNZBHealthCheckBatch(ctx context.Context, db *database.DB, workflowSvc *workflow.Service, publicationSvc *library.Publisher, logger zerolog.Logger, limit int, force bool) (maintenance.Result, error) {
	result := maintenance.Result{TaskName: "nzb-health-check"}
	candidates, err := db.ListDeepHealthCandidates(ctx, limit)
	if err != nil {
		logger.Error().Err(err).Msg("health check: query failed")
		return result, err
	}
	now := time.Now()
	logger.Info().Int("count", len(candidates)).Bool("force", force).Msg("health check: scanning deep-check candidates")
	resetSeen := make(map[int64]struct{})
	repairedSeen := make(map[int64]struct{})
	for i, c := range candidates {
		if ctx.Err() != nil {
			break
		}
		if i > 0 {
			// Pace deep checks so a large batch (e.g. a manually-triggered
			// full-library sweep) can't fire enough concurrent NNTP requests
			// to trip the provider's rate limiting — that throttling showed
			// up as generic FUSE read EOFs, not recognizable NNTP errors,
			// and caused a wave of false "corrupt content" verdicts across
			// completely unrelated releases within the same few seconds.
			time.Sleep(500 * time.Millisecond)
		}
		result.ScannedRows++
		// symlinkOK only proves the symlink resolves into the VFS content
		// tree (os.Readlink + string compare) — it never reads a single byte
		// of the target file, so it cannot prove the content is actually
		// playable. Previously this unconditionally called
		// RecordHealthCheck(symlinkOK) here, every 15 minutes, for every
		// item — which meant a real negative verdict from the deep check
		// below (StrictCheckFirstSegments + container-magic validation) got
		// silently overwritten back to healthy on the very next cheap pass,
		// long before the deep check's own backoff would run it again. A
		// symlink that resolves fine but whose Usenet articles are gone
		// (provider outage, expired retention) would report health_ok=true
		// forever. Only record here when symlinkOK is a genuine signal:
		// broken (worth flagging immediately) or freshly repaired.
		symlinkOK := database.CheckSymlinkHealth(c.LibraryPath, c.TargetPath)
		if !symlinkOK {
			_ = db.RecordHealthCheck(ctx, c.PublicationID, false)
			if publicationSvc != nil {
				logger.Warn().
					Int64("libraryItemId", c.LibraryItemID).
					Str("libraryPath", c.LibraryPath).
					Msg("health check: broken symlink publication — re-publishing item")
				if err := publicationSvc.RepublishLibraryItem(ctx, c.LibraryItemID); err != nil {
					logger.Error().Err(err).Int64("libraryItemId", c.LibraryItemID).Msg("health check: republish failed")
				} else {
					symlinkOK = database.CheckSymlinkHealth(c.LibraryPath, c.TargetPath)
					if symlinkOK {
						_ = db.RecordHealthCheck(ctx, c.PublicationID, true)
						if _, exists := repairedSeen[c.LibraryItemID]; !exists {
							repairedSeen[c.LibraryItemID] = struct{}{}
							result.RepairedItems++
						}
					}
				}
			}
			if !symlinkOK && !force {
				continue
			}
		}
		if !strings.Contains(c.TargetPath, "/content/") {
			// Not a VFS-backed symlink (e.g. completed-symlinks) — nothing to
			// deep-validate; a resolving symlink is the whole health signal.
			logger.Debug().Int64("libraryItemId", c.LibraryItemID).Str("targetPath", c.TargetPath).
				Msg("health check: non-VFS symlink, skipping deep validation")
			_ = db.RecordHealthCheck(ctx, c.PublicationID, true)
			continue
		}
		if !force && !shouldRunDeepHealthCheck(now, c) {
			logger.Debug().Int64("libraryItemId", c.LibraryItemID).Msg("health check: skipping — not due yet")
			continue
		}
		if c.NZBDocumentID <= 0 {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		err := db.StrictCheckFirstSegments(checkCtx, c.NZBDocumentID)
		cancel()
		if err != nil && isTransientHealthCheckErr(err) {
			// Timeout/throttle/connection errors don't prove the release is
			// bad — blocklisting on these caused good releases to be dropped
			// during provider hiccups. Leave it for the next scheduled pass.
			logger.Warn().
				Int64("libraryItemId", c.LibraryItemID).
				Str("title", c.Title).
				Err(err).
				Msg("health check: transient error during validation — skipping, will retry next pass")
			continue
		}
		if err == nil {
			// Also validate the video container magic bytes. Files named .mkv/.mp4
			// that are actually stubs/extras produce "Video: none / Audio: none" in
			// Plex. For an already-published item we give the VFS header read a much
			// longer retry window than the cheap single check above; if the header
			// still never becomes readable after that, the file is not fit for
			// playback and should be replaced by the next candidate.
			magicCtx, magicCancel := context.WithTimeout(ctx, 45*time.Second)
			magicErr := waitForReadableVideoContainer(magicCtx, c.TargetPath, 6, 5*time.Second)
			magicCancel()
			if magicErr != nil {
				if isTransientHealthCheckErr(magicErr) {
					logger.Warn().
						Int64("libraryItemId", c.LibraryItemID).
						Str("title", c.Title).
						Err(magicErr).
						Msg("health check: transient container-read error — skipping, will retry next pass")
					continue
				}
				err = fmt.Errorf("invalid video container: %w", magicErr)
			} else {
				logger.Debug().Int64("libraryItemId", c.LibraryItemID).Str("title", c.Title).
					Msg("health check: passed — segments and container valid")
				_ = db.RecordHealthCheck(ctx, c.PublicationID, true)
				continue
			}
		}
		logger.Warn().
			Int64("libraryItemId", c.LibraryItemID).
			Str("title", c.Title).
			Err(err).
			Msg("health check: NZB validation failed — blocklisting release and promoting next")
		_ = db.RecordHealthCheck(ctx, c.PublicationID, false)
		// Remove symlinks before blocklisting so the filesystem is clean
		// regardless of whether a next candidate exists.
		paths, pathErr := db.DeleteSymlinkPublicationsForLibraryItem(ctx, c.LibraryItemID)
		if pathErr == nil {
			for _, p := range paths {
				if removeErr := os.Remove(p); removeErr != nil && !os.IsNotExist(removeErr) {
					logger.Warn().Str("path", p).Err(removeErr).Msg("health check: could not remove symlink")
				}
			}
		}
		blocklistErr := workflowSvc.FailAndBlocklistRelease(ctx, c.SelectedReleaseID, "strict health: "+err.Error())
		if blocklistErr != nil {
			logger.Error().Err(blocklistErr).Int64("libraryItemId", c.LibraryItemID).Msg("health check: blocklist failed")
		} else if _, exists := resetSeen[c.LibraryItemID]; !exists {
			resetSeen[c.LibraryItemID] = struct{}{}
			result.ResetItems++
		}
	}
	return result, nil
}

// runNZBHealthCheck repairs bad symlinks by re-publishing them, performs decoded-
// segment validation on available items, resets broken releases for re-queue,
// and resets sample-only publications.
func runNZBHealthCheck(ctx context.Context, db *database.DB, workflowSvc *workflow.Service, publicationSvc *library.Publisher, logger zerolog.Logger) (maintenance.Result, error) {
	result, err := runNZBHealthCheckBatch(ctx, db, workflowSvc, publicationSvc, logger, 0, true)
	if err != nil {
		return result, err
	}
	resetSeen := make(map[int64]struct{})

	sampleRows, err := db.SQL.QueryContext(ctx, `
		SELECT DISTINCT qi.library_item_id, li.title, sr.id
		FROM queue_items qi
		JOIN library_items li ON li.id = qi.library_item_id
		JOIN selected_releases sr ON sr.id = qi.selected_release_id
		JOIN virtual_files vf ON vf.selected_release_id = sr.id
		WHERE qi.state = 'available' AND li.available = true
		  AND lower(vf.file_name) ~ '^(sample|sample[-_].+|.+[-_]sample)\.(mkv|mp4|avi)$'
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_files vf2
		      WHERE vf2.selected_release_id = sr.id
		        AND lower(vf2.file_name) !~ '^(sample|sample[-_].+|.+[-_]sample)\.(mkv|mp4|avi)$'
		  )`)
	if err == nil {
		defer sampleRows.Close()
		for sampleRows.Next() {
			var libID, selectedReleaseID int64
			var title string
			if err := sampleRows.Scan(&libID, &title, &selectedReleaseID); err != nil {
				continue
			}
			if _, exists := resetSeen[libID]; exists {
				continue
			}
			// Blocklist the sample-only release rather than a plain reset —
			// a plain reset only applies a ranking penalty, which isn't
			// enough to stop the same sample-only release being reselected
			// out of a small candidate pool.
			logger.Warn().Int64("libraryItemId", libID).Str("title", title).
				Msg("health check: only sample file published — blocklisting release and promoting next")
			if resetErr := workflowSvc.FailAndBlocklistRelease(ctx, selectedReleaseID, "sample-only release"); resetErr != nil {
				logger.Error().Err(resetErr).Int64("libraryItemId", libID).Msg("health check: sample blocklist failed")
			} else {
				resetSeen[libID] = struct{}{}
				result.ResetItems++
			}
		}
	}

	if err := db.TouchMaintenanceCursor(ctx, taskNZBHealthCheck, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return result, err
	}
	if result.ResetItems > 0 {
		logger.Info().Int("reset", result.ResetItems).Msg("health check: reset broken items for re-queue")
	}
	return result, nil
}

// errContainerHeaderUnreadable means the header bytes themselves could not be
// obtained (open/read failure) — this is inconclusive about the file's
// actual content and can be caused by NNTP provider throttling or a
// momentarily stale VFS cache entry, not just genuine corruption. Callers
// should treat it like a transient error (retry next pass) rather than
// blocklisting a release on the strength of it alone.
var errContainerHeaderUnreadable = errors.New("container header unreadable")

func readContainerHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: open: %v", errContainerHeaderUnreadable, err)
	}
	defer f.Close()
	// 30-second deadline so a hung FUSE read doesn't block the health check.
	_ = f.SetDeadline(time.Now().Add(30 * time.Second))
	buf := make([]byte, 12)
	n, err := io.ReadAtLeast(f, buf, 4)
	if err != nil {
		return fmt.Errorf("%w: read header: %v", errContainerHeaderUnreadable, err)
	}
	return validateVideoContainerHeader(buf[:n])
}

// validateVideoContainerHeader checks whether the first bytes of a file match
// a known video container format. Supported: MKV/WebM (EBML), AVI (RIFF),
// MP4/MOV (ISO Base Media box types: ftyp, moov, mdat, free, wide, skip).
func validateVideoContainerHeader(header []byte) error {
	if len(header) < 4 {
		return errors.New("header too short to identify container")
	}
	// MKV / WebM — EBML magic
	if header[0] == 0x1a && header[1] == 0x45 && header[2] == 0xdf && header[3] == 0xa3 {
		return nil
	}
	// AVI — RIFF header
	if string(header[0:4]) == "RIFF" {
		return nil
	}
	// MP4 / MOV — ISO Base Media File Format: box type at bytes 4–7
	if len(header) >= 8 {
		switch string(header[4:8]) {
		case "ftyp", "moov", "mdat", "free", "wide", "skip", "pnot":
			return nil
		}
	}
	return fmt.Errorf("unrecognised video container (magic: %02x %02x %02x %02x)", header[0], header[1], header[2], header[3])
}

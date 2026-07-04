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
	msg := err.Error()
	return strings.Contains(msg, "status 430") || strings.Contains(msg, "i/o timeout")
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
	for _, c := range candidates {
		if ctx.Err() != nil {
			break
		}
		result.ScannedRows++
		symlinkOK := database.CheckSymlinkHealth(c.LibraryPath, c.TargetPath)
		_ = db.RecordHealthCheck(ctx, c.PublicationID, symlinkOK)
		if !symlinkOK {
			if publicationSvc != nil {
				logger.Warn().
					Int64("libraryItemId", c.LibraryItemID).
					Str("libraryPath", c.LibraryPath).
					Msg("health check: broken symlink publication — re-publishing item")
				if err := publicationSvc.RepublishLibraryItem(ctx, c.LibraryItemID); err != nil {
					logger.Error().Err(err).Int64("libraryItemId", c.LibraryItemID).Msg("health check: republish failed")
				} else {
					symlinkOK = database.CheckSymlinkHealth(c.LibraryPath, c.TargetPath)
					_ = db.RecordHealthCheck(ctx, c.PublicationID, symlinkOK)
					if symlinkOK {
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
			_ = db.RecordHealthCheck(ctx, c.PublicationID, true)
			continue
		}
		if !force && !shouldRunDeepHealthCheck(now, c) {
			continue
		}
		if c.NZBDocumentID <= 0 {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
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
			// Plex. Reading 12 bytes from the VFS path is cheap — the first NNTP
			// segment is already cached from StrictCheckFirstSegments.
			if magicErr := checkVFSContainerMagic(c.TargetPath); magicErr != nil {
				err = fmt.Errorf("invalid video container: %w", magicErr)
			} else {
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

// checkVFSContainerMagic reads the first 12 bytes of a VFS-served file and
// validates that they carry a recognised video container signature (MKV/WebM,
// MP4/MOV, or AVI). Files with a .mkv/.mp4 extension that are actually stubs,
// extras, or corrupt placeholders will have wrong or empty magic bytes and
// cause Plex to report "Video: none / Audio: none".
// Non-VFS paths (no "/content/" segment) are skipped — they don't go through
// the NNTP stack and are always valid.
func checkVFSContainerMagic(path string) error {
	if !strings.Contains(path, "/content/") {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	// 30-second deadline so a hung FUSE read doesn't block the health check.
	_ = f.SetDeadline(time.Now().Add(30 * time.Second))
	buf := make([]byte, 12)
	n, err := io.ReadAtLeast(f, buf, 4)
	if err != nil {
		return fmt.Errorf("read header: %w", err)
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

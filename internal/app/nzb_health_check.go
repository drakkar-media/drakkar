package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/library"
	"github.com/drakkar-media/drakkar/internal/maintenance"
	"github.com/drakkar-media/drakkar/internal/mediaprobe"
	"github.com/drakkar-media/drakkar/internal/workflow"
	"github.com/rs/zerolog"
)

// maintenanceOpsService adapts maintenance.Service to the API's maintenance
// operations contract and owns the shared deep-health worker. Deep checks need
// database, workflow, and publication dependencies the base service does not
// carry; one atomic gate serializes scheduled, regular, and manual callers.
type maintenanceOpsService struct {
	base           *maintenance.Service
	db             *database.DB
	workflowSvc    *workflow.Service
	publicationSvc *library.Publisher
	logger         zerolog.Logger
	deepHealthRun  atomic.Bool
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

func (s *maintenanceOpsService) PruneOrphanedSelectedReleases(ctx context.Context) (maintenance.Result, error) {
	return s.base.PruneOrphanedSelectedReleases(ctx)
}

func (s *maintenanceOpsService) RestoreNZBFileMessageIDs(ctx context.Context) (maintenance.Result, error) {
	return s.base.RestoreNZBFileMessageIDs(ctx)
}

// TryStartDeepNZBHealthCheck reserves the process-wide deep-health worker and
// returns its bounded, resumable sweep step. Scheduled and API callers must
// invoke the returned function exactly once; it releases the reservation when
// the run exits, including after cancellation or panic.
//
// Returns:
//   - run: reserved work function, or nil while another deep check is active.
//   - bool: true only when this caller acquired the reservation.
func (s *maintenanceOpsService) TryStartDeepNZBHealthCheck() (func(context.Context) (maintenance.Result, error), bool) {
	return s.reserveDeepHealthRun(func(ctx context.Context) (maintenance.Result, error) {
		return runNZBHealthSweepStep(ctx, s.db, s.workflowSvc, s.publicationSvc, s.logger)
	})
}

// reserveDeepHealthRun wraps one deep-health operation in the service's shared
// run gate. Reservation happens before work is launched, closing the race where
// concurrent API requests could all observe an idle worker.
func (s *maintenanceOpsService) reserveDeepHealthRun(run func(context.Context) (maintenance.Result, error)) (func(context.Context) (maintenance.Result, error), bool) {
	if !s.deepHealthRun.CompareAndSwap(false, true) {
		return nil, false
	}
	return func(ctx context.Context) (maintenance.Result, error) {
		defer s.deepHealthRun.Store(false)
		return run(ctx)
	}, true
}

// runRegularDeepNZBHealthCheck runs the age-prioritized background batch only
// when no forced sweep or manual run owns the shared deep-health worker.
func (s *maintenanceOpsService) runRegularDeepNZBHealthCheck(ctx context.Context, limit int) (maintenance.Result, bool, error) {
	run, started := s.reserveDeepHealthRun(func(ctx context.Context) (maintenance.Result, error) {
		return runNZBHealthCheckBatch(ctx, s.db, s.workflowSvc, s.publicationSvc, s.logger, limit, false)
	})
	if !started {
		return maintenance.Result{TaskName: "nzb-health-check"}, false, nil
	}
	result, err := run(ctx)
	return result, true, err
}

// nextDeepHealthCheckDelay computes the interval before an item's next deep
// health check, scaled to its age: a fresh release is re-checked as soon as
// an hour later since early corruption is more likely and cheaper to catch,
// while a release that has survived a month is re-checked only every 30
// days, since deep checks issue real NNTP article reads and re-validating
// long-stable content on a short cycle would waste provider connections for
// little benefit.
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

// shouldRunDeepHealthCheck reports whether item is due for a deep check: an
// item never checked or already flagged unhealthy always qualifies, while a
// previously-healthy item is re-checked only once nextDeepHealthCheckDelay's
// age-scaled interval has elapsed since its last check.
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
//
// Status 430 is NOT transient: per RFC 3977 and Newshosting's own support
// docs, it means the specific article is gone (past retention or removed),
// a property of that article, not of the provider's health — it should
// blocklist like any other confirmed-bad content, not retry forever.
// errContainerHeaderUnreadable is checked by its wrapped message text rather
// than unconditionally, since it wraps whatever the underlying read error
// was (including a genuine 430) and unconditionally calling it transient
// meant a permanently-dead article behind a container-read failure would
// never get blocklisted.
func isTransientHealthCheckErr(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := err.Error()
	if strings.Contains(msg, "status 430") {
		return false
	}
	if errors.Is(err, errContainerHeaderUnreadable) {
		return true
	}
	return strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "provider circuit open")
}

const deepHealthPacingDelay = 500 * time.Millisecond

// waitForDeepHealthPacing applies provider-friendly spacing while remaining
// responsive to cancellation and a bounded sweep's work deadline.
func waitForDeepHealthPacing(ctx context.Context, stopAt time.Time) bool {
	if !stopAt.IsZero() && time.Until(stopAt) < deepHealthPacingDelay {
		return false
	}
	timer := time.NewTimer(deepHealthPacingDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// runNZBHealthCheckBatch scans up to limit deep-health candidates (0 means
// no limit) and, for each: repairs a broken publish symlink by
// re-publishing, skips non-VFS-backed symlinks and items not yet due, then
// performs a strict decoded-segment read plus video container validation.
// A definitive failure blocklists the selected release and removes its
// symlinks so the next candidate can be promoted; a transient failure
// (timeout, throttle, momentarily-unreadable container) is left for the
// next scheduled pass rather than penalizing a release that may be fine.
//
// Candidates are processed with a deliberate pacing delay between them (see
// inline comment) to avoid tripping provider rate limiting under a large
// batch, and force bypasses both the symlink-only short-circuit and the
// due-date check so a manually-triggered full sweep validates every row.
func runNZBHealthCheckBatch(ctx context.Context, db *database.DB, workflowSvc *workflow.Service, publicationSvc *library.Publisher, logger zerolog.Logger, limit int, force bool) (maintenance.Result, error) {
	result := maintenance.Result{TaskName: "nzb-health-check"}
	candidates, err := db.ListDeepHealthCandidates(ctx, limit)
	if err != nil {
		logger.Error().Err(err).Msg("health check: query failed")
		return result, err
	}
	result, _ = runDeepHealthCandidates(ctx, db, workflowSvc, publicationSvc, logger, candidates, force, time.Time{})
	return result, nil
}

// runDeepHealthCandidates processes a previously selected, stable candidate
// page. stopAt is checked between candidates so a full sweep can yield after a
// bounded work window; processed reports the contiguous prefix safe to persist
// as its resume cursor.
func runDeepHealthCandidates(ctx context.Context, db *database.DB, workflowSvc *workflow.Service, publicationSvc *library.Publisher, logger zerolog.Logger, candidates []database.DeepHealthCandidate, force bool, stopAt time.Time) (maintenance.Result, int) {
	result := maintenance.Result{TaskName: "nzb-health-check"}
	now := time.Now()
	logger.Info().Int("count", len(candidates)).Bool("force", force).Msg("health check: scanning deep-check candidates")
	resetSeen := make(map[int64]struct{})
	repairedSeen := make(map[int64]struct{})
	processed := 0
	// A started candidate may finish through a cancellation/transient branch.
	// Advancing past that attempt prevents one slow row from pinning the durable
	// sweep forever; its untouched last_checked_at keeps it eligible for regular
	// priority retries.
	markProcessed := func() {
		processed++
	}
	for i, c := range candidates {
		if ctx.Err() != nil || (!stopAt.IsZero() && !time.Now().Before(stopAt)) {
			break
		}
		if i > 0 {
			// Pace deep checks so a large batch (e.g. a manually-triggered
			// full-library sweep) can't fire enough concurrent NNTP requests
			// to trip the provider's rate limiting — that throttling showed
			// up as generic FUSE read EOFs, not recognizable NNTP errors,
			// and caused a wave of false "corrupt content" verdicts across
			// completely unrelated releases within the same few seconds.
			if !waitForDeepHealthPacing(ctx, stopAt) {
				break
			}
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
				markProcessed()
				continue
			}
		}
		if !strings.Contains(c.TargetPath, "/content/") {
			// Not a VFS-backed symlink (e.g. completed-symlinks) — nothing to
			// deep-validate; a resolving symlink is the whole health signal.
			logger.Debug().Int64("libraryItemId", c.LibraryItemID).Str("targetPath", c.TargetPath).
				Msg("health check: non-VFS symlink, skipping deep validation")
			_ = db.RecordHealthCheck(ctx, c.PublicationID, true)
			markProcessed()
			continue
		}
		if !force && !shouldRunDeepHealthCheck(now, c) {
			logger.Debug().Int64("libraryItemId", c.LibraryItemID).Msg("health check: skipping — not due yet")
			markProcessed()
			continue
		}
		if c.NZBDocumentID <= 0 {
			markProcessed()
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
			markProcessed()
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
			magicErr := waitForReadableVideoContainerDirect(magicCtx, db, c.VirtualFileID, 6, 5*time.Second)
			magicCancel()
			if magicErr != nil {
				if isTransientHealthCheckErr(magicErr) {
					if publicationSvc != nil {
						logger.Warn().
							Int64("libraryItemId", c.LibraryItemID).
							Str("title", c.Title).
							Err(magicErr).
							Msg("health check: transient container-read error — re-publishing item before retry")
						if republishErr := publicationSvc.RepublishLibraryItem(ctx, c.LibraryItemID); republishErr != nil {
							logger.Warn().Err(republishErr).Int64("libraryItemId", c.LibraryItemID).Msg("health check: transient container-read republish failed")
						} else {
							retryCtx, retryCancel := context.WithTimeout(ctx, 45*time.Second)
							retryErr := waitForReadableVideoContainerDirect(retryCtx, db, c.VirtualFileID, 6, 5*time.Second)
							retryCancel()
							if retryErr == nil {
								logger.Info().Int64("libraryItemId", c.LibraryItemID).Str("title", c.Title).
									Msg("health check: transient container-read recovered after re-publish")
								_ = db.RecordHealthCheck(ctx, c.PublicationID, true)
								if _, exists := repairedSeen[c.LibraryItemID]; !exists {
									repairedSeen[c.LibraryItemID] = struct{}{}
									result.RepairedItems++
								}
								markProcessed()
								continue
							}
							magicErr = retryErr
						}
					}
					logger.Warn().
						Int64("libraryItemId", c.LibraryItemID).
						Str("title", c.Title).
						Err(magicErr).
						Msg("health check: transient container-read error — skipping, will retry next pass")
					markProcessed()
					continue
				}
				err = fmt.Errorf("invalid video container: %w", magicErr)
			} else {
				logger.Debug().Int64("libraryItemId", c.LibraryItemID).Str("title", c.Title).
					Msg("health check: passed — segments and container valid")
				_ = db.RecordHealthCheck(ctx, c.PublicationID, true)
				checkDecodeIssuesAdvisory(ctx, db, logger, c)
				markProcessed()
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
		// StrictCheckFirstSegments's own error already carries a "strict
		// health: " prefix (via sanitizedSegmentErr); only the
		// "invalid video container" variant built just above doesn't.
		// Unconditionally prepending here produced a doubled
		// "strict health: strict health: ..." reason for every segment
		// failure.
		reason := err.Error()
		if !strings.HasPrefix(strings.ToLower(reason), "strict health:") {
			reason = "strict health: " + reason
		}
		blocklistErr := workflowSvc.FailAndBlocklistRelease(ctx, c.SelectedReleaseID, reason)
		if blocklistErr != nil {
			logger.Error().Err(blocklistErr).Int64("libraryItemId", c.LibraryItemID).Msg("health check: blocklist failed")
		} else if _, exists := resetSeen[c.LibraryItemID]; !exists {
			resetSeen[c.LibraryItemID] = struct{}{}
			result.ResetItems++
		}
		markProcessed()
	}
	return result, processed
}

const (
	deepHealthSweepWorkBudget   = 10 * time.Minute
	deepHealthCheckpointTimeout = 5 * time.Second
	deepHealthSweepStepTimeout  = 15 * time.Minute
	deepHealthCoordinatorTick   = 15 * time.Minute
	deepHealthFullSweepInterval = 168 * time.Hour
)

// deepHealthSweepProgress is the durable keyset checkpoint for one forced
// library sweep. ThroughLibraryItemID freezes its population; subsequent runs
// resume strictly after AfterLibraryItemID until that snapshot is exhausted.
type deepHealthSweepProgress struct {
	StartedAt            time.Time `json:"startedAt"`
	AfterLibraryItemID   int64     `json:"afterLibraryItemId"`
	ThroughLibraryItemID int64     `json:"throughLibraryItemId"`
	ScannedRows          int       `json:"scannedRows"`
}

func decodeDeepHealthSweepProgress(raw string) (deepHealthSweepProgress, error) {
	var progress deepHealthSweepProgress
	if err := json.Unmarshal([]byte(raw), &progress); err != nil {
		return progress, fmt.Errorf("decode deep health sweep progress: %w", err)
	}
	if progress.StartedAt.IsZero() || progress.AfterLibraryItemID < 0 || progress.ThroughLibraryItemID < progress.AfterLibraryItemID || progress.ScannedRows < 0 {
		return progress, errors.New("invalid deep health sweep progress")
	}
	return progress, nil
}

func saveDeepHealthSweepProgress(ctx context.Context, db *database.DB, progress deepHealthSweepProgress) error {
	raw, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	return db.TouchMaintenanceCursor(ctx, taskNZBHealthCheckProgress, string(raw))
}

// shouldRunDeepHealthSweep reports whether the coordinator has unfinished
// progress or the last completed weekly sweep is due. Progress always wins so a
// recent completion timestamp cannot strand a checkpoint left by a partial
// completion cleanup.
func shouldRunDeepHealthSweep(ctx context.Context, db *database.DB, now time.Time) (bool, error) {
	progress, err := db.GetMaintenanceCursor(ctx, taskNZBHealthCheckProgress)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(progress) != "" {
		return true, nil
	}
	return shouldRunRecentOnStartup(ctx, db, taskNZBHealthCheck, deepHealthFullSweepInterval, 0, now), nil
}

// loadOrStartDeepHealthSweep restores an unfinished snapshot or records a new
// one before any candidate work begins. Invalid internal state is discarded and
// rebuilt so one damaged cursor cannot permanently disable maintenance.
func loadOrStartDeepHealthSweep(ctx context.Context, db *database.DB, logger zerolog.Logger) (deepHealthSweepProgress, error) {
	raw, err := db.GetMaintenanceCursor(ctx, taskNZBHealthCheckProgress)
	if err != nil {
		return deepHealthSweepProgress{}, err
	}
	if strings.TrimSpace(raw) != "" {
		progress, decodeErr := decodeDeepHealthSweepProgress(raw)
		if decodeErr == nil {
			return progress, nil
		}
		logger.Warn().Err(decodeErr).Msg("health check: resetting invalid sweep progress")
		if err := db.DeleteMaintenanceCursor(ctx, taskNZBHealthCheckProgress); err != nil {
			return deepHealthSweepProgress{}, err
		}
	}

	upperBound, err := db.DeepHealthSweepUpperBound(ctx)
	if err != nil {
		return deepHealthSweepProgress{}, err
	}
	progress := deepHealthSweepProgress{
		StartedAt:            time.Now().UTC(),
		ThroughLibraryItemID: upperBound,
	}
	if err := saveDeepHealthSweepProgress(ctx, db, progress); err != nil {
		return deepHealthSweepProgress{}, err
	}
	return progress, nil
}

func addMaintenanceResult(total *maintenance.Result, part maintenance.Result) {
	total.ScannedRows += part.ScannedRows
	total.ResetItems += part.ResetItems
	total.RepairedItems += part.RepairedItems
	total.DegradedItems += part.DegradedItems
}

// runNZBHealthSweepStep advances a forced sweep for at most one bounded work
// window. Every completed page (including a partial final page) is checkpointed
// with a short cancellation-independent DB context, allowing timeout, restart,
// or deployment to resume without rescanning the full library.
func runNZBHealthSweepStep(ctx context.Context, db *database.DB, workflowSvc *workflow.Service, publicationSvc *library.Publisher, logger zerolog.Logger) (maintenance.Result, error) {
	result := maintenance.Result{TaskName: "nzb-health-check"}
	progress, err := loadOrStartDeepHealthSweep(ctx, db, logger)
	if err != nil {
		return result, err
	}
	stopAt := time.Now().Add(deepHealthSweepWorkBudget)

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		candidates, err := db.ListDeepHealthCandidatesPage(ctx, progress.AfterLibraryItemID, progress.ThroughLibraryItemID, backgroundDeepHealthBatchSize)
		if err != nil {
			return result, err
		}
		if len(candidates) == 0 {
			reset, err := resetSampleOnlyPublications(ctx, db, workflowSvc, logger)
			if err != nil {
				return result, err
			}
			result.ResetItems += reset
			completionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deepHealthCheckpointTimeout)
			defer cancel()
			if err := db.TouchMaintenanceCursor(completionCtx, taskNZBHealthCheck, time.Now().UTC().Format(time.RFC3339)); err != nil {
				return result, err
			}
			if err := db.DeleteMaintenanceCursor(completionCtx, taskNZBHealthCheckProgress); err != nil {
				return result, err
			}
			logger.Info().Int("scanned", progress.ScannedRows).Int("reset", result.ResetItems).
				Msg("health check: full sweep complete")
			return result, nil
		}

		pageResult, processed := runDeepHealthCandidates(ctx, db, workflowSvc, publicationSvc, logger, candidates, true, stopAt)
		addMaintenanceResult(&result, pageResult)
		if processed > 0 {
			progress.AfterLibraryItemID = candidates[processed-1].LibraryItemID
			progress.ScannedRows += pageResult.ScannedRows
			checkpointCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deepHealthCheckpointTimeout)
			err = saveDeepHealthSweepProgress(checkpointCtx, db, progress)
			cancel()
			if err != nil {
				return result, err
			}
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if processed < len(candidates) || !time.Now().Before(stopAt) {
			logger.Info().Int64("afterLibraryItemId", progress.AfterLibraryItemID).
				Int64("throughLibraryItemId", progress.ThroughLibraryItemID).
				Int("scanned", progress.ScannedRows).
				Msg("health check: full sweep checkpointed")
			return result, nil
		}
	}
}

// resetSampleOnlyPublications blocklists available releases whose only video
// file is a sample. It runs once after the forced candidate snapshot is fully
// consumed, before the sweep completion cursor is advanced.
func resetSampleOnlyPublications(ctx context.Context, db *database.DB, workflowSvc *workflow.Service, logger zerolog.Logger) (int, error) {
	if workflowSvc == nil {
		return 0, errors.New("workflow service unavailable")
	}
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
	if err != nil {
		return 0, err
	}
	defer sampleRows.Close()

	resetSeen := make(map[int64]struct{})
	reset := 0
	for sampleRows.Next() {
		var libID, selectedReleaseID int64
		var title string
		if err := sampleRows.Scan(&libID, &title, &selectedReleaseID); err != nil {
			return reset, err
		}
		if _, exists := resetSeen[libID]; exists {
			continue
		}
		// Blocklisting prevents a small candidate pool from immediately selecting
		// the same sample-only release again after a plain state reset.
		logger.Warn().Int64("libraryItemId", libID).Str("title", title).
			Msg("health check: only sample file published — blocklisting release and promoting next")
		if resetErr := workflowSvc.FailAndBlocklistRelease(ctx, selectedReleaseID, "sample-only release"); resetErr != nil {
			logger.Error().Err(resetErr).Int64("libraryItemId", libID).Msg("health check: sample blocklist failed")
			continue
		}
		resetSeen[libID] = struct{}{}
		reset++
	}
	return reset, sampleRows.Err()
}

// errContainerHeaderUnreadable means the header bytes themselves could not be
// obtained (open/read failure) — this is inconclusive about the file's
// actual content and can be caused by NNTP provider throttling or a
// momentarily stale VFS cache entry, not just genuine corruption. Callers
// should treat it like a transient error (retry next pass) rather than
// blocklisting a release on the strength of it alone.
var errContainerHeaderUnreadable = errors.New("container header unreadable")

// readContainerHeader opens path and validates its leading bytes against
// known video container magic numbers. A bounded read deadline guards
// against a hung FUSE read blocking the health check indefinitely; any
// open/read failure is wrapped in errContainerHeaderUnreadable so callers
// can distinguish "content not yet readable" from "content is corrupt".
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

// decodeIssuesProbeBytes bounds how much of a published file's decoded
// bytes checkDecodeIssuesAdvisory reads for the ffmpeg decode-error scan --
// generous enough to cover several seconds of most video bitrates, small
// enough to stay a bounded background-priority read rather than a
// meaningful fraction of the file.
const decodeIssuesProbeBytes = 48 * 1024 * 1024

// checkDecodeIssuesAdvisory is a best-effort, ADVISORY-ONLY health-check
// addition: none of Drakkar's existing checks (yEnc/CRC segment validation,
// container-magic-byte check) actually decode a single video frame, so a
// release with a genuinely corrupt video bitstream but valid article bytes
// and a valid container header passes both today. This runs
// mediaprobe.DetectDecodeIssues against a bounded prefix and just logs a
// warning when it finds something -- it deliberately does NOT blocklist or
// otherwise change the item's state, since the underlying heuristic hasn't
// been validated against real corrupt-vs-clean samples the way this
// session's RAR5 decryption work was, and a false positive here would
// wrongly discard a perfectly good release. Silently does nothing for
// anything other than a "direct_nzb" virtual file (see
// database.DB.PrefixBytesBackground's doc comment for why stored_rar --
// including password-decrypted RAR -- isn't supported by this bounded-read
// path yet) or when ffmpeg isn't installed.
func checkDecodeIssuesAdvisory(ctx context.Context, db *database.DB, logger zerolog.Logger, c database.DeepHealthCandidate) {
	data, ok, err := db.PrefixBytesBackground(ctx, c.VirtualFileID, decodeIssuesProbeBytes)
	if err != nil || !ok || len(data) == 0 {
		return
	}
	issues, err := mediaprobe.DetectDecodeIssues(ctx, data, 5)
	if err != nil || len(issues) == 0 {
		return
	}
	logger.Warn().
		Int64("libraryItemId", c.LibraryItemID).
		Str("title", c.Title).
		Strs("decodeIssues", issues).
		Msg("health check: ffmpeg reported possible decode issues (advisory only -- not blocklisting)")
}

// readContainerHeaderDirect validates the same leading-bytes magic-number
// check as readContainerHeader, but reads straight from the backend
// (db.OpenVirtualMediaFile) instead of through the host symlink path.
//
// The periodic deep health-check batch (up to ~50 candidates per pass) used
// to go through os.Open on the real symlink path -- the exact same
// kernel-FUSE-mount -> rclone -> WebDAV round trip a real player uses, which
// creates a real tracked read-ahead session on the Drakkar side (see
// internal/dav's virtualFile.ensureOpen). ActiveCount() divides read-ahead
// parallelism across every such session, so this diagnostic check competed
// directly with real playback for the same connection budget -- and a
// broken file's retry loop (waitForReadableVideoContainer, up to 6 attempts
// x 5s, then a republish attempt, then another 6x5s) could hold that
// contention open for up to ~90 seconds. Confirmed live 2026-08-11: two
// entirely unrelated files (a health-check candidate and a real concurrent
// playback stream) hit hard EOF from rclone at the exact same second.
// Reading directly from the backend needs only the virtual file's own byte
// range, never touches FUSE/rclone/WebDAV, and registers no session at all,
// so it cannot contend with real playback for read-ahead parallelism.
//
// Trade-off: this no longer exercises the full symlink/FUSE/rclone plumbing
// the way the path-based check did -- it only proves the underlying data is
// readable, not that the mount serving it to Plex/Jellyfin is healthy. That
// full round-trip is still exercised naturally by every real playback
// request, so it isn't going unverified, just not via this specific
// high-frequency batch check.
func readContainerHeaderDirect(ctx context.Context, db *database.DB, virtualFileID int64) error {
	vf, err := db.OpenVirtualMediaFile(ctx, virtualFileID)
	if err != nil {
		return fmt.Errorf("%w: open: %v", errContainerHeaderUnreadable, err)
	}
	buf := make([]byte, 12)
	n, err := vf.ReadAt(ctx, buf, 0)
	if err != nil && n < 4 {
		return fmt.Errorf("%w: read header: %v", errContainerHeaderUnreadable, err)
	}
	return validateVideoContainerHeader(buf[:n])
}

// waitForReadableVideoContainerDirect is waitForReadableVideoContainer's
// direct-backend counterpart -- see readContainerHeaderDirect for why the
// periodic deep health check uses this instead of the path-based version.
func waitForReadableVideoContainerDirect(ctx context.Context, db *database.DB, virtualFileID int64, attempts int, delay time.Duration) error {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := readContainerHeaderDirect(ctx, db, virtualFileID); err == nil {
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

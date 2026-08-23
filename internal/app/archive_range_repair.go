package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/library"
	"github.com/drakkar-media/drakkar/internal/maintenance"
	"github.com/drakkar-media/drakkar/internal/nzb"
	"github.com/rs/zerolog"
)

// archiveRangeRepairService owns the shared archive-range repair worker,
// gated the same way maintenanceOpsService gates the deep NZB health check:
// one atomic reservation serializes the scheduled sweep and any manually
// triggered run.
type archiveRangeRepairService struct {
	db             *database.DB
	publicationSvc *library.Publisher
	logger         zerolog.Logger
	repairRun      atomic.Bool
}

// TryStartArchiveRangeRepairSweep reserves the shared worker and returns its
// bounded, resumable sweep step. Returns started=false while another run
// (scheduled or manual) already owns the reservation.
func (s *archiveRangeRepairService) TryStartArchiveRangeRepairSweep() (run func(context.Context) (maintenance.Result, error), started bool) {
	if !s.repairRun.CompareAndSwap(false, true) {
		return nil, false
	}
	return func(ctx context.Context) (maintenance.Result, error) {
		defer s.repairRun.Store(false)
		return runArchiveRangeRepairSweepStep(ctx, s.db, s.publicationSvc, s.logger)
	}, true
}

// RepairArchiveRangeForRelease reserves the shared worker to repair one
// specific, already-known selectedReleaseID immediately, bypassing the
// sweep's keyset cursor entirely. Used to fix a specific reported release
// without waiting for the gradual background sweep to reach it.
func (s *archiveRangeRepairService) RepairArchiveRangeForRelease(ctx context.Context, selectedReleaseID int64) (changed bool, started bool, err error) {
	if !s.repairRun.CompareAndSwap(false, true) {
		return false, false, nil
	}
	defer s.repairRun.Store(false)
	changed, err = repairArchiveRangeCandidate(ctx, s.db, s.publicationSvc, selectedReleaseID)
	return changed, true, err
}

// repairArchiveRangeCandidate re-imports selectedReleaseID from its
// already-stored NZB document (no re-download, no re-selection) so
// inspectRARArchive recomputes archive_ranges/archive_entries/virtual_files
// against live NNTP data with the reconcileStoreMethodSize ordering fix
// applied. Safe to run on an unaffected release: re-inspection of correct
// data reproduces the same values, so changed reports false. Restores the
// symlink and available/published state afterward via RepublishLibraryItem,
// since ImportSelectedReleaseNZB's delete-and-recreate of virtual_files
// cascades away the existing symlink_publications row.
func repairArchiveRangeCandidate(ctx context.Context, db *database.DB, publicationSvc *library.Publisher, selectedReleaseID int64) (bool, error) {
	before, err := db.SumVirtualFileSizeForRelease(ctx, selectedReleaseID)
	if err != nil {
		return false, fmt.Errorf("read pre-repair size for release %d: %w", selectedReleaseID, err)
	}
	doc, err := db.GetStoredNZBDocument(ctx, selectedReleaseID)
	if err != nil {
		return false, err
	}
	imported, err := nzb.BuildImportedNZB(doc.FileName, doc.XML, fmt.Sprintf("archive-range-repair:%d", selectedReleaseID), doc.ExternalURL)
	if err != nil {
		return false, err
	}
	snapshot, err := db.ImportSelectedReleaseNZB(ctx, selectedReleaseID, imported)
	if err != nil {
		return false, err
	}
	if publicationSvc != nil {
		if err := publicationSvc.RepublishLibraryItem(ctx, snapshot.LibraryItemID); err != nil {
			return false, fmt.Errorf("republish library item %d after repair: %w", snapshot.LibraryItemID, err)
		}
	}
	after, err := db.SumVirtualFileSizeForRelease(ctx, selectedReleaseID)
	if err != nil {
		return false, fmt.Errorf("read post-repair size for release %d: %w", selectedReleaseID, err)
	}
	return before != after, nil
}

const (
	archiveRangeRepairPacingDelay     = 2 * time.Second
	archiveRangeRepairBatchSize       = 25
	archiveRangeRepairWorkBudget      = 10 * time.Minute
	archiveRangeRepairCheckpointWait  = 5 * time.Second
	archiveRangeRepairCoordinatorTick = 15 * time.Minute
)

// archiveRangeRepairProgress is the durable keyset checkpoint for one sweep
// pass across every multi-volume RAR archive, mirroring
// deepHealthSweepProgress's shape (see nzb_health_check.go).
type archiveRangeRepairProgress struct {
	StartedAt        time.Time `json:"startedAt"`
	AfterArchiveID   int64     `json:"afterArchiveId"`
	ThroughArchiveID int64     `json:"throughArchiveId"`
	ScannedRows      int       `json:"scannedRows"`
	RepairedItems    int       `json:"repairedItems"`
}

func decodeArchiveRangeRepairProgress(raw string) (archiveRangeRepairProgress, error) {
	var progress archiveRangeRepairProgress
	if err := json.Unmarshal([]byte(raw), &progress); err != nil {
		return progress, fmt.Errorf("decode archive range repair progress: %w", err)
	}
	if progress.StartedAt.IsZero() || progress.AfterArchiveID < 0 || progress.ThroughArchiveID < progress.AfterArchiveID || progress.ScannedRows < 0 {
		return progress, errors.New("invalid archive range repair progress")
	}
	return progress, nil
}

func saveArchiveRangeRepairProgress(ctx context.Context, db *database.DB, progress archiveRangeRepairProgress) error {
	raw, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	return db.TouchMaintenanceCursor(ctx, taskArchiveRangeRepairProgress, string(raw))
}

// loadOrStartArchiveRangeRepairSweep restores an unfinished checkpoint or
// starts a fresh one, discarding an unreadable cursor rather than letting one
// damaged row permanently disable the sweep (mirrors
// loadOrStartDeepHealthSweep).
func loadOrStartArchiveRangeRepairSweep(ctx context.Context, db *database.DB, logger zerolog.Logger) (archiveRangeRepairProgress, error) {
	raw, err := db.GetMaintenanceCursor(ctx, taskArchiveRangeRepairProgress)
	if err != nil {
		return archiveRangeRepairProgress{}, err
	}
	if strings.TrimSpace(raw) != "" {
		progress, decodeErr := decodeArchiveRangeRepairProgress(raw)
		if decodeErr == nil {
			return progress, nil
		}
		logger.Warn().Err(decodeErr).Msg("archive range repair: resetting invalid sweep progress")
		if err := db.DeleteMaintenanceCursor(ctx, taskArchiveRangeRepairProgress); err != nil {
			return archiveRangeRepairProgress{}, err
		}
	}
	upperBound, err := db.ArchiveRangeRepairSweepUpperBound(ctx)
	if err != nil {
		return archiveRangeRepairProgress{}, err
	}
	progress := archiveRangeRepairProgress{
		StartedAt:        time.Now().UTC(),
		ThroughArchiveID: upperBound,
	}
	if err := saveArchiveRangeRepairProgress(ctx, db, progress); err != nil {
		return archiveRangeRepairProgress{}, err
	}
	return progress, nil
}

// waitForArchiveRangeRepairPacing spaces out repairs so a large batch can't
// fire enough concurrent NNTP header fetches to trip provider rate limiting
// -- see waitForDeepHealthPacing's matching comment for the observed failure
// mode this avoids. Longer than the health check's pacing delay since each
// repair does a full re-import (every volume's header, not one segment).
func waitForArchiveRangeRepairPacing(ctx context.Context, stopAt time.Time) bool {
	if !stopAt.IsZero() && time.Until(stopAt) < archiveRangeRepairPacingDelay {
		return false
	}
	timer := time.NewTimer(archiveRangeRepairPacingDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// runArchiveRangeRepairSweepStep advances the sweep for at most one bounded
// work window, checkpointing after every page (including a partial final
// page) so a timeout, restart, or deployment resumes without re-processing
// already-repaired archives.
func runArchiveRangeRepairSweepStep(ctx context.Context, db *database.DB, publicationSvc *library.Publisher, logger zerolog.Logger) (maintenance.Result, error) {
	result := maintenance.Result{TaskName: "archive-range-repair"}
	progress, err := loadOrStartArchiveRangeRepairSweep(ctx, db, logger)
	if err != nil {
		return result, err
	}
	stopAt := time.Now().Add(archiveRangeRepairWorkBudget)

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		candidates, err := db.ListArchiveRangeRepairCandidatesPage(ctx, progress.AfterArchiveID, progress.ThroughArchiveID, archiveRangeRepairBatchSize)
		if err != nil {
			return result, err
		}
		if len(candidates) == 0 {
			completionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), archiveRangeRepairCheckpointWait)
			defer cancel()
			if err := db.TouchMaintenanceCursor(completionCtx, taskArchiveRangeRepair, time.Now().UTC().Format(time.RFC3339)); err != nil {
				return result, err
			}
			if err := db.DeleteMaintenanceCursor(completionCtx, taskArchiveRangeRepairProgress); err != nil {
				return result, err
			}
			logger.Info().Int("scanned", progress.ScannedRows).Int("repaired", progress.RepairedItems).
				Msg("archive range repair: full sweep complete")
			return result, nil
		}

		processed, pageRepaired := 0, 0
		for i, c := range candidates {
			if ctx.Err() != nil || !time.Now().Before(stopAt) {
				break
			}
			if i > 0 && !waitForArchiveRangeRepairPacing(ctx, stopAt) {
				break
			}
			changed, repairErr := repairArchiveRangeCandidate(ctx, db, publicationSvc, c.SelectedReleaseID)
			if repairErr != nil {
				logger.Warn().Err(repairErr).Int64("archiveId", c.ArchiveID).Int64("selectedReleaseId", c.SelectedReleaseID).
					Msg("archive range repair: candidate failed — leaving its stored data untouched, will retry next sweep")
			} else if changed {
				pageRepaired++
				logger.Info().Int64("archiveId", c.ArchiveID).Int64("selectedReleaseId", c.SelectedReleaseID).
					Msg("archive range repair: corrected volume-boundary data")
			}
			processed++
		}
		result.ScannedRows += processed
		result.RepairedItems += pageRepaired
		if processed > 0 {
			progress.AfterArchiveID = candidates[processed-1].ArchiveID
			progress.ScannedRows += processed
			progress.RepairedItems += pageRepaired
			checkpointCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), archiveRangeRepairCheckpointWait)
			err = saveArchiveRangeRepairProgress(checkpointCtx, db, progress)
			cancel()
			if err != nil {
				return result, err
			}
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if processed < len(candidates) || !time.Now().Before(stopAt) {
			logger.Info().Int64("afterArchiveId", progress.AfterArchiveID).
				Int64("throughArchiveId", progress.ThroughArchiveID).
				Int("scanned", progress.ScannedRows).Int("repaired", progress.RepairedItems).
				Msg("archive range repair: sweep checkpointed")
			return result, nil
		}
	}
}

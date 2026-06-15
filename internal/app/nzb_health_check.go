package app

import (
	"context"
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

// runNZBHealthCheck first repairs bad symlinks by re-publishing them, then
// performs a heavier decoded-segment validation on available items and resets
// broken releases for re-queue. Sample-only publications are also reset.
func runNZBHealthCheck(ctx context.Context, db *database.DB, workflowSvc *workflow.Service, publicationSvc *library.Publisher, logger zerolog.Logger) (maintenance.Result, error) {
	result := maintenance.Result{TaskName: "nzb-health-check"}

	if publicationSvc != nil {
		entries, err := db.ListHealthEntries(ctx)
		if err == nil {
			repairedSeen := make(map[int64]struct{})
			for _, entry := range entries {
				if ctx.Err() != nil {
					break
				}
				if database.CheckSymlinkHealth(entry.LibraryPath, entry.TargetPath) {
					continue
				}
				logger.Warn().
					Int64("libraryItemId", entry.LibraryItemID).
					Str("libraryPath", entry.LibraryPath).
					Msg("health check: broken symlink publication — re-publishing item")
				if err := publicationSvc.RepublishLibraryItem(ctx, entry.LibraryItemID); err != nil {
					logger.Error().Err(err).Int64("libraryItemId", entry.LibraryItemID).Msg("health check: republish failed")
					continue
				}
				if _, exists := repairedSeen[entry.LibraryItemID]; !exists {
					repairedSeen[entry.LibraryItemID] = struct{}{}
					result.RepairedItems++
				}
			}
		}
	}

	type candidate struct {
		libraryItemID int64
		nzbDocumentID int64
		title         string
	}

	rows, err := db.SQL.QueryContext(ctx, `
		SELECT DISTINCT ON (qi.library_item_id)
		    qi.library_item_id,
		    nd.id,
		    li.title
		FROM queue_items qi
		JOIN library_items li ON li.id = qi.library_item_id
		JOIN selected_releases sr ON sr.id = qi.selected_release_id
		JOIN nzb_documents nd ON nd.selected_release_id = sr.id
		WHERE qi.state = 'available' AND li.available = true
		ORDER BY qi.library_item_id ASC, qi.id DESC`)
	if err != nil {
		logger.Error().Err(err).Msg("health check: query failed")
		return result, err
	}
	defer rows.Close()

	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.libraryItemID, &c.nzbDocumentID, &c.title); err != nil {
			continue
		}
		if c.nzbDocumentID > 0 {
			candidates = append(candidates, c)
		}
	}
	_ = rows.Err()

	result.ScannedRows = len(candidates)
	logger.Info().Int("count", len(candidates)).Msg("health check: scanning available library items")
	resetSeen := make(map[int64]struct{})
	for _, c := range candidates {
		if ctx.Err() != nil {
			break
		}
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := db.StrictCheckFirstSegments(checkCtx, c.nzbDocumentID)
		cancel()
		if err == nil {
			continue
		}
		logger.Warn().
			Int64("libraryItemId", c.libraryItemID).
			Str("title", c.title).
			Err(err).
			Msg("health check: strict NZB validation failed — resetting item for re-queue")
		if resetErr := workflowSvc.ResetLibraryItem(ctx, c.libraryItemID); resetErr != nil {
			logger.Error().Err(resetErr).Int64("libraryItemId", c.libraryItemID).Msg("health check: reset failed")
		} else if _, exists := resetSeen[c.libraryItemID]; !exists {
			resetSeen[c.libraryItemID] = struct{}{}
			result.ResetItems++
		}
	}

	sampleRows, err := db.SQL.QueryContext(ctx, `
		SELECT DISTINCT qi.library_item_id, li.title
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
			var libID int64
			var title string
			if err := sampleRows.Scan(&libID, &title); err != nil {
				continue
			}
			if _, exists := resetSeen[libID]; exists {
				continue
			}
			logger.Warn().Int64("libraryItemId", libID).Str("title", title).
				Msg("health check: only sample file published — resetting item for re-queue")
			if resetErr := workflowSvc.ResetLibraryItem(ctx, libID); resetErr != nil {
				logger.Error().Err(resetErr).Int64("libraryItemId", libID).Msg("health check: sample reset failed")
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

package app

import (
	"context"
	"strings"
	"time"

	"github.com/hjongedijk/drakkar/internal/database"
	"github.com/hjongedijk/drakkar/internal/maintenance"
	"github.com/hjongedijk/drakkar/internal/workflow"
	"github.com/rs/zerolog"
)

type maintenanceOpsService struct {
	base        *maintenance.Service
	db          *database.DB
	workflowSvc *workflow.Service
	logger      zerolog.Logger
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
	return runNZBHealthCheck(ctx, s.db, s.workflowSvc, s.logger)
}

// runNZBHealthCheck scans all available library items and resets any whose
// first/last NNTP segments are no longer retrievable or whose only published
// file is a sample clip.
func runNZBHealthCheck(ctx context.Context, db *database.DB, workflowSvc *workflow.Service, logger zerolog.Logger) (maintenance.Result, error) {
	result := maintenance.Result{TaskName: "nzb-health-check"}
	sizer, ok := db.SegmentFetcher.(database.SegmentSizer)
	if !ok || sizer == nil {
		return result, nil
	}

	type candidate struct {
		libraryItemID int64
		title         string
		firstMsgID    string
		lastMsgID     string
	}

	rows, err := db.SQL.QueryContext(ctx, `
		SELECT DISTINCT ON (qi.library_item_id)
		    qi.library_item_id,
		    li.title,
		    (SELECT ns.message_id FROM nzb_files nf JOIN nzb_segments ns ON ns.nzb_file_id=nf.id
		     WHERE nf.nzb_document_id=nd.id ORDER BY nf.id ASC, ns.segment_number ASC LIMIT 1),
		    (SELECT ns.message_id FROM nzb_files nf JOIN nzb_segments ns ON ns.nzb_file_id=nf.id
		     WHERE nf.nzb_document_id=nd.id ORDER BY nf.id ASC, ns.segment_number DESC LIMIT 1)
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
		if err := rows.Scan(&c.libraryItemID, &c.title, &c.firstMsgID, &c.lastMsgID); err != nil {
			continue
		}
		if c.firstMsgID != "" {
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
		isMissing := func(msgID string) bool {
			checkCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			_, err := sizer.DecodedSize(checkCtx, msgID)
			cancel()
			if err == nil {
				return false
			}
			msg := err.Error()
			return strings.Contains(msg, "not found") || strings.Contains(msg, "430")
		}
		broken := isMissing(c.firstMsgID) || (c.lastMsgID != c.firstMsgID && isMissing(c.lastMsgID))
		if !broken {
			continue
		}
		logger.Warn().
			Int64("libraryItemId", c.libraryItemID).
			Str("title", c.title).
			Msg("health check: segment missing — resetting item for re-queue")
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

package database

import (
	"context"
	"fmt"
	"log/slog"
)

// SegmentSizer can return the actual decoded byte size of an NNTP article.
type SegmentSizer interface {
	DecodedSize(ctx context.Context, messageID string) (int64, error)
}

// CalibrateAllNZBOffsets runs CalibrateNZBOffsets for every NZB document in the
// database. Called once at startup to fix any NZBs imported with the old
// estimated offset factor.
func (db *DB) CalibrateAllNZBOffsets(ctx context.Context) error {
	rows, err := db.SQL.QueryContext(ctx, `SELECT id FROM nzb_documents`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := db.CalibrateNZBOffsets(ctx, id); err != nil {
			slog.Warn("calibrate all: failed for document", "nzb_document_id", id, "err", err)
		}
	}
	return nil
}

// CalibrateNZBOffsets corrects segment decoded offsets for all files in an NZB
// document by fetching the first segment of each file and measuring its actual
// decoded size. This replaces the estimated offsets (0.74 or 0.97 factor) with
// values derived from the real yEnc payload size.
func (db *DB) CalibrateNZBOffsets(ctx context.Context, nzbDocumentID int64) error {
	sizer, ok := db.SegmentFetcher.(SegmentSizer)
	if !ok || sizer == nil {
		return nil
	}

	rows, err := db.SQL.QueryContext(ctx, `
		SELECT nf.id,
		       (SELECT ns.message_id FROM nzb_segments ns WHERE ns.nzb_file_id = nf.id ORDER BY ns.segment_number LIMIT 1),
		       (SELECT ns.decoded_end_offset - ns.decoded_start_offset FROM nzb_segments ns WHERE ns.nzb_file_id = nf.id ORDER BY ns.segment_number LIMIT 1)
		FROM nzb_files nf
		WHERE nf.nzb_document_id = $1`, nzbDocumentID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type fileInfo struct {
		id        int64
		msgID     string
		estSize   int64
	}
	var files []fileInfo
	for rows.Next() {
		var f fileInfo
		if err := rows.Scan(&f.id, &f.msgID, &f.estSize); err != nil {
			return err
		}
		if f.msgID != "" && f.estSize > 0 {
			files = append(files, f)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, f := range files {
		actual, err := sizer.DecodedSize(ctx, f.msgID)
		if err != nil {
			slog.Warn("calibrate: could not fetch first segment", "nzb_file_id", f.id, "err", err)
			continue
		}
		if actual <= 0 {
			continue
		}
		// Skip if the stored estimate is already within 2% of the actual size.
		// This avoids re-calibrating files whose offsets are already good.
		diff := actual - f.estSize
		if diff < 0 {
			diff = -diff
		}
		if f.estSize > 0 && float64(diff)/float64(f.estSize) < 0.02 {
			continue
		}
		if err := db.rescaleFileSegments(ctx, f.id, f.estSize, actual); err != nil {
			return fmt.Errorf("rescale nzb_file %d: %w", f.id, err)
		}
		slog.Info("calibrate: corrected segment offsets",
			"nzb_file_id", f.id,
			"estimated", f.estSize,
			"actual", actual)
	}
	return nil
}

func (db *DB) rescaleFileSegments(ctx context.Context, nzbFileID, estimatedSize, actualSize int64) error {
	// All non-last segments share the same decoded size. Assign exact uniform
	// boundaries based on actualSize (measured from the real yEnc payload of
	// segment 1) so there are no gaps or overlaps between segments.
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Rewrite decoded offsets uniformly: segment k → [k*actualSize, (k+1)*actualSize].
	// The last segment gets whatever remains up to its estimated end (scaled).
	_, err = tx.ExecContext(ctx, `
		WITH numbered AS (
		    SELECT id, segment_number,
		           ROW_NUMBER() OVER (ORDER BY segment_number) - 1 AS idx,
		           COUNT(*) OVER () AS total
		    FROM nzb_segments
		    WHERE nzb_file_id = $1
		)
		UPDATE nzb_segments ns SET
		    decoded_start_offset = n.idx * $2,
		    decoded_end_offset   = CASE
		        WHEN n.idx = n.total - 1
		            THEN n.idx * $2 + ROUND((ns.decoded_end_offset - ns.decoded_start_offset)::numeric * $2 / $3)
		        ELSE (n.idx + 1) * $2
		    END
		FROM numbered n
		WHERE ns.id = n.id`,
		nzbFileID, actualSize, estimatedSize)
	if err != nil {
		return err
	}

	// Rewrite virtual_file_ranges using the same uniform boundaries.
	_, err = tx.ExecContext(ctx, `
		WITH numbered AS (
		    SELECT ns.id as seg_id,
		           ROW_NUMBER() OVER (PARTITION BY ns.nzb_file_id ORDER BY ns.segment_number) - 1 AS idx,
		           COUNT(*) OVER (PARTITION BY ns.nzb_file_id) AS total,
		           ns.decoded_end_offset - ns.decoded_start_offset AS old_size
		    FROM nzb_segments ns
		    WHERE ns.nzb_file_id = $1
		)
		UPDATE virtual_file_ranges vfr SET
		    range_start = n.idx * $2,
		    range_end   = CASE
		        WHEN n.idx = n.total - 1
		            THEN n.idx * $2 + ROUND(n.old_size::numeric * $2 / $3)
		        ELSE (n.idx + 1) * $2
		    END
		FROM numbered n
		WHERE vfr.nzb_segment_id = n.seg_id`,
		nzbFileID, actualSize, estimatedSize)
	if err != nil {
		return err
	}

	// Update virtual_file size_bytes from the corrected max range_end.
	_, err = tx.ExecContext(ctx, `
		UPDATE virtual_files vf SET
		    size_bytes = (
		        SELECT COALESCE(MAX(vfr.range_end), 0)
		        FROM virtual_file_ranges vfr
		        WHERE vfr.virtual_file_id = vf.id
		    )
		WHERE id IN (
		    SELECT DISTINCT vfr.virtual_file_id
		    FROM virtual_file_ranges vfr
		    JOIN nzb_segments ns ON ns.id = vfr.nzb_segment_id
		    WHERE ns.nzb_file_id = $1
		)`, nzbFileID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

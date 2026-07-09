package database

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (db *DB) ListSymlinkPublicationRecords(ctx context.Context) ([]SymlinkPublicationRecord, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select id, library_path, target_path
		from symlink_publications
		order by id asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SymlinkPublicationRecord
	for rows.Next() {
		var item SymlinkPublicationRecord
		if err := rows.Scan(&item.ID, &item.LibraryPath, &item.TargetPath); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) DeleteSymlinkPublication(ctx context.Context, publicationID int64) error {
	_, err := db.SQL.ExecContext(ctx, `delete from symlink_publications where id = $1`, publicationID)
	return err
}

func (db *DB) TouchMaintenanceCursor(ctx context.Context, taskName string, cursor string) error {
	_, err := db.SQL.ExecContext(ctx, `
		insert into maintenance_cursors (task_name, cursor)
		values ($1, $2)
		on conflict (task_name)
		do update set cursor = excluded.cursor, updated_at = now()`, taskName, cursor,
	)
	return err
}

func (db *DB) GetMaintenanceCursor(ctx context.Context, taskName string) (string, error) {
	var cursor string
	err := db.SQL.QueryRowContext(ctx, `
		select cursor
		from maintenance_cursors
		where task_name = $1`, taskName,
	).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return cursor, nil
}

// PruneStaleReleaseCandidates deletes release_candidates rows older than
// olderThan that were never selected and are not referenced by
// selected_releases (which would cascade-delete real grab history). It runs
// in batches to avoid holding a long-lived lock on a large table.
func (db *DB) PruneStaleReleaseCandidates(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	const batchSize = 5000
	var total int64
	for {
		res, err := db.SQL.ExecContext(ctx, `
			delete from release_candidates
			where id in (
				select rc.id
				from release_candidates rc
				where rc.selected = false
				  and rc.created_at < $1
				  and not exists (
					select 1 from selected_releases sr where sr.release_candidate_id = rc.id
				  )
				limit $2
			)`, cutoff, batchSize)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < batchSize {
			return total, nil
		}
	}
}

// PruneOrphanedSelectedReleases deletes selected_releases rows that no
// queue_item points to anymore — leftover from a candidate that was
// abandoned (moved on to search again) via a path that didn't call
// FailSelectedReleaseAndPromoteNext/ResetLibraryItemState to clean up
// properly (e.g. a crash mid-attempt). Each orphan's virtual_files/
// archives/nzb_documents cascade-delete automatically via their FK to
// selected_releases, so this is the single place that needs to run.
// Excludes any selected_release still backing an active symlink_publication
// (via its virtual_files), even if no queue_item currently points to it —
// deleting that would break a working publish.
func (db *DB) PruneOrphanedSelectedReleases(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	const batchSize = 2000
	var total int64
	for {
		res, err := db.SQL.ExecContext(ctx, `
			delete from selected_releases
			where id in (
				select sr.id
				from selected_releases sr
				where sr.created_at < $1
				  and not exists (
					select 1 from queue_items q where q.selected_release_id = sr.id
				  )
				  and not exists (
					select 1 from virtual_files vf
					join symlink_publications sp on sp.virtual_file_id = vf.id
					where vf.selected_release_id = sr.id
				  )
				limit $2
			)`, cutoff, batchSize)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < batchSize {
			return total, nil
		}
	}
}

func (db *DB) ListMaintenanceCursors(ctx context.Context) ([]MaintenanceCursorEntry, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select task_name, cursor, updated_at
		from maintenance_cursors
		order by task_name asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MaintenanceCursorEntry
	for rows.Next() {
		var item MaintenanceCursorEntry
		if err := rows.Scan(&item.TaskName, &item.Cursor, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

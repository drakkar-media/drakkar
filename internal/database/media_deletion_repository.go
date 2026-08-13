package database

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// MediaCleanupJob preserves the external identity needed to finish cleanup
// after the corresponding Drakkar movie or show has been deleted.
type MediaCleanupJob struct {
	ID                 int64
	MediaType          string
	TMDBID             int64
	Title              string
	ExternalRequestIDs []string
	LibraryPaths       []string
	SubtitlePaths      []string
	Attempts           int
	LastError          string
	NextAttemptAt      time.Time
	CreatedAt          time.Time
	CompletedAt        *time.Time
}

// MediaDeletionRecord reports the rows and filesystem paths captured by a
// transactional movie/show deletion.
type MediaDeletionRecord struct {
	CleanupJob         MediaCleanupJob
	LibraryItemIDs     []int64
	SelectedReleaseIDs []int64
	SymlinkPaths       []string
	SubtitlePaths      []string
	RequestsDeleted    int64
}

// DeleteMediaByLibraryItem deletes a movie or an entire TV show, including all
// episode library items and cascade-owned acquisition data. It also deletes
// the otherwise-unlinked media_requests rows and creates a durable external
// cleanup job in the same transaction.
//
// Errors:
//   - sql.ErrNoRows: libraryItemID does not exist.
//   - returns an error for unsupported media types rather than partially
//     deleting an entity whose ownership boundary is unknown.
func (db *DB) DeleteMediaByLibraryItem(ctx context.Context, libraryItemID int64) (MediaDeletionRecord, error) {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return MediaDeletionRecord{}, err
	}
	defer tx.Rollback()

	var (
		mediaType string
		title     string
		movieID   sql.NullInt64
		showID    sql.NullInt64
		tmdbID    sql.NullInt64
		tvdbID    sql.NullInt64
	)
	err = tx.QueryRowContext(ctx, `
		select li.media_type, coalesce(m.title, ts.title, li.title), li.movie_id, e.tv_show_id,
		       coalesce(m.tmdb_id, ts.tmdb_id), ts.tvdb_id
		from library_items li
		left join movies m on m.id = li.movie_id
		left join episodes e on e.id = li.episode_id
		left join tv_shows ts on ts.id = e.tv_show_id
		where li.id = $1`, libraryItemID,
	).Scan(&mediaType, &title, &movieID, &showID, &tmdbID, &tvdbID)
	if err != nil {
		return MediaDeletionRecord{}, err
	}

	var libraryItemIDs []int64
	switch mediaType {
	case "movie":
		if !movieID.Valid {
			return MediaDeletionRecord{}, fmt.Errorf("movie library item %d has no movie row", libraryItemID)
		}
		if tmdbID.Valid && tmdbID.Int64 > 0 {
			if _, err = tx.ExecContext(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("seerr-movie:tmdb:%d", tmdbID.Int64)); err != nil {
				return MediaDeletionRecord{}, err
			}
		}
		if err = lockMediaLibraryItem(ctx, tx, libraryItemID); err != nil {
			return MediaDeletionRecord{}, err
		}
		libraryItemIDs, err = queryInt64s(ctx, tx, `select id from library_items where movie_id = $1 order by id`, movieID.Int64)
	case "episode", "tv":
		if !showID.Valid {
			return MediaDeletionRecord{}, fmt.Errorf("TV library item %d has no show row", libraryItemID)
		}
		lockKeys := make([]string, 0, 2)
		if tvdbID.Valid && tvdbID.Int64 > 0 {
			lockKeys = append(lockKeys, fmt.Sprintf("seerr-show:tvdb:%d", tvdbID.Int64))
		}
		if tmdbID.Valid && tmdbID.Int64 > 0 {
			lockKeys = append(lockKeys, fmt.Sprintf("seerr-show:tmdb:%d", tmdbID.Int64))
		}
		sort.Strings(lockKeys)
		for _, key := range lockKeys {
			if _, err = tx.ExecContext(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
				return MediaDeletionRecord{}, err
			}
		}
		if err = lockMediaLibraryItem(ctx, tx, libraryItemID); err != nil {
			return MediaDeletionRecord{}, err
		}
		libraryItemIDs, err = queryInt64s(ctx, tx, `
			select li.id
			from library_items li
			join episodes e on e.id = li.episode_id
			where e.tv_show_id = $1
			order by li.id`, showID.Int64)
		mediaType = "tv"
	default:
		return MediaDeletionRecord{}, fmt.Errorf("unsupported media type %q", mediaType)
	}
	if err != nil {
		return MediaDeletionRecord{}, err
	}
	if len(libraryItemIDs) == 0 {
		return MediaDeletionRecord{}, sql.ErrNoRows
	}

	placeholders, args := int64SQLArgs(libraryItemIDs)
	record := MediaDeletionRecord{LibraryItemIDs: libraryItemIDs}
	record.SelectedReleaseIDs, err = queryInt64s(ctx, tx, fmt.Sprintf(`
		select distinct id from selected_releases
		where library_item_id in (%s)
		order by id`, placeholders), args...)
	if err != nil {
		return MediaDeletionRecord{}, err
	}
	record.SymlinkPaths, err = queryStrings(ctx, tx, fmt.Sprintf(`
		select distinct library_path from symlink_publications
		where library_item_id in (%s)
		order by library_path`, placeholders), args...)
	if err != nil {
		return MediaDeletionRecord{}, err
	}
	record.SubtitlePaths, err = queryStrings(ctx, tx, fmt.Sprintf(`
		select distinct path from subtitle_files
		where library_item_id in (%s)
		order by path`, placeholders), args...)
	if err != nil {
		return MediaDeletionRecord{}, err
	}
	externalIDs, err := queryStrings(ctx, tx, fmt.Sprintf(`
		select distinct mr.external_id
		from media_requests mr
		join queue_items q
		  on q.idempotency_key in (
		      'seerr-movie-' || coalesce(mr.external_id, ''),
		      'seerr-tv-' || coalesce(mr.external_id, '')
		  )
		where q.library_item_id in (%s)
		  and coalesce(mr.external_id, '') <> ''
		order by mr.external_id`, placeholders), args...)
	if err != nil {
		return MediaDeletionRecord{}, err
	}

	if len(externalIDs) > 0 {
		var deleted sql.Result
		if mediaType == "tv" {
			baseIDs := uniqueRequestBaseIDs(externalIDs)
			deleted, err = tx.ExecContext(ctx, `
				delete from media_requests
				where request_type = 'tv'
				  and split_part(external_id, '-', 1) = any($1::text[])`, pgTextArray(baseIDs))
		} else {
			deleted, err = tx.ExecContext(ctx, `
				delete from media_requests
				where request_type = 'movie'
				  and external_id = any($1::text[])`, pgTextArray(externalIDs))
		}
		if err != nil {
			return MediaDeletionRecord{}, err
		}
		record.RequestsDeleted, _ = deleted.RowsAffected()
	}

	job := MediaCleanupJob{
		MediaType:          mediaType,
		TMDBID:             tmdbID.Int64,
		Title:              title,
		ExternalRequestIDs: externalIDs,
		LibraryPaths:       record.SymlinkPaths,
		SubtitlePaths:      record.SubtitlePaths,
	}
	err = tx.QueryRowContext(ctx, `
		insert into media_cleanup_jobs
		    (media_type, tmdb_id, title, external_request_ids, library_paths, subtitle_paths)
		values ($1, $2, $3, $4::text[], $5::text[], $6::text[])
		returning id, attempts, last_error, next_attempt_at, created_at`,
		job.MediaType, job.TMDBID, job.Title, pgTextArray(job.ExternalRequestIDs),
		pgTextArray(job.LibraryPaths), pgTextArray(job.SubtitlePaths),
	).Scan(&job.ID, &job.Attempts, &job.LastError, &job.NextAttemptAt, &job.CreatedAt)
	if err != nil {
		return MediaDeletionRecord{}, err
	}
	record.CleanupJob = job

	if mediaType == "movie" {
		_, err = tx.ExecContext(ctx, `delete from movies where id = $1`, movieID.Int64)
	} else {
		_, err = tx.ExecContext(ctx, `delete from tv_shows where id = $1`, showID.Int64)
	}
	if err != nil {
		return MediaDeletionRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return MediaDeletionRecord{}, err
	}
	db.invalidateCatalogAmbiguityIndex()
	return record, nil
}

func lockMediaLibraryItem(ctx context.Context, tx *sql.Tx, libraryItemID int64) error {
	var id int64
	return tx.QueryRowContext(ctx, `select id from library_items where id = $1 for update`, libraryItemID).Scan(&id)
}

// ListPendingMediaCleanupJobs returns due external cleanup jobs in FIFO order.
func (db *DB) ListPendingMediaCleanupJobs(ctx context.Context, limit int) ([]MediaCleanupJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := db.SQL.QueryContext(ctx, `
		select id, media_type, tmdb_id, title, external_request_ids,
		       library_paths, subtitle_paths, attempts, last_error, next_attempt_at, created_at,
		       completed_at
		from media_cleanup_jobs
		where completed_at is null and next_attempt_at <= now()
		order by next_attempt_at, id
		limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MediaCleanupJob
	for rows.Next() {
		var (
			job       MediaCleanupJob
			completed sql.NullTime
		)
		if err := rows.Scan(
			&job.ID, &job.MediaType, &job.TMDBID, &job.Title,
			pgTextArrayScan(&job.ExternalRequestIDs), pgTextArrayScan(&job.LibraryPaths), pgTextArrayScan(&job.SubtitlePaths),
			&job.Attempts, &job.LastError, &job.NextAttemptAt, &job.CreatedAt, &completed,
		); err != nil {
			return nil, err
		}
		if completed.Valid {
			value := completed.Time
			job.CompletedAt = &value
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// RecordMediaCleanupAttempt marks a cleanup job complete on success or
// schedules an exponentially backed-off retry after a failure.
func (db *DB) RecordMediaCleanupAttempt(ctx context.Context, jobID int64, cleanupErr error) error {
	if cleanupErr == nil {
		_, err := db.SQL.ExecContext(ctx, `
			update media_cleanup_jobs
			set attempts = attempts + 1, last_error = '', completed_at = now()
			where id = $1`, jobID)
		return err
	}

	var attempts int
	if err := db.SQL.QueryRowContext(ctx, `select attempts from media_cleanup_jobs where id = $1`, jobID).Scan(&attempts); err != nil {
		return err
	}
	delay := time.Minute << min(attempts, 10)
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	_, err := db.SQL.ExecContext(ctx, `
		update media_cleanup_jobs
		set attempts = attempts + 1, last_error = $2, next_attempt_at = $3
		where id = $1 and completed_at is null`, jobID, cleanupErr.Error(), time.Now().UTC().Add(delay))
	return err
}

func int64SQLArgs(ids []int64) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}

func queryInt64s(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var value int64
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func queryStrings(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func uniqueRequestBaseIDs(externalIDs []string) []string {
	seen := make(map[string]struct{}, len(externalIDs))
	for _, externalID := range externalIDs {
		base := strings.TrimSpace(strings.SplitN(externalID, "-", 2)[0])
		if base != "" {
			seen[base] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for base := range seen {
		out = append(out, base)
	}
	sort.Strings(out)
	return out
}

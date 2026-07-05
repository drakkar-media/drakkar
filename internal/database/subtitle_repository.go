package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func (db *DB) ListSubtitleFiles(ctx context.Context, libraryItemID int64) ([]SubtitleFileSummary, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select id, library_item_id, provider, language, path, created_at
		from subtitle_files
		where library_item_id = $1
		order by language asc, provider asc, path asc, id asc`, libraryItemID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SubtitleFileSummary
	for rows.Next() {
		var item SubtitleFileSummary
		if err := rows.Scan(&item.ID, &item.LibraryItemID, &item.Provider, &item.Language, &item.Path, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) ListSubtitleCandidates(ctx context.Context, libraryItemID int64) ([]SubtitleCandidateSummary, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select id, library_item_id, provider, language, title, release_name, format, hearing_impaired, score, external_id, download_url, created_at
		from subtitle_candidates
		where library_item_id = $1
		order by score desc, language asc, provider asc, created_at asc, id asc`, libraryItemID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SubtitleCandidateSummary
	for rows.Next() {
		var item SubtitleCandidateSummary
		if err := rows.Scan(
			&item.ID,
			&item.LibraryItemID,
			&item.Provider,
			&item.Language,
			&item.Title,
			&item.ReleaseName,
			&item.Format,
			&item.HearingImpaired,
			&item.Score,
			&item.ExternalID,
			&item.DownloadURL,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) GetSubtitleCandidate(ctx context.Context, candidateID int64) (SubtitleCandidateSummary, error) {
	var item SubtitleCandidateSummary
	err := db.SQL.QueryRowContext(ctx, `
		select id, library_item_id, provider, language, title, release_name, format, hearing_impaired, score, external_id, download_url, created_at
		from subtitle_candidates
		where id = $1`, candidateID,
	).Scan(
		&item.ID,
		&item.LibraryItemID,
		&item.Provider,
		&item.Language,
		&item.Title,
		&item.ReleaseName,
		&item.Format,
		&item.HearingImpaired,
		&item.Score,
		&item.ExternalID,
		&item.DownloadURL,
		&item.CreatedAt,
	)
	return item, err
}

func (db *DB) ListPublicationPathsForLibraryItem(ctx context.Context, libraryItemID int64) ([]string, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select library_path
		from symlink_publications
		where library_item_id = $1
		order by library_path asc`, libraryItemID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		out = append(out, path)
	}
	return out, rows.Err()
}

func (db *DB) GetSubtitleSearchInput(ctx context.Context, libraryItemID int64) (SubtitleSearchInput, error) {
	var item SubtitleSearchInput
	err := db.SQL.QueryRowContext(ctx, `
		select
			li.id,
			li.media_type,
			coalesce(li.title, ''),
			coalesce(tv.title, ''),
			coalesce(m.release_year, 0),
			coalesce(tv.release_year, 0),
			coalesce(e.season_number, 0),
			coalesce(e.episode_number, 0),
			coalesce(m.tmdb_id, 0),
			coalesce(tv.tmdb_id, 0)
		from library_items li
		left join movies m on m.id = li.movie_id
		left join episodes e on e.id = li.episode_id
		left join tv_shows tv on tv.id = e.tv_show_id
		where li.id = $1`, libraryItemID,
	).Scan(
		&item.LibraryItemID,
		&item.MediaType,
		&item.Title,
		&item.ShowTitle,
		&item.MovieYear,
		&item.ShowYear,
		&item.SeasonNumber,
		&item.EpisodeNumber,
		&item.TMDBID,
		&item.TVDBID,
	)
	return item, err
}

func (db *DB) ReplaceSubtitleCandidates(ctx context.Context, libraryItemID int64, provider string, candidates []SubtitleCandidateRecord) error {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, `
		delete from subtitle_candidates
		where library_item_id = $1
		  and provider = $2`, libraryItemID, provider,
	); err != nil {
		return err
	}

	for _, candidate := range candidates {
		if _, err = tx.ExecContext(ctx, `
			insert into subtitle_candidates (
				library_item_id, provider, language, score, external_id, title, release_name, format, hearing_impaired, download_url
			) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			libraryItemID,
			candidate.Provider,
			candidate.Language,
			candidate.Score,
			candidate.ExternalID,
			candidate.Title,
			candidate.ReleaseName,
			candidate.Format,
			candidate.HearingImpaired,
			candidate.DownloadURL,
		); err != nil {
			return err
		}
	}

	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (db *DB) ReplaceSubtitleFiles(ctx context.Context, libraryItemID int64, provider, language string, paths []string) error {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, `
		delete from subtitle_files
		where library_item_id = $1
		  and provider = $2
		  and language = $3`, libraryItemID, provider, language,
	); err != nil {
		return err
	}

	for _, path := range paths {
		if _, err = tx.ExecContext(ctx, `
			insert into subtitle_files (library_item_id, provider, language, path)
			values ($1, $2, $3, $4)`, libraryItemID, provider, language, path,
		); err != nil {
			return err
		}
	}

	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// ListSubtitleLibrary returns a paged, filterable view of subtitle state
// across every available library item (movies and individual TV episodes),
// for the library-wide subtitle manager page.
func (db *DB) ListSubtitleLibrary(ctx context.Context, filter SubtitleLibraryFilter) (SubtitleLibraryPage, error) {
	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	search := strings.TrimSpace(filter.Search)
	mediaType := strings.TrimSpace(filter.MediaType)

	var total int
	if err := db.SQL.QueryRowContext(ctx, `
		select count(*)
		from library_items li
		left join episodes e on e.id = li.episode_id
		left join tv_shows tv on tv.id = e.tv_show_id
		left join lateral (
			select array_agg(distinct language) as languages
			from subtitle_files sf
			where sf.library_item_id = li.id
		) sf on true
		where li.available
		  and ($1 = '' or li.media_type = $1)
		  and ($2 = '' or li.title ilike '%' || $2 || '%' or coalesce(tv.title, '') ilike '%' || $2 || '%')
		  and (not $3 or coalesce(array_length(sf.languages, 1), 0) = 0)`,
		mediaType, search, filter.MissingOnly,
	).Scan(&total); err != nil {
		return SubtitleLibraryPage{}, err
	}

	rows, err := db.SQL.QueryContext(ctx, `
		select
			li.id,
			li.media_type,
			li.title,
			coalesce(tv.title, ''),
			coalesce(e.season_number, 0),
			coalesce(e.episode_number, 0),
			li.available,
			coalesce(sf.languages, array[]::text[]),
			coalesce(sc.candidate_count, 0),
			li.requested_at
		from library_items li
		left join episodes e on e.id = li.episode_id
		left join tv_shows tv on tv.id = e.tv_show_id
		left join lateral (
			select array_agg(distinct language order by language) as languages
			from subtitle_files sf
			where sf.library_item_id = li.id
		) sf on true
		left join lateral (
			select count(*) as candidate_count
			from subtitle_candidates sc
			where sc.library_item_id = li.id
		) sc on true
		where li.available
		  and ($1 = '' or li.media_type = $1)
		  and ($2 = '' or li.title ilike '%' || $2 || '%' or coalesce(tv.title, '') ilike '%' || $2 || '%')
		  and (not $3 or coalesce(array_length(sf.languages, 1), 0) = 0)
		order by li.media_type, coalesce(tv.title, li.title), e.season_number, e.episode_number, li.id
		limit $4 offset $5`,
		mediaType, search, filter.MissingOnly, pageSize, (page-1)*pageSize,
	)
	if err != nil {
		return SubtitleLibraryPage{}, err
	}
	defer rows.Close()

	var items []SubtitleLibraryRow
	for rows.Next() {
		var item SubtitleLibraryRow
		if err := rows.Scan(
			&item.LibraryItemID,
			&item.MediaType,
			&item.Title,
			&item.ShowTitle,
			&item.SeasonNumber,
			&item.EpisodeNumber,
			&item.Available,
			pgTextArrayScan(&item.Languages),
			&item.CandidateCount,
			&item.RequestedAt,
		); err != nil {
			return SubtitleLibraryPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return SubtitleLibraryPage{}, err
	}

	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	return SubtitleLibraryPage{
		Items:      items,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages,
	}, nil
}

func (db *DB) DeleteSubtitleFile(ctx context.Context, subtitleID int64) (SubtitleDeleteGroup, error) {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return SubtitleDeleteGroup{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var group SubtitleDeleteGroup
	if err = tx.QueryRowContext(ctx, `
		select library_item_id, provider, language
		from subtitle_files
		where id = $1`, subtitleID,
	).Scan(&group.LibraryItemID, &group.Provider, &group.Language); err != nil {
		if err == sql.ErrNoRows {
			return SubtitleDeleteGroup{}, fmt.Errorf("subtitle file %d not found", subtitleID)
		}
		return SubtitleDeleteGroup{}, err
	}
	rows, queryErr := tx.QueryContext(ctx, `
		select path
		from subtitle_files
		where library_item_id = $1
		  and provider = $2
		  and language = $3
		order by path asc, id asc`, group.LibraryItemID, group.Provider, group.Language)
	if queryErr != nil {
		err = queryErr
		return SubtitleDeleteGroup{}, err
	}
	for rows.Next() {
		var path string
		if scanErr := rows.Scan(&path); scanErr != nil {
			rows.Close()
			err = scanErr
			return SubtitleDeleteGroup{}, err
		}
		group.Paths = append(group.Paths, path)
	}
	if closeErr := rows.Close(); closeErr != nil {
		err = closeErr
		return SubtitleDeleteGroup{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		delete from subtitle_files
		where library_item_id = $1
		  and provider = $2
		  and language = $3`, group.LibraryItemID, group.Provider, group.Language,
	); err != nil {
		return SubtitleDeleteGroup{}, err
	}
	if err = tx.Commit(); err != nil {
		return SubtitleDeleteGroup{}, err
	}
	return group, nil
}

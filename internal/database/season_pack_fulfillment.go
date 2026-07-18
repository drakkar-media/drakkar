package database

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
)

// episodePattern extracts SxxExx, sxex, or x×xx from a filename.
var episodePattern = regexp.MustCompile(`(?i)[^a-z]s(\d{1,2})e(\d{1,3})[^0-9]`)

// ParseEpisodeFromFilename returns (season, episode) or (0, 0) if not found.
func ParseEpisodeFromFilename(name string) (season, episode int) {
	// Pad both sides to ensure the non-alpha boundary matches.
	m := episodePattern.FindStringSubmatch(" " + strings.ToLower(name) + " ")
	if m == nil {
		return 0, 0
	}
	s, _ := strconv.Atoi(m[1])
	e, _ := strconv.Atoi(m[2])
	return s, e
}

// SeasonPackEpisodeMatch pairs a virtual file path with a library item.
type SeasonPackEpisodeMatch struct {
	VirtualFileID   int64
	VirtualFilePath string
	FileName        string
	LibraryItemID   int64
	SeasonNumber    int
	EpisodeNumber   int
}

// FindSeasonPackMatches looks up library items matching the episode numbers
// encoded in the virtual file filenames for a given selected release.
// It returns one match per (season, episode) pair, preferring library items
// for the same TV show as the triggering library item.
func (db *DB) FindSeasonPackMatches(ctx context.Context, selectedReleaseID, triggeringLibraryItemID int64) ([]SeasonPackEpisodeMatch, error) {
	tvShowID, _, err := db.resolveSeasonPackShow(ctx, triggeringLibraryItemID)
	if err != nil || tvShowID == 0 {
		return nil, nil
	}

	// Get all virtual files for this release that we haven't matched yet.
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT vf.id, vf.path, vf.file_name
		FROM virtual_files vf
		WHERE vf.selected_release_id = $1`, selectedReleaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type vfRow struct {
		id, path, name string
		intID          int64
	}
	var vfs []vfRow
	for rows.Next() {
		var r vfRow
		if err := rows.Scan(&r.intID, &r.path, &r.name); err != nil {
			return nil, err
		}
		vfs = append(vfs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Batch-load every library item for this show keyed by (season, episode)
	// instead of issuing one query per virtual file — a season pack's file
	// count and a show's episode count are both small, so this is one query
	// either way, but it no longer scales with the number of files.
	itemRows, err := db.SQL.QueryContext(ctx, `
		SELECT li.id, e.season_number, e.episode_number
		FROM library_items li
		JOIN episodes e ON e.id = li.episode_id
		WHERE e.tv_show_id = $1`, tvShowID)
	if err != nil {
		return nil, err
	}
	defer itemRows.Close()
	libraryItemByEpisode := map[[2]int]int64{}
	for itemRows.Next() {
		var id int64
		var season, episode int
		if err := itemRows.Scan(&id, &season, &episode); err != nil {
			return nil, err
		}
		libraryItemByEpisode[[2]int{season, episode}] = id
	}
	if err := itemRows.Err(); err != nil {
		return nil, err
	}

	var matches []SeasonPackEpisodeMatch
	seen := map[[2]int]bool{}

	for _, vf := range vfs {
		season, episode := ParseEpisodeFromFilename(vf.name)
		if season <= 0 || episode <= 0 {
			continue
		}
		key := [2]int{season, episode}
		if seen[key] {
			continue
		}
		libraryItemID, ok := libraryItemByEpisode[key]
		if !ok {
			continue // no matching un-fulfilled library item
		}
		seen[key] = true
		matches = append(matches, SeasonPackEpisodeMatch{
			VirtualFileID:   vf.intID,
			VirtualFilePath: vf.path,
			FileName:        vf.name,
			LibraryItemID:   libraryItemID,
			SeasonNumber:    season,
			EpisodeNumber:   episode,
		})
	}
	return matches, nil
}

// CreateSeasonPackEpisodeItems is called when a season pack is published for a
// whole-show library item (season=0, episode=0). It parses each virtual file
// in the release, creates episode + library_item records for any SxxExx it
// finds, and marks them available. This turns one whole-show library item into
// many per-episode items so the library reflects actual episode availability.
func (db *DB) CreateSeasonPackEpisodeItems(ctx context.Context, selectedReleaseID, triggeringLibraryItemID int64) error {
	tvShowID, showTitle, err := db.resolveSeasonPackShow(ctx, triggeringLibraryItemID)
	if err != nil || tvShowID == 0 {
		return nil
	}

	// Collect virtual files for this release.
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, file_name FROM virtual_files WHERE selected_release_id = $1`, selectedReleaseID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type vf struct {
		id   int64
		name string
	}
	var files []vf
	for rows.Next() {
		var f vf
		if err := rows.Scan(&f.id, &f.name); err != nil {
			return err
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Also get the release_candidate_id to link the new selected_releases.
	var releaseCandidateID int64
	err = db.SQL.QueryRowContext(ctx, `
		SELECT release_candidate_id FROM selected_releases WHERE id = $1`, selectedReleaseID).Scan(&releaseCandidateID)
	if err != nil {
		return err
	}

	seen := map[[2]int]bool{}
	for _, f := range files {
		season, episode := ParseEpisodeFromFilename(f.name)
		if season <= 0 || episode <= 0 {
			continue
		}
		key := [2]int{season, episode}
		if seen[key] {
			continue
		}
		seen[key] = true
		// Best-effort per episode, same as before: one bad episode (e.g. a
		// transient connection error) shouldn't abort the rest of the pack.
		// createSeasonPackEpisodeItem itself now runs atomically, though, so
		// a failure can no longer leave this episode half-linked (episode
		// row created but no library_item, or library_item but no
		// queue_item) the way the previous unguarded statement-by-statement
		// version could.
		if err := db.createSeasonPackEpisodeItem(ctx, tvShowID, showTitle, releaseCandidateID, season, episode); err != nil {
			slog.Warn("season pack: failed to create episode item", "tv_show_id", tvShowID, "season", season, "episode", episode, "err", err)
		}
	}
	return nil
}

// createSeasonPackEpisodeItem upserts one episode's episode/library_item/
// selected_release/queue_item rows in a single transaction.
func (db *DB) createSeasonPackEpisodeItem(ctx context.Context, tvShowID int64, showTitle string, releaseCandidateID int64, season, episode int) error {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Upsert the episode record.
	var episodeID int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO episodes (tv_show_id, season_number, episode_number, title)
		VALUES ($1, $2, $3, '')
		ON CONFLICT (tv_show_id, season_number, episode_number) DO UPDATE
		  SET tv_show_id = excluded.tv_show_id
		RETURNING id`, tvShowID, season, episode).Scan(&episodeID); err != nil {
		return err
	}

	// Upsert the library_item for this episode (unique on episode_id).
	var libItemID int64
	profileID := db.resolveDefaultQualityProfileID(ctx, "episode")
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO library_items (media_type, episode_id, title, available, quality_profile_id)
		VALUES ('episode', $1, $2, true, $3)
		ON CONFLICT (episode_id) WHERE episode_id IS NOT NULL DO UPDATE
		  SET available = true
		RETURNING id`, episodeID, showTitle, profileID).Scan(&libItemID); err != nil {
		return err
	}

	// Link a selected_release so the episode is associated with the NZB
	// release. This must be idempotent -- re-running this for an episode
	// that's already linked (e.g. the pack being re-selected/republished)
	// must not create a second row. selected_releases has no unique
	// constraint on library_item_id for ON CONFLICT to match against, so (as
	// with FulfillEpisodeLibraryItem below, which hit this same bug and had
	// 94.5% of the table end up as dead duplicate rows) an explicit
	// existence check is required instead of relying on ON CONFLICT, which
	// never fires and always inserts a fresh row.
	var srID int64
	var alreadyLinked bool
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM selected_releases WHERE library_item_id = $1`, libItemID).Scan(&srID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		err = nil
	} else {
		alreadyLinked = true
	}
	if !alreadyLinked {
		if err = tx.QueryRowContext(ctx, `
			INSERT INTO selected_releases (release_candidate_id, library_item_id)
			VALUES ($1, $2)
			RETURNING id`, releaseCandidateID, libItemID).Scan(&srID); err != nil {
			return err
		}
	}

	// Create a queue_item so the monitoring system knows this episode is done.
	ikey := strings.ToLower(showTitle) + "-pack-" + strconv.Itoa(season) + "-" + strconv.Itoa(episode)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO queue_items
		    (library_item_id, selected_release_id, state, idempotency_key, updated_at)
		VALUES ($1, $2, 'available', $3, now())
		ON CONFLICT (library_item_id) DO UPDATE
		  SET state = 'available', selected_release_id = $2, updated_at = now()`,
		libItemID, srID, ikey); err != nil {
		return err
	}

	return tx.Commit()
}

func (db *DB) resolveSeasonPackShow(ctx context.Context, triggeringLibraryItemID int64) (int64, string, error) {
	var (
		tvShowID  int64
		showTitle string
	)
	err := db.SQL.QueryRowContext(ctx, `
		SELECT
			coalesce(
				e.tv_show_id,
				(
					SELECT tv.id
					FROM tv_shows tv
					WHERE lower(tv.title) = lower(li.title)
					ORDER BY tv.id ASC
					LIMIT 1
				),
				0
			),
			coalesce(nullif(tv.title, ''), nullif(li.title, ''), '')
		FROM library_items li
		LEFT JOIN episodes e ON e.id = li.episode_id
		LEFT JOIN tv_shows tv ON tv.id = e.tv_show_id
		WHERE li.id = $1`, triggeringLibraryItemID).Scan(&tvShowID, &showTitle)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, "", nil
		}
		return 0, "", err
	}
	return tvShowID, showTitle, nil
}

// FulfillEpisodeLibraryItem creates a selected_release + queue_item for an
// episode library item that is being fulfilled by a season pack virtual file,
// then marks it as available.
func (db *DB) FulfillEpisodeLibraryItem(ctx context.Context, libraryItemID, sourceSelectedReleaseID, virtualFileID int64) error {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Unlike createSeasonPackEpisodeItem's sibling flow (which naturally
	// serializes concurrent calls for the same episode via the earlier
	// library_items upsert's ON CONFLICT (episode_id) row lock),
	// libraryItemID here is a bare caller-supplied parameter with no prior
	// statement in this transaction to serialize on -- so two concurrent
	// calls for the same libraryItemID (e.g. two overlapping season-pack
	// publishes finishing around the same time via the download worker
	// pool) could otherwise both pass the alreadyFulfilled check below
	// before either commits. Lock first, matching every selection-mutating
	// function in workflow_repository.go.
	if err = lockLibraryItemQueueRow(ctx, tx, libraryItemID); err != nil {
		return err
	}

	// Re-use the same release candidate as the triggering item so all episodes
	// share the same NZB document and provenance.
	var releaseCandidateID int64
	err = tx.QueryRowContext(ctx, `
		SELECT release_candidate_id FROM selected_releases WHERE id = $1`,
		sourceSelectedReleaseID).Scan(&releaseCandidateID)
	if err != nil {
		return err
	}

	// This is meant to be idempotent -- fulfilling an episode that's already
	// fulfilled should just mark it available and stop, not create another
	// selected_release row. That used to rely on "ON CONFLICT DO NOTHING",
	// but selected_releases has no unique constraint on library_item_id for
	// it to conflict against, so the insert always succeeded and created a
	// fresh row on every call (every RebuildPublications pass on every
	// process start, every repeat season-pack search): 94.5% of the table
	// ended up being dead duplicate rows this way, and thousands of episodes'
	// live queue_items.selected_release_id pointer ended up referencing
	// whichever duplicate happened to be inserted last -- sometimes one with
	// no virtual_files at all, sometimes a genuinely redundant re-download.
	// Checking for an existing row explicitly (rather than relying on a
	// constraint that doesn't exist) restores the originally-intended
	// no-op-if-already-fulfilled behavior without a schema change.
	var alreadyFulfilled bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM selected_releases WHERE library_item_id = $1)`,
		libraryItemID).Scan(&alreadyFulfilled); err != nil {
		return err
	}
	if alreadyFulfilled {
		if err := markLibraryItemAvailable(ctx, tx, libraryItemID); err != nil {
			return err
		}
		return tx.Commit()
	}

	// Insert a new selected_release row for this episode.
	var newSelectedReleaseID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO selected_releases (release_candidate_id, library_item_id)
		VALUES ($1, $2)
		RETURNING id`, releaseCandidateID, libraryItemID).Scan(&newSelectedReleaseID)
	if err != nil {
		return err
	}

	// Update the virtual file to also reference this selected release.
	// We do NOT duplicate the virtual file — we just update the queue item.
	_, err = tx.ExecContext(ctx, `
		UPDATE queue_items SET
			state = 'available',
			selected_release_id = $2,
			updated_at = now()
		WHERE library_item_id = $1 AND state != 'available'`,
		libraryItemID, newSelectedReleaseID)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE library_items SET available = true WHERE id = $1`, libraryItemID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// sqlExecer is satisfied by both *sql.DB and *sql.Tx, so callers can reuse
// the same statement helper whether or not they're inside a transaction.
type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// markLibraryItemAvailable marks a library item and its queue item
// available. Pass tx when called from within an existing transaction so
// both updates commit atomically with the rest of it; pass db.SQL otherwise.
func markLibraryItemAvailable(ctx context.Context, exec sqlExecer, libraryItemID int64) error {
	if _, err := exec.ExecContext(ctx, `
		UPDATE library_items SET available = true WHERE id = $1`, libraryItemID); err != nil {
		return err
	}
	_, err := exec.ExecContext(ctx, `
		UPDATE queue_items SET state = 'available', updated_at = now()
		WHERE library_item_id = $1 AND state != 'available'`, libraryItemID)
	return err
}

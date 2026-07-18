package database

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// openPublicationTestDB mirrors openBlocklistTestDB: a real Postgres
// connection for publication_repository.go's query functions, skipping when
// DRAKKAR_TEST_DATABASE_URL isn't set.
func openPublicationTestDB(t *testing.T) (*DB, *sql.DB, context.Context) {
	t.Helper()
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return &DB{SQL: sqlDB}, sqlDB, context.Background()
}

// pubTestFixture creates a library item + release_candidate + selected_release,
// optionally with a virtual file, and returns their ids. Cleanup of the
// library item cascades everything else.
func pubTestFixture(t *testing.T, ctx context.Context, sqlDB *sql.DB, title, state string, withVirtualFile bool) (libID, selectedReleaseID int64) {
	t.Helper()
	libID = setupRaceTestLibraryItem(t, ctx, sqlDB, title, state)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, $2, $3, 'test-indexer')
		returning id`, libID, title, "http://example/"+title,
	).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libID, rcID,
	).Scan(&selectedReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		update queue_items set selected_release_id = $2 where library_item_id = $1`, libID, selectedReleaseID,
	); err != nil {
		t.Fatal(err)
	}
	if withVirtualFile {
		if _, err := sqlDB.ExecContext(ctx, `
			insert into virtual_files (selected_release_id, path, file_name, reader_kind)
			values ($1, $2, $3, 'archive')`, selectedReleaseID, "/"+title+"/file.mkv", title+".mkv",
		); err != nil {
			t.Fatal(err)
		}
	}
	return libID, selectedReleaseID
}

func TestListSelectedReleasesForPublication(t *testing.T) {
	db, sqlDB, ctx := openPublicationTestDB(t)

	// In an active pipeline state (preflight) -- must be included.
	libPreflight, srPreflight := pubTestFixture(t, ctx, sqlDB, "pub-list-preflight", "preflight", true)
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libPreflight)

	// Available, but already has a symlink_publications row -- must be
	// excluded (already fully published).
	libPublished, srPublished := pubTestFixture(t, ctx, sqlDB, "pub-list-published", "available", true)
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libPublished)
	var vfPublishedID int64
	if err := sqlDB.QueryRowContext(ctx, `select id from virtual_files where selected_release_id = $1`, srPublished).Scan(&vfPublishedID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into symlink_publications (library_item_id, virtual_file_id, library_path, target_path)
		values ($1, $2, $3, $4)`, libPublished, vfPublishedID, "/library/pub-list-published.mkv", "/virtual/pub-list-published.mkv",
	); err != nil {
		t.Fatal(err)
	}

	// Available, but missing its symlink_publications row -- must be included.
	libAvailableUnpublished, srAvailableUnpublished := pubTestFixture(t, ctx, sqlDB, "pub-list-available-unpublished", "available", true)
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libAvailableUnpublished)

	// Requested state (not in either the active or available/degraded set) --
	// must be excluded even though it has a virtual file.
	libRequested, _ := pubTestFixture(t, ctx, sqlDB, "pub-list-requested", "requested", true)
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libRequested)

	got, err := db.ListSelectedReleasesForPublication(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set := map[int64]bool{}
	for _, id := range got {
		set[id] = true
	}
	if !set[srPreflight] {
		t.Errorf("expected preflight selected_release %d to be included", srPreflight)
	}
	if set[srPublished] {
		t.Errorf("expected already-published selected_release %d to be excluded", srPublished)
	}
	if !set[srAvailableUnpublished] {
		t.Errorf("expected available-but-unpublished selected_release %d to be included", srAvailableUnpublished)
	}
}

func TestListSelectedReleasesByLibraryItem(t *testing.T) {
	db, sqlDB, ctx := openPublicationTestDB(t)

	libID, srCurrent := pubTestFixture(t, ctx, sqlDB, "pub-by-item-current", "available", true)
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	// A second, stale selected_release for the same library item, also with
	// a virtual file, but not the one the current queue_item points to.
	var rcStale int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'pub-by-item-stale', 'http://example/pub-by-item-stale', 'test-indexer')
		returning id`, libID).Scan(&rcStale); err != nil {
		t.Fatal(err)
	}
	var srStale int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libID, rcStale).Scan(&srStale); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, reader_kind)
		values ($1, '/pub-by-item-stale/file.mkv', 'stale.mkv', 'archive')`, srStale,
	); err != nil {
		t.Fatal(err)
	}

	got, err := db.ListSelectedReleasesByLibraryItem(ctx, libID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 selected_releases (current + stale), got %d: %v", len(got), got)
	}
	// The release the current queue item actually points to must sort last.
	if got[len(got)-1] != srCurrent {
		t.Errorf("expected the current selected_release %d to sort last, got order %v", srCurrent, got)
	}
}

func TestListPendingRepublishTargets(t *testing.T) {
	db, sqlDB, ctx := openPublicationTestDB(t)

	// (a) Not yet available, stuck in an active queue state.
	libActive, _ := pubTestFixture(t, ctx, sqlDB, "pub-republish-active", "preflight", false)
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libActive)

	// (b) Marked available but with no symlink_publications at all.
	libNoSymlink, _ := pubTestFixture(t, ctx, sqlDB, "pub-republish-no-symlink", "available", true)
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libNoSymlink)
	if _, err := sqlDB.ExecContext(ctx, `update library_items set available = true where id = $1`, libNoSymlink); err != nil {
		t.Fatal(err)
	}

	// (c) Marked available, current release has virtual files, but the
	// existing symlink_publications row points at a stale, different release.
	libStaleSymlink, _ := pubTestFixture(t, ctx, sqlDB, "pub-republish-stale-symlink", "available", true)
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libStaleSymlink)
	if _, err := sqlDB.ExecContext(ctx, `update library_items set available = true where id = $1`, libStaleSymlink); err != nil {
		t.Fatal(err)
	}
	var rcOld int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'pub-republish-stale-old', 'http://example/pub-republish-stale-old', 'test-indexer')
		returning id`, libStaleSymlink).Scan(&rcOld); err != nil {
		t.Fatal(err)
	}
	var srOld int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libStaleSymlink, rcOld).Scan(&srOld); err != nil {
		t.Fatal(err)
	}
	var vfOld int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, reader_kind)
		values ($1, '/pub-republish-stale-old/file.mkv', 'old.mkv', 'archive')
		returning id`, srOld).Scan(&vfOld); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into symlink_publications (library_item_id, virtual_file_id, library_path, target_path)
		values ($1, $2, '/library/pub-republish-stale-symlink.mkv', '/virtual/old.mkv')`, libStaleSymlink, vfOld,
	); err != nil {
		t.Fatal(err)
	}
	// (d) Fully published and up to date -- must be excluded.
	libDone, srDone := pubTestFixture(t, ctx, sqlDB, "pub-republish-done", "available", true)
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libDone)
	if _, err := sqlDB.ExecContext(ctx, `update library_items set available = true where id = $1`, libDone); err != nil {
		t.Fatal(err)
	}
	var vfDone int64
	if err := sqlDB.QueryRowContext(ctx, `select id from virtual_files where selected_release_id = $1`, srDone).Scan(&vfDone); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into symlink_publications (library_item_id, virtual_file_id, library_path, target_path)
		values ($1, $2, '/library/pub-republish-done.mkv', '/virtual/done.mkv')`, libDone, vfDone,
	); err != nil {
		t.Fatal(err)
	}

	got, err := db.ListPendingRepublishTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set := map[int64]bool{}
	for _, item := range got {
		set[item.LibraryItemID] = true
	}
	if !set[libActive] {
		t.Errorf("expected active-state item %d to be a pending republish target", libActive)
	}
	if !set[libNoSymlink] {
		t.Errorf("expected available-without-symlink item %d to be a pending republish target", libNoSymlink)
	}
	if !set[libStaleSymlink] {
		t.Errorf("expected stale-symlink item %d to be a pending republish target", libStaleSymlink)
	}
	if set[libDone] {
		t.Errorf("expected fully-published item %d to NOT be a pending republish target", libDone)
	}
}

func TestMarkReleaseAvailableClearsFailureReasonAndMarksAvailable(t *testing.T) {
	db, sqlDB, ctx := openPublicationTestDB(t)

	libID, srID := pubTestFixture(t, ctx, sqlDB, "pub-mark-available", "failed", true)
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	if _, err := sqlDB.ExecContext(ctx, `
		update queue_items set failure_reason = 'status 430' where library_item_id = $1`, libID,
	); err != nil {
		t.Fatal(err)
	}

	if err := db.MarkReleaseAvailable(ctx, srID); err != nil {
		t.Fatal(err)
	}

	var state, failureReason string
	if err := sqlDB.QueryRowContext(ctx, `select state, failure_reason from queue_items where library_item_id = $1`, libID).Scan(&state, &failureReason); err != nil {
		t.Fatal(err)
	}
	if state != string(QueueAvailable) {
		t.Errorf("state = %q, want %q", state, QueueAvailable)
	}
	if failureReason != "" {
		t.Errorf("failure_reason = %q, want cleared", failureReason)
	}

	var available bool
	if err := sqlDB.QueryRowContext(ctx, `select available from library_items where id = $1`, libID).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if !available {
		t.Error("expected library item to be marked available")
	}
}

func TestSymlinkPublicationCRUD(t *testing.T) {
	db, sqlDB, ctx := openPublicationTestDB(t)

	libID, srID := pubTestFixture(t, ctx, sqlDB, "pub-symlink-crud", "available", true)
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var vfID int64
	if err := sqlDB.QueryRowContext(ctx, `select id from virtual_files where selected_release_id = $1`, srID).Scan(&vfID); err != nil {
		t.Fatal(err)
	}

	const libraryPath = "/library/pub-symlink-crud.mkv"
	if err := db.UpsertSymlinkPublication(ctx, libID, vfID, libraryPath, "/virtual/original.mkv"); err != nil {
		t.Fatal(err)
	}

	paths, err := db.GetSymlinkPathsForLibraryItem(ctx, libID)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != libraryPath {
		t.Fatalf("GetSymlinkPathsForLibraryItem = %v, want [%s]", paths, libraryPath)
	}

	// Re-upserting the same (library_item_id, library_path) must update the
	// target_path in place, not create a second row.
	if err := db.UpsertSymlinkPublication(ctx, libID, vfID, libraryPath, "/virtual/updated.mkv"); err != nil {
		t.Fatal(err)
	}
	all, err := db.ListSymlinkPublications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, p := range all {
		if p.LibraryPath == libraryPath {
			found++
			if p.TargetPath != "/virtual/updated.mkv" {
				t.Errorf("TargetPath = %q, want /virtual/updated.mkv (upsert should update in place)", p.TargetPath)
			}
		}
	}
	if found != 1 {
		t.Fatalf("expected exactly 1 symlink_publications row for %q after upserting twice, found %d", libraryPath, found)
	}

	completed, err := db.ListCompletedSymlinkEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundCompleted := false
	for _, c := range completed {
		if c.TargetPath == "/virtual/updated.mkv" {
			foundCompleted = true
			if c.Name != "pub-symlink-crud.mkv" {
				t.Errorf("Name = %q, want pub-symlink-crud.mkv (basename of library_path)", c.Name)
			}
		}
	}
	if !foundCompleted {
		t.Error("expected the symlink publication to appear in ListCompletedSymlinkEntries")
	}

	deletedPaths, err := db.DeleteSymlinkPublicationsForLibraryItem(ctx, libID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deletedPaths) != 1 || deletedPaths[0] != libraryPath {
		t.Fatalf("DeleteSymlinkPublicationsForLibraryItem returned %v, want [%s]", deletedPaths, libraryPath)
	}

	remaining, err := db.GetSymlinkPathsForLibraryItem(ctx, libID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no symlink_publications remaining after delete, got %v", remaining)
	}
}

func TestGetEpisodeMetadataForLibraryItem(t *testing.T) {
	db, sqlDB, ctx := openPublicationTestDB(t)

	var tvShowID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into tv_shows (title, release_year, tvdb_id) values ('Pub Episode Metadata Show', 2021, 555111)
		returning id`).Scan(&tvShowID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from tv_shows where id = $1`, tvShowID)

	var episodeID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into episodes (tv_show_id, season_number, episode_number, title)
		values ($1, 3, 7, 'ep title') returning id`, tvShowID).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, episode_id, title, available)
		values ('episode', $1, 'Pub Episode Metadata Show', false) returning id`, episodeID).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	meta, err := db.GetEpisodeMetadataForLibraryItem(ctx, libID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ShowTitle != "Pub Episode Metadata Show" {
		t.Errorf("ShowTitle = %q, want Pub Episode Metadata Show", meta.ShowTitle)
	}
	if meta.ShowYear != 2021 {
		t.Errorf("ShowYear = %d, want 2021", meta.ShowYear)
	}
	if meta.ShowTVDBID != 555111 {
		t.Errorf("ShowTVDBID = %d, want 555111", meta.ShowTVDBID)
	}
	if meta.SeasonNumber != 3 {
		t.Errorf("SeasonNumber = %d, want 3", meta.SeasonNumber)
	}
	if meta.EpisodeNumber != 7 {
		t.Errorf("EpisodeNumber = %d, want 7", meta.EpisodeNumber)
	}

	// A library item with no episode_id at all must return zero values, not error.
	libMovieID := setupRaceTestLibraryItem(t, ctx, sqlDB, "pub-episode-metadata-no-episode", "requested")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libMovieID)
	metaEmpty, err := db.GetEpisodeMetadataForLibraryItem(ctx, libMovieID)
	if err != nil {
		t.Fatal(err)
	}
	if metaEmpty.ShowTitle != "" || metaEmpty.SeasonNumber != 0 || metaEmpty.EpisodeNumber != 0 {
		t.Errorf("expected zero-value metadata for a non-episode item, got %+v", metaEmpty)
	}
}

func TestFindSourceSelectedReleaseForItem(t *testing.T) {
	db, sqlDB, ctx := openPublicationTestDB(t)

	packLibID := setupRaceTestLibraryItem(t, ctx, sqlDB, "pub-source-release-pack", "available")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, packLibID)
	episodeLibID := setupRaceTestLibraryItem(t, ctx, sqlDB, "pub-source-release-episode", "available")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, episodeLibID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'pub-source-release-pack', 'http://example/pub-source-release-pack', 'test-indexer')
		returning id`, packLibID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}

	// The season pack's own selected_release, with virtual files.
	var srPack int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, packLibID, rcID).Scan(&srPack); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, reader_kind)
		values ($1, '/pub-source-release-pack/e01.mkv', 'e01.mkv', 'archive')`, srPack,
	); err != nil {
		t.Fatal(err)
	}

	// The episode's own selected_release (same release_candidate, no virtual files of its own).
	if _, err := sqlDB.ExecContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)`, episodeLibID, rcID,
	); err != nil {
		t.Fatal(err)
	}

	got, err := db.FindSourceSelectedReleaseForItem(ctx, episodeLibID)
	if err != nil {
		t.Fatal(err)
	}
	if got != srPack {
		t.Errorf("FindSourceSelectedReleaseForItem = %d, want the pack's selected_release %d", got, srPack)
	}

	// An item with no such pack relationship must return 0, not an error.
	standaloneLibID := setupRaceTestLibraryItem(t, ctx, sqlDB, "pub-source-release-standalone", "requested")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, standaloneLibID)
	gotNone, err := db.FindSourceSelectedReleaseForItem(ctx, standaloneLibID)
	if err != nil {
		t.Fatal(err)
	}
	if gotNone != 0 {
		t.Errorf("expected 0 for an item with no pack source, got %d", gotNone)
	}
}

func TestListVirtualFilesForRelease(t *testing.T) {
	db, sqlDB, ctx := openPublicationTestDB(t)

	var movieID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into movies (title, release_year, tmdb_id) values ('Pub VF Movie', 2020, 424242)
		returning id`).Scan(&movieID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from movies where id = $1`, movieID)

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, movie_id, title, available)
		values ('movie', $1, 'Pub VF Movie', false) returning id`, movieID).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'pub-vf-movie-release', 'http://example/pub-vf-movie', 'test-indexer')
		returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, reader_kind)
		values ($1, '/pub-vf-movie/movie.mkv', 'movie.mkv', 'archive')`, srID,
	); err != nil {
		t.Fatal(err)
	}

	got, err := db.ListVirtualFilesForRelease(ctx, srID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 virtual file, got %d", len(got))
	}
	if got[0].MediaType != "movie" {
		t.Errorf("MediaType = %q, want movie", got[0].MediaType)
	}
	if got[0].MovieTitle != "Pub VF Movie" {
		t.Errorf("MovieTitle = %q, want Pub VF Movie", got[0].MovieTitle)
	}
	if got[0].MovieYear != 2020 {
		t.Errorf("MovieYear = %d, want 2020", got[0].MovieYear)
	}
	if got[0].MovieTMDBID != 424242 {
		t.Errorf("MovieTMDBID = %d, want 424242", got[0].MovieTMDBID)
	}
	if got[0].LibraryItemID != libID {
		t.Errorf("LibraryItemID = %d, want %d", got[0].LibraryItemID, libID)
	}
}

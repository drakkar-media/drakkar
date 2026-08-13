package database

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

func TestDeleteMovieByLibraryItemCascadesOwnedData(t *testing.T) {
	db, sqlDB, ctx := openPublicationTestDB(t)
	suffix := time.Now().UnixNano()
	externalID := fmt.Sprintf("%d", suffix)
	tmdbID := suffix
	libraryItemID, _, err := db.UpsertMovieRequest(ctx, externalID, tmdbID, "Delete Movie", 2024)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from media_requests where external_id = $1`, externalID)

	selectedReleaseID, virtualFileID := addDeletionReleaseFixture(t, ctx, sqlDB, libraryItemID, suffix)
	symlinkPath := fmt.Sprintf("/mnt/drakkar/media/movies/delete-%d.mkv", suffix)
	subtitlePath := fmt.Sprintf("/mnt/drakkar/media/movies/delete-%d.en.srt", suffix)
	if _, err := sqlDB.ExecContext(ctx, `
		insert into symlink_publications (library_item_id, virtual_file_id, library_path, target_path)
		values ($1, $2, $3, $4)`, libraryItemID, virtualFileID, symlinkPath, "/virtual/delete.mkv"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into subtitle_files (library_item_id, provider, language, path)
		values ($1, 'test', 'en', $2)`, libraryItemID, subtitlePath); err != nil {
		t.Fatal(err)
	}

	record, err := db.DeleteMediaByLibraryItem(ctx, libraryItemID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = sqlDB.ExecContext(context.Background(), `delete from media_cleanup_jobs where id = $1`, record.CleanupJob.ID)
	})
	if record.CleanupJob.MediaType != "movie" || record.CleanupJob.TMDBID != tmdbID {
		t.Fatalf("unexpected cleanup identity: %+v", record.CleanupJob)
	}
	if len(record.SelectedReleaseIDs) != 1 || record.SelectedReleaseIDs[0] != selectedReleaseID {
		t.Fatalf("selected releases not captured: %v", record.SelectedReleaseIDs)
	}
	if len(record.SymlinkPaths) != 1 || record.SymlinkPaths[0] != symlinkPath || len(record.SubtitlePaths) != 1 || record.SubtitlePaths[0] != subtitlePath {
		t.Fatalf("filesystem paths not captured: %+v", record)
	}
	if record.RequestsDeleted != 1 {
		t.Fatalf("expected one request deletion, got %d", record.RequestsDeleted)
	}

	assertDatabaseCount(t, ctx, sqlDB, 0, `select count(*) from library_items where id = $1`, libraryItemID)
	assertDatabaseCount(t, ctx, sqlDB, 0, `select count(*) from movies where tmdb_id = $1`, tmdbID)
	assertDatabaseCount(t, ctx, sqlDB, 0, `select count(*) from selected_releases where id = $1`, selectedReleaseID)
	assertDatabaseCount(t, ctx, sqlDB, 0, `select count(*) from media_requests where external_id = $1`, externalID)
	assertDatabaseCount(t, ctx, sqlDB, 1, `select count(*) from media_cleanup_jobs where id = $1 and subtitle_paths = array[$2]::text[]`, record.CleanupJob.ID, subtitlePath)

	recreatedID, created, err := db.UpsertMovieRequest(ctx, externalID, tmdbID, "Delete Movie", 2024)
	if err != nil {
		t.Fatal(err)
	}
	if recreatedID != 0 || created {
		t.Fatalf("pending cleanup allowed stale request recreation: id=%d created=%t", recreatedID, created)
	}
	assertDatabaseCount(t, ctx, sqlDB, 0, `select count(*) from movies where tmdb_id = $1`, tmdbID)
	if _, err := sqlDB.ExecContext(ctx, `update media_cleanup_jobs set completed_at = now() where id = $1`, record.CleanupJob.ID); err != nil {
		t.Fatal(err)
	}
	recreatedID, created, err = db.UpsertMovieRequest(ctx, externalID, tmdbID, "Delete Movie", 2024)
	if err != nil {
		t.Fatal(err)
	}
	if recreatedID != 0 || created {
		t.Fatalf("completed cleanup allowed old request ID recreation: id=%d created=%t", recreatedID, created)
	}
	newExternalID := externalID + "-new"
	newLibraryItemID, created, err := db.UpsertMovieRequest(ctx, newExternalID, tmdbID, "Delete Movie", 2024)
	if err != nil {
		t.Fatal(err)
	}
	if newLibraryItemID == 0 || !created {
		t.Fatalf("completed cleanup blocked new intentional request: id=%d created=%t", newLibraryItemID, created)
	}
	defer sqlDB.ExecContext(ctx, `delete from movies where tmdb_id = $1`, tmdbID)
	defer sqlDB.ExecContext(ctx, `delete from media_requests where external_id = $1`, newExternalID)
}

func TestDeleteEpisodeRemovesWholeShowAndSyntheticRequests(t *testing.T) {
	db, sqlDB, ctx := openPublicationTestDB(t)
	suffix := time.Now().UnixNano()
	baseID := fmt.Sprintf("%d", suffix)
	tmdbID := suffix
	tvdbID := suffix + 1
	firstExternalID := baseID + "-s1e1"
	secondExternalID := baseID + "-s1e2"
	firstID, _, err := db.UpsertEpisodeRequest(ctx, firstExternalID, tvdbID, tmdbID, "Delete Show", 2024, 1, 1, "One")
	if err != nil {
		t.Fatal(err)
	}
	secondID, _, err := db.UpsertEpisodeRequest(ctx, secondExternalID, tvdbID, tmdbID, "Delete Show", 2024, 1, 2, "Two")
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from media_requests where split_part(external_id, '-', 1) = $1`, baseID)

	record, err := db.DeleteMediaByLibraryItem(ctx, firstID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = sqlDB.ExecContext(context.Background(), `delete from media_cleanup_jobs where id = $1`, record.CleanupJob.ID)
	})
	if record.CleanupJob.MediaType != "tv" || len(record.LibraryItemIDs) != 2 || record.RequestsDeleted != 2 {
		t.Fatalf("unexpected show deletion record: %+v", record)
	}
	assertDatabaseCount(t, ctx, sqlDB, 0, `select count(*) from library_items where id in ($1, $2)`, firstID, secondID)
	assertDatabaseCount(t, ctx, sqlDB, 0, `select count(*) from tv_shows where tmdb_id = $1`, tmdbID)
	assertDatabaseCount(t, ctx, sqlDB, 0, `select count(*) from media_requests where split_part(external_id, '-', 1) = $1`, baseID)
}

func addDeletionReleaseFixture(t *testing.T, ctx context.Context, sqlDB *sql.DB, libraryItemID, suffix int64) (int64, int64) {
	t.Helper()
	var candidateID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, $2, $3, 'test-indexer') returning id`,
		libraryItemID, fmt.Sprintf("delete-release-%d", suffix), fmt.Sprintf("http://example/%d", suffix),
	).Scan(&candidateID); err != nil {
		t.Fatal(err)
	}
	var selectedReleaseID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libraryItemID, candidateID,
	).Scan(&selectedReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `update queue_items set selected_release_id = $2 where library_item_id = $1`, libraryItemID, selectedReleaseID); err != nil {
		t.Fatal(err)
	}
	var virtualFileID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, reader_kind)
		values ($1, $2, 'delete.mkv', 'direct') returning id`, selectedReleaseID, fmt.Sprintf("releases/%d/delete.mkv", selectedReleaseID),
	).Scan(&virtualFileID); err != nil {
		t.Fatal(err)
	}
	return selectedReleaseID, virtualFileID
}

func assertDatabaseCount(t *testing.T, ctx context.Context, sqlDB *sql.DB, want int, query string, args ...any) {
	t.Helper()
	var got int
	if err := sqlDB.QueryRowContext(ctx, query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("count mismatch: got %d want %d for %s", got, want, query)
	}
}

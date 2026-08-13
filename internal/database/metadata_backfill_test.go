package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func openMetadataBackfillTestDB(t *testing.T) (*DB, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return &DB{SQL: sqlDB}, sqlDB
}

func containsMetadataTarget(targets []MetadataBackfillTarget, libraryItemID int64) bool {
	for _, target := range targets {
		if target.LibraryItemID == libraryItemID {
			return true
		}
	}
	return false
}

func TestMetadataBackfillTreatsLegitimateNullMovieFieldsAsRefreshed(t *testing.T) {
	db, sqlDB := openMetadataBackfillTestDB(t)
	ctx := context.Background()
	tmdbID := time.Now().UnixNano()
	title := fmt.Sprintf("metadata-null-movie-%d", tmdbID)

	var movieID, libraryItemID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into movies (tmdb_id, title, release_year)
		values ($1, $2, 2026)
		returning id`, tmdbID, title).Scan(&movieID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sqlDB.ExecContext(context.Background(), `delete from movies where id = $1`, movieID) })
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, movie_id, title, available)
		values ('movie', $1, $2, false)
		returning id`, movieID, title).Scan(&libraryItemID); err != nil {
		t.Fatal(err)
	}

	targets, err := db.ListMetadataBackfillTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !containsMetadataTarget(targets, libraryItemID) {
		t.Fatal("movie missing optional metadata was not selected for its first refresh")
	}

	if err := db.EnrichMovieFull(ctx, libraryItemID, MovieEnrichment{TMDBID: tmdbID, Title: title}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordMetadataRefreshOutcome(ctx, libraryItemID, "movie", MetadataRefreshSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	var (
		status                              string
		refreshError                        sql.NullString
		taglineNull, releaseDateNull, timed bool
	)
	if err := sqlDB.QueryRowContext(ctx, `
		select metadata_refresh_status,
		       metadata_refresh_error,
		       tagline is null,
		       release_date is null,
		       metadata_refresh_attempted_at is not null
		from movies where id = $1`, movieID).Scan(&status, &refreshError, &taglineNull, &releaseDateNull, &timed); err != nil {
		t.Fatal(err)
	}
	if status != string(MetadataRefreshSucceeded) || refreshError.Valid || !taglineNull || !releaseDateNull || !timed {
		t.Fatalf("unexpected successful-null outcome status=%q error=%v taglineNull=%v releaseDateNull=%v timed=%v", status, refreshError, taglineNull, releaseDateNull, timed)
	}
	targets, err = db.ListMetadataBackfillTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if containsMetadataTarget(targets, libraryItemID) {
		t.Fatal("successful refresh with legitimate null fields remained eligible")
	}

	if err := db.RecordMetadataRefreshOutcome(ctx, libraryItemID, "movie", MetadataRefreshFailed, "provider timeout"); err != nil {
		t.Fatal(err)
	}
	targets, err = db.ListMetadataBackfillTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !containsMetadataTarget(targets, libraryItemID) {
		t.Fatal("failed refresh did not remain eligible for retry")
	}
}

func TestEnrichTVFullUpdatesShowOnceAndSynchronizesEpisodeTitles(t *testing.T) {
	db, sqlDB := openMetadataBackfillTestDB(t)
	ctx := context.Background()
	tmdbID := time.Now().UnixNano()
	tvdbID := tmdbID + 1
	oldTitle := fmt.Sprintf("metadata-old-show-%d", tmdbID)
	newTitle := fmt.Sprintf("metadata-new-show-%d", tmdbID)

	var showID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into tv_shows (tmdb_id, tvdb_id, title, release_year)
		values ($1, $2, $3, 2026)
		returning id`, tmdbID, tvdbID, oldTitle).Scan(&showID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sqlDB.ExecContext(context.Background(), `delete from tv_shows where id = $1`, showID) })

	libraryIDs := make([]int64, 0, 2)
	for episodeNumber := 1; episodeNumber <= 2; episodeNumber++ {
		var episodeID, libraryItemID int64
		if err := sqlDB.QueryRowContext(ctx, `
			insert into episodes (tv_show_id, season_number, episode_number, title)
			values ($1, 1, $2, $3)
			returning id`, showID, episodeNumber, fmt.Sprintf("Episode %d", episodeNumber)).Scan(&episodeID); err != nil {
			t.Fatal(err)
		}
		if err := sqlDB.QueryRowContext(ctx, `
			insert into library_items (media_type, episode_id, title, available)
			values ('episode', $1, $2, false)
			returning id`, episodeID, oldTitle).Scan(&libraryItemID); err != nil {
			t.Fatal(err)
		}
		libraryIDs = append(libraryIDs, libraryItemID)
	}

	if err := db.EnrichTVFull(ctx, libraryIDs[0], "Episode 1 Updated", TVShowEnrichment{TMDBID: tmdbID, ShowTitle: newTitle}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordMetadataRefreshOutcome(ctx, libraryIDs[0], "episode", MetadataRefreshSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	rows, err := sqlDB.QueryContext(ctx, `
		select title
		from library_items
		where id = any($1::bigint[])
		order by id`, libraryIDs)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	wantTitles := []string{newTitle + " S01E01", newTitle + " S01E02"}
	var gotTitles []string
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			t.Fatal(err)
		}
		gotTitles = append(gotTitles, title)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(gotTitles) != fmt.Sprint(wantTitles) {
		t.Fatalf("library titles = %v, want %v", gotTitles, wantTitles)
	}

	var status string
	if err := sqlDB.QueryRowContext(ctx, `select metadata_refresh_status from tv_shows where id = $1`, showID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(MetadataRefreshSucceeded) {
		t.Fatalf("metadata refresh status = %q, want success", status)
	}
	targets, err := db.ListMetadataBackfillTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, libraryItemID := range libraryIDs {
		if containsMetadataTarget(targets, libraryItemID) {
			t.Fatal("successfully refreshed show remained eligible despite legitimate null metadata")
		}
	}
}

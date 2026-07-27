package database

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestGetLibrarySearchInputPopulatesEpisodeYear guards the fix for a real
// wrong-show selection reported live (2026-07-28): ranking needs a specific
// episode's own air-date year (not just the show's first-air-date year) to
// tell a legitimate later-season release (e.g. "Bones.S02E01.2006" for a
// show that debuted in 2005) apart from an actual wrong-show match. This
// guards that GetLibrarySearchInput's EpisodeYear column is wired up and
// extracts the right year from episodes.air_date.
func TestGetLibrarySearchInputPopulatesEpisodeYear(t *testing.T) {
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()
	db := &DB{SQL: sqlDB}

	var tvShowID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into tv_shows (title, release_year) values ('episode-year-show', 2005) returning id`,
	).Scan(&tvShowID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from tv_shows where id = $1`, tvShowID)

	var episodeID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into episodes (tv_show_id, season_number, episode_number, title, air_date)
		values ($1, 2, 1, 'Season 2 Premiere', '2006-09-05'::date)
		returning id`, tvShowID,
	).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, episode_id, available)
		values ('episode', 'episode-year-show', $1, false)
		returning id`, episodeID,
	).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	input, err := db.GetLibrarySearchInput(ctx, libID)
	if err != nil {
		t.Fatal(err)
	}
	if input.ShowYear != 2005 {
		t.Errorf("expected ShowYear 2005, got %d", input.ShowYear)
	}
	if input.EpisodeYear != 2006 {
		t.Errorf("expected EpisodeYear 2006 (from air_date), got %d", input.EpisodeYear)
	}
}

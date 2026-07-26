package catalog

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/drakkar-media/drakkar/internal/database"
)

// TestReleaseCalendarIncludesEpisodeDetail guards a real gap reported live
// (2026-07-26): the calendar only ever showed a TV entry's SHOW name, never
// which season/episode was airing on that date -- the SQL joined episodes
// and even filtered on season_number/episode_number, but never selected
// them, so the frontend had nothing to render regardless of its own markup.
func TestReleaseCalendarIncludesEpisodeDetail(t *testing.T) {
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
	db := &database.DB{SQL: sqlDB}
	svc := NewService(db, nil)

	var tvShowID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into tv_shows (title) values ('calendar-episode-detail-show') returning id`,
	).Scan(&tvShowID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from tv_shows where id = $1`, tvShowID)

	airDate := time.Now().UTC().Format("2006-01-02")
	var episodeID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into episodes (tv_show_id, season_number, episode_number, title, air_date)
		values ($1, 3, 7, 'The Long Way Round', $2::date)
		returning id`, tvShowID, airDate,
	).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, episode_id, available)
		values ('episode', 'calendar-episode-detail-show', $1, false)
		returning id`, episodeID,
	).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	month := time.Now().UTC().Format("2006-01")
	entries, err := svc.ReleaseCalendar(ctx, month)
	if err != nil {
		t.Fatal(err)
	}

	var found *CalendarEntry
	for i := range entries {
		if entries[i].LibraryItemID == libID {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected library item %d in this month's calendar entries, got %d entries", libID, len(entries))
	}
	if found.SeasonNumber != 3 {
		t.Errorf("expected SeasonNumber=3, got %d", found.SeasonNumber)
	}
	if found.EpisodeNumber != 7 {
		t.Errorf("expected EpisodeNumber=7, got %d", found.EpisodeNumber)
	}
	if found.EpisodeTitle != "The Long Way Round" {
		t.Errorf("expected EpisodeTitle %q, got %q", "The Long Way Round", found.EpisodeTitle)
	}
	if found.Title != "calendar-episode-detail-show" {
		t.Errorf("expected show Title to remain the show name, got %q", found.Title)
	}
}

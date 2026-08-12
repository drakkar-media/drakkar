package catalog

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/tmdb"
)

// airDateBackfillTMDBStub returns a single season with a single episode
// carrying a real AirDate, so buildTVSeasons has a fresh TMDB value to
// compare against (and, when the local column is empty, persist).
type airDateBackfillTMDBStub struct {
	seasonNumber, episodeNumber int
	airDate                     string
}

func (s airDateBackfillTMDBStub) Enabled() bool { return true }
func (s airDateBackfillTMDBStub) Search(ctx context.Context, mediaType, query string) ([]tmdb.MediaSummary, error) {
	return nil, nil
}
func (s airDateBackfillTMDBStub) Trending(ctx context.Context, mediaType string) ([]tmdb.MediaSummary, error) {
	return nil, nil
}
func (s airDateBackfillTMDBStub) TrendingPage(ctx context.Context, mediaType string, page int) (tmdb.ListResult, error) {
	return tmdb.ListResult{}, nil
}
func (s airDateBackfillTMDBStub) MovieDetails(ctx context.Context, tmdbID int64) (tmdb.MovieDetails, error) {
	return tmdb.MovieDetails{}, nil
}
func (s airDateBackfillTMDBStub) TVDetails(ctx context.Context, tmdbID int64) (tmdb.TVDetails, error) {
	return tmdb.TVDetails{}, nil
}
func (s airDateBackfillTMDBStub) TVSeasonNumbers(ctx context.Context, tmdbID int64) ([]int, error) {
	return []int{s.seasonNumber}, nil
}
func (s airDateBackfillTMDBStub) TVSeason(ctx context.Context, tmdbID int64, seasonNumber int) (tmdb.TVSeason, error) {
	return tmdb.TVSeason{
		SeasonNumber: seasonNumber,
		Name:         "Season",
		Episodes: []tmdb.TVEpisode{
			{EpisodeNumber: s.episodeNumber, Name: "Episode", AirDate: s.airDate},
		},
	}, nil
}

// TestBuildTVSeasonsBackfillsMissingLocalAirDate guards a real gap
// confirmed live (2026-08-12): Reacher S04's episodes were added to the
// library before TMDB had assigned them air dates, so episodes.air_date
// stayed permanently empty locally. The Details page never noticed --
// buildTVSeasons falls back to a live TMDB fetch for display -- but
// release-calendar reads episodes.air_date directly in bulk with no
// per-row TMDB round trip, so it silently excluded these episodes even
// after TMDB confirmed real air dates. buildTVSeasons must now persist a
// freshly-discovered TMDB air date back to the local column the moment it
// notices the gap, so anything else reading air_date in bulk catches up.
func TestBuildTVSeasonsBackfillsMissingLocalAirDate(t *testing.T) {
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

	var tvShowID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into tv_shows (title, tmdb_id) values ('air-date-backfill-show', 999001) returning id`,
	).Scan(&tvShowID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from tv_shows where id = $1`, tvShowID)

	// air_date deliberately NULL: this is the exact state an episode is left
	// in when added to the library before TMDB assigns it a real date.
	var episodeID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into episodes (tv_show_id, season_number, episode_number, title)
		values ($1, 4, 1, 'Episode 1')
		returning id`, tvShowID,
	).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, episode_id, available)
		values ('episode', 'air-date-backfill-show', $1, false)
		returning id`, episodeID,
	).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	const freshAirDate = "2026-08-12"
	svc := NewService(db, airDateBackfillTMDBStub{seasonNumber: 4, episodeNumber: 1, airDate: freshAirDate})

	seasons, err := svc.buildTVSeasons(ctx, LibraryDetail{TVShowID: tvShowID, TMDBID: 999001})
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 1 || len(seasons[0].Episodes) != 1 {
		t.Fatalf("expected exactly 1 season with 1 episode, got %+v", seasons)
	}
	if got := seasons[0].Episodes[0].AirDate; got != freshAirDate {
		t.Errorf("expected the returned episode AirDate to be the fresh TMDB value %q, got %q", freshAirDate, got)
	}

	var storedAirDate sql.NullString
	if err := sqlDB.QueryRowContext(ctx, `select air_date::text from episodes where id = $1`, episodeID).Scan(&storedAirDate); err != nil {
		t.Fatal(err)
	}
	if !storedAirDate.Valid || storedAirDate.String != freshAirDate {
		t.Errorf("expected buildTVSeasons to persist the fresh air date back to episodes.air_date, got %+v", storedAirDate)
	}
}

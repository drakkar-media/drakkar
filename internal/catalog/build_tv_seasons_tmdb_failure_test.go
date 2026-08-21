package catalog

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/tmdb"
)

// partialFailureTMDBStub reports two seasons but fails TVSeason for one of
// them, simulating a transient per-season TMDB error.
type partialFailureTMDBStub struct {
	seasonNumbers  []int
	failSeason     int
	okEpisodeCount int
}

func (s partialFailureTMDBStub) Enabled() bool { return true }
func (s partialFailureTMDBStub) Search(ctx context.Context, mediaType, query string) ([]tmdb.MediaSummary, error) {
	return nil, nil
}
func (s partialFailureTMDBStub) Trending(ctx context.Context, mediaType string) ([]tmdb.MediaSummary, error) {
	return nil, nil
}
func (s partialFailureTMDBStub) TrendingPage(ctx context.Context, mediaType string, page int) (tmdb.ListResult, error) {
	return tmdb.ListResult{}, nil
}
func (s partialFailureTMDBStub) MovieDetails(ctx context.Context, tmdbID int64) (tmdb.MovieDetails, error) {
	return tmdb.MovieDetails{}, nil
}
func (s partialFailureTMDBStub) TVDetails(ctx context.Context, tmdbID int64) (tmdb.TVDetails, error) {
	return tmdb.TVDetails{}, nil
}
func (s partialFailureTMDBStub) TVSeasonNumbers(ctx context.Context, tmdbID int64) ([]int, error) {
	return s.seasonNumbers, nil
}
func (s partialFailureTMDBStub) TVSeason(ctx context.Context, tmdbID int64, seasonNumber int) (tmdb.TVSeason, error) {
	if seasonNumber == s.failSeason {
		return tmdb.TVSeason{}, errors.New("tmdb: transient upstream error")
	}
	episodes := make([]tmdb.TVEpisode, s.okEpisodeCount)
	for i := range episodes {
		episodes[i] = tmdb.TVEpisode{EpisodeNumber: i + 1, Name: "Episode"}
	}
	return tmdb.TVSeason{SeasonNumber: seasonNumber, Name: "Season", Episodes: episodes}, nil
}

// TestBuildTVSeasonsFallsBackToLocalDataWhenTMDBSeasonCallFails guards a real
// gap: a TVSeason() failure for ONE season used to drop that season from the
// response entirely (a bare `continue`), silently corrupting the whole
// show's AvailableCount/MissingCount rollup and hiding the season from the
// UI, instead of falling back to whatever local data is already loaded for
// it -- exactly like the no-TMDB-at-all path already does.
func TestBuildTVSeasonsFallsBackToLocalDataWhenTMDBSeasonCallFails(t *testing.T) {
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
		insert into tv_shows (title, tmdb_id) values ('tmdb-season-failure-show', 999002) returning id`,
	).Scan(&tvShowID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from tv_shows where id = $1`, tvShowID)

	// Season 1 episode 1: available locally. Season 2 (the one TMDB will
	// fail for) episode 1: available locally too -- if the old bare
	// `continue` bug is present, this whole season and its available
	// episode vanish from the result.
	var s1e1ID, s2e1ID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into episodes (tv_show_id, season_number, episode_number, title)
		values ($1, 1, 1, 'S1E1') returning id`, tvShowID,
	).Scan(&s1e1ID); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into episodes (tv_show_id, season_number, episode_number, title)
		values ($1, 2, 1, 'S2E1') returning id`, tvShowID,
	).Scan(&s2e1ID); err != nil {
		t.Fatal(err)
	}
	var libS1E1, libS2E1 int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, episode_id, available)
		values ('episode', 'tmdb-season-failure-show', $1, true) returning id`, s1e1ID,
	).Scan(&libS1E1); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libS1E1)
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, episode_id, available)
		values ('episode', 'tmdb-season-failure-show', $1, true) returning id`, s2e1ID,
	).Scan(&libS2E1); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libS2E1)

	svc := NewService(db, partialFailureTMDBStub{seasonNumbers: []int{1, 2}, failSeason: 2, okEpisodeCount: 1})

	seasons, err := svc.buildTVSeasons(ctx, LibraryDetail{TVShowID: tvShowID, TMDBID: 999002})
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 2 {
		t.Fatalf("expected both seasons present despite season 2's TMDB call failing, got %+v", seasons)
	}

	var season2 *SeasonDetail
	for i := range seasons {
		if seasons[i].SeasonNumber == 2 {
			season2 = &seasons[i]
		}
	}
	if season2 == nil {
		t.Fatalf("season 2 was dropped from the result entirely, got %+v", seasons)
	}
	if season2.AvailableCount != 1 || len(season2.Episodes) != 1 {
		t.Fatalf("expected season 2's fallback to reflect its 1 locally-available episode, got %+v", season2)
	}
}

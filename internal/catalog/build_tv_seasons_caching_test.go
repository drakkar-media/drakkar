package catalog

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/tmdb"
)

// countingTMDBStub reports N seasons, each with one episode, and counts how
// many times TVSeasonNumbers/TVSeason are actually invoked -- used to prove
// buildTVSeasons caches season-level TMDB responses across calls instead of
// re-fetching every season on every invocation.
type countingTMDBStub struct {
	seasonCount               int
	seasonNumbersCalls        atomic.Int32
	seasonCallsBySeasonNumber sync.Map
}

func (s *countingTMDBStub) Enabled() bool { return true }
func (s *countingTMDBStub) Search(ctx context.Context, mediaType, query string) ([]tmdb.MediaSummary, error) {
	return nil, nil
}
func (s *countingTMDBStub) Trending(ctx context.Context, mediaType string) ([]tmdb.MediaSummary, error) {
	return nil, nil
}
func (s *countingTMDBStub) TrendingPage(ctx context.Context, mediaType string, page int) (tmdb.ListResult, error) {
	return tmdb.ListResult{}, nil
}
func (s *countingTMDBStub) MovieDetails(ctx context.Context, tmdbID int64) (tmdb.MovieDetails, error) {
	return tmdb.MovieDetails{}, nil
}
func (s *countingTMDBStub) TVDetails(ctx context.Context, tmdbID int64) (tmdb.TVDetails, error) {
	return tmdb.TVDetails{}, nil
}
func (s *countingTMDBStub) TVSeasonNumbers(ctx context.Context, tmdbID int64) ([]int, error) {
	s.seasonNumbersCalls.Add(1)
	numbers := make([]int, s.seasonCount)
	for i := range numbers {
		numbers[i] = i + 1
	}
	return numbers, nil
}
func (s *countingTMDBStub) TVSeason(ctx context.Context, tmdbID int64, seasonNumber int) (tmdb.TVSeason, error) {
	v, _ := s.seasonCallsBySeasonNumber.LoadOrStore(seasonNumber, new(atomic.Int32))
	v.(*atomic.Int32).Add(1)
	return tmdb.TVSeason{
		SeasonNumber: seasonNumber,
		Name:         "Season",
		Episodes:     []tmdb.TVEpisode{{EpisodeNumber: 1, Name: "Episode"}},
	}, nil
}

// TestBuildTVSeasonsCachesTMDBSeasonResponsesAcrossCalls guards the "no
// caching" half of the finding at service.go:1139: the weekly air-date
// backfill task calls buildTVSeasons for every show, and a live details-page
// view for the same show shortly after used to re-fetch every season from
// TMDB all over again. A second buildTVSeasons call for the same show must
// reuse the cached TVSeasonNumbers/TVSeason responses instead of re-issuing
// them.
func TestBuildTVSeasonsCachesTMDBSeasonResponsesAcrossCalls(t *testing.T) {
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
		insert into tv_shows (title, tmdb_id) values ('tmdb-season-caching-show', 999003) returning id`,
	).Scan(&tvShowID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from tv_shows where id = $1`, tvShowID)

	stub := &countingTMDBStub{seasonCount: 8}
	svc := NewService(db, stub)

	if _, err := svc.buildTVSeasons(ctx, LibraryDetail{TVShowID: tvShowID, TMDBID: 999003}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.buildTVSeasons(ctx, LibraryDetail{TVShowID: tvShowID, TMDBID: 999003}); err != nil {
		t.Fatal(err)
	}

	if got := stub.seasonNumbersCalls.Load(); got != 1 {
		t.Errorf("expected TVSeasonNumbers to be called once (cached on the 2nd call), got %d", got)
	}
	stub.seasonCallsBySeasonNumber.Range(func(_, v any) bool {
		if got := v.(*atomic.Int32).Load(); got != 1 {
			t.Errorf("expected each season to be fetched once (cached on the 2nd call), got %d", got)
		}
		return true
	})
}

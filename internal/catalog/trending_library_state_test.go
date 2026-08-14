package catalog

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/drakkar-media/drakkar/internal/database"
)

// TestTrendingLibraryStateMergesMovieAndTV guards a real gap reported live
// (2026-07-27): Dashboard's trending rails are built purely from TMDB
// metadata (summariesToCards), which never sets ID -- so every trending
// card always reported "not in library" (ID 0), and the frontend's "+
// request" button never disappeared even after a title was successfully
// requested and downloaded. trendingLibraryState/mergeTrendingLibraryState
// cross-reference trending TMDB ids against the local library so already-
// tracked titles report their real id/availability/queue state.
func TestTrendingLibraryStateMergesMovieAndTV(t *testing.T) {
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

	const movieTMDBID = 990001001
	const tvTMDBID = 990001002
	const untrackedTMDBID = 990001003

	var movieID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into movies (title, tmdb_id) values ('trending-state-movie', $1) returning id`,
		movieTMDBID,
	).Scan(&movieID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from movies where id = $1`, movieID)

	var movieLibID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, movie_id, available)
		values ('movie', 'trending-state-movie', $1, true)
		returning id`, movieID,
	).Scan(&movieLibID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, movieLibID)

	var tvShowID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into tv_shows (title, tmdb_id) values ('trending-state-show', $1) returning id`,
		tvTMDBID,
	).Scan(&tvShowID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from tv_shows where id = $1`, tvShowID)

	var episodeID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into episodes (tv_show_id, season_number, episode_number, title)
		values ($1, 1, 1, 'Pilot') returning id`, tvShowID,
	).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}

	var tvLibID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, episode_id, available)
		values ('episode', 'trending-state-show', $1, false)
		returning id`, episodeID,
	).Scan(&tvLibID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, tvLibID)

	movieState, err := svc.trendingLibraryState(ctx, "movie", []int64{movieTMDBID, untrackedTMDBID})
	if err != nil {
		t.Fatal(err)
	}
	tvState, err := svc.trendingLibraryState(ctx, "tv", []int64{tvTMDBID, untrackedTMDBID})
	if err != nil {
		t.Fatal(err)
	}

	cards := []MediaCard{
		{TMDBID: movieTMDBID, MediaType: "movie"},
		{TMDBID: untrackedTMDBID, MediaType: "movie"},
	}
	mergeTrendingLibraryState(cards, movieState)
	if cards[0].ID != movieLibID {
		t.Errorf("expected tracked movie card to get local ID %d, got %d", movieLibID, cards[0].ID)
	}
	if !cards[0].Available {
		t.Error("expected tracked movie card to report Available=true")
	}
	if cards[1].ID != 0 {
		t.Errorf("expected untracked movie card to keep ID 0, got %d", cards[1].ID)
	}

	tvCards := []MediaCard{{TMDBID: tvTMDBID, MediaType: "tv"}}
	mergeTrendingLibraryState(tvCards, tvState)
	if tvCards[0].ID == 0 {
		t.Error("expected tracked TV card to get a non-zero local ID")
	}
	if tvCards[0].Available {
		t.Error("expected tracked TV card (episode not yet available) to report Available=false")
	}
}

func TestTVCardsPreferOfficialEpisodeTotalOverGhostRows(t *testing.T) {
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
		insert into tv_shows (title, tmdb_id, number_of_episodes)
		values ('ghost-row-complete-show', 990001004, 2)
		returning id`,
	).Scan(&tvShowID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from tv_shows where id = $1`, tvShowID)

	for episodeNumber := 1; episodeNumber <= 3; episodeNumber++ {
		var episodeID int64
		if err := sqlDB.QueryRowContext(ctx, `
			insert into episodes (tv_show_id, season_number, episode_number, title)
			values ($1, 1, $2, $3)
			returning id`, tvShowID, episodeNumber, "episode",
		).Scan(&episodeID); err != nil {
			t.Fatal(err)
		}
		available := episodeNumber <= 2
		state := database.QueueAvailable
		failureReason := ""
		if !available {
			state = database.QueueFailed
			failureReason = "no_releases"
		}
		var libID int64
		if err := sqlDB.QueryRowContext(ctx, `
			insert into library_items (media_type, title, episode_id, available)
			values ('episode', 'ghost-row-complete-show', $1, $2)
			returning id`, episodeID, available,
		).Scan(&libID); err != nil {
			t.Fatal(err)
		}
		if _, err := sqlDB.ExecContext(ctx, `
			insert into queue_items (library_item_id, state, failure_reason, idempotency_key)
			values ($1, $2, $3, $4)`, libID, state, failureReason, "ghost-row-complete-show-"+strconv.Itoa(episodeNumber),
		); err != nil {
			t.Fatal(err)
		}
	}

	cards, err := svc.ListLibraryCards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *MediaCard
	for i := range cards {
		if cards[i].TVShowID == tvShowID {
			found = &cards[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected complete show card")
	}
	if !found.Available {
		t.Fatalf("expected show to remain available despite failed ghost row, got queue state %q", found.QueueState)
	}
	if found.AvailableCount != 2 || found.MissingCount != 0 {
		t.Fatalf("expected 2/2 availability, got available=%d missing=%d", found.AvailableCount, found.MissingCount)
	}
	if found.QueueState != string(database.QueueAvailable) {
		t.Fatalf("expected queue state available, got %q", found.QueueState)
	}
}

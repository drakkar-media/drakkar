package database

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// setupPendingMovie inserts a movie whose release_date is `dateOffsetDays`
// days from today (0 = today, -1 = yesterday, +1 = tomorrow), plus a
// library_item + queue_item in state 'requested' eligible for search.
// Deleting the returned movie id cascades through library_items/queue_items.
func setupPendingMovie(t *testing.T, ctx context.Context, sqlDB *sql.DB, title string, dateOffsetDays int) (movieID, libID int64) {
	t.Helper()
	if err := sqlDB.QueryRowContext(ctx, `
		insert into movies (title, release_date)
		values ($1, current_date + make_interval(days => $2::int))
		returning id`, title, dateOffsetDays).Scan(&movieID); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, movie_id, available)
		values ('movie', $1, $2, false)
		returning id`, title, movieID).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into queue_items (library_item_id, state, idempotency_key)
		values ($1, 'requested', $2)`, libID, title,
	); err != nil {
		t.Fatal(err)
	}
	return movieID, libID
}

// setupPendingEpisode inserts a TV episode whose air_date is `dateOffsetDays`
// days from today, plus a library_item + queue_item eligible for search.
// Deleting the returned tv_show id cascades through episodes/library_items/
// queue_items.
func setupPendingEpisode(t *testing.T, ctx context.Context, sqlDB *sql.DB, title string, dateOffsetDays int) (tvShowID, libID int64) {
	t.Helper()
	if err := sqlDB.QueryRowContext(ctx, `
		insert into tv_shows (title) values ($1) returning id`, title,
	).Scan(&tvShowID); err != nil {
		t.Fatal(err)
	}
	var episodeID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into episodes (tv_show_id, season_number, episode_number, title, air_date)
		values ($1, 1, 1, $2, current_date + make_interval(days => $3::int))
		returning id`, tvShowID, title, dateOffsetDays).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, episode_id, available)
		values ('episode', $1, $2, false)
		returning id`, title, episodeID).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into queue_items (library_item_id, state, idempotency_key)
		values ($1, 'requested', $2)`, libID, title,
	); err != nil {
		t.Fatal(err)
	}
	return tvShowID, libID
}

// setupFailedMovie mirrors setupPendingMovie but leaves the queue_item in
// 'failed' state with no last_searched_at, matching a movie that already
// exhausted its known candidates and is now eligible for the retry pass.
func setupFailedMovie(t *testing.T, ctx context.Context, sqlDB *sql.DB, title string, dateOffsetDays int) (movieID, queueItemID int64) {
	t.Helper()
	if err := sqlDB.QueryRowContext(ctx, `
		insert into movies (title, release_date)
		values ($1, current_date + make_interval(days => $2::int))
		returning id`, title, dateOffsetDays).Scan(&movieID); err != nil {
		t.Fatal(err)
	}
	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, movie_id, available)
		values ('movie', $1, $2, false)
		returning id`, title, movieID).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into queue_items (library_item_id, state, idempotency_key)
		values ($1, 'failed', $2)
		returning id`, libID, title,
	).Scan(&queueItemID); err != nil {
		t.Fatal(err)
	}
	return movieID, queueItemID
}

// TestListFailedQueueRetryTargetsSkipsUnreleasedMovies guards a real gap
// confirmed live (2026-07-26): this query had NO movie release_date check at
// all, unlike ListPendingLibrarySearchTargets -- a failed movie item whose
// release date later slipped into the future (a real-world release delay)
// would still be retried by the housekeeping pass with no protection.
func TestListFailedQueueRetryTargetsSkipsUnreleasedMovies(t *testing.T) {
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

	releasedMovie, releasedQueueID := setupFailedMovie(t, ctx, sqlDB, "grace-failed-movie-released", -1)
	delayedMovie, delayedQueueID := setupFailedMovie(t, ctx, sqlDB, "grace-failed-movie-delayed", 1)
	defer func() {
		for _, id := range []int64{releasedMovie, delayedMovie} {
			sqlDB.ExecContext(ctx, `delete from movies where id = $1`, id)
		}
	}()

	targets, err := db.ListFailedQueueRetryTargets(ctx, 0, 12)
	if err != nil {
		t.Fatal(err)
	}
	byQueueID := make(map[int64]bool, len(targets))
	for _, tg := range targets {
		byQueueID[tg.QueueItemID] = true
	}
	if !byQueueID[releasedQueueID] {
		t.Error("expected a failed movie item already released to remain eligible for retry")
	}
	if byQueueID[delayedQueueID] {
		t.Error("expected a failed movie item whose release date slipped into the future to be excluded from retry")
	}
}

// TestListPendingLibrarySearchTargetsAppliesReleaseGraceHours guards the
// 2026-07-26 feature: search eligibility isn't just "release date has
// passed" (a bare calendar-date comparison), it's "release date + a
// configurable grace period has passed" -- since a release posts at a
// specific time, not literally at 00:00 local time the moment the calendar
// date flips. Offsets are chosen so every assertion is deterministic
// regardless of what time of day the test actually runs.
func TestListPendingLibrarySearchTargetsAppliesReleaseGraceHours(t *testing.T) {
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

	movieYesterday, movieYesterdayLib := setupPendingMovie(t, ctx, sqlDB, "grace-movie-yesterday", -1)
	movieTomorrow, movieTomorrowLib := setupPendingMovie(t, ctx, sqlDB, "grace-movie-tomorrow", 1)
	movieTodayNoGrace, movieTodayNoGraceLib := setupPendingMovie(t, ctx, sqlDB, "grace-movie-today-nograce", 0)
	movieTodayLongGrace, movieTodayLongGraceLib := setupPendingMovie(t, ctx, sqlDB, "grace-movie-today-longgrace", 0)
	epYesterday, epYesterdayLib := setupPendingEpisode(t, ctx, sqlDB, "grace-episode-yesterday", -1)
	epTomorrow, epTomorrowLib := setupPendingEpisode(t, ctx, sqlDB, "grace-episode-tomorrow", 1)
	defer func() {
		for _, id := range []int64{movieYesterday, movieTomorrow, movieTodayNoGrace, movieTodayLongGrace} {
			sqlDB.ExecContext(ctx, `delete from movies where id = $1`, id)
		}
		for _, id := range []int64{epYesterday, epTomorrow} {
			sqlDB.ExecContext(ctx, `delete from tv_shows where id = $1`, id)
		}
	}()

	// graceHours=12: yesterday's release/air date is always eligible (its
	// midnight+12h is always in the past relative to "now" today), tomorrow's
	// is always ineligible (its midnight+12h is always still in the future).
	targets, err := db.ListPendingLibrarySearchTargets(ctx, 12)
	if err != nil {
		t.Fatal(err)
	}
	byLibID := make(map[int64]bool, len(targets))
	for _, tg := range targets {
		byLibID[tg.LibraryItemID] = true
	}
	if !byLibID[movieYesterdayLib] {
		t.Error("expected a movie released yesterday to be eligible for search with a 12h grace period")
	}
	if byLibID[movieTomorrowLib] {
		t.Error("expected a movie releasing tomorrow to NOT be eligible for search with a 12h grace period")
	}
	if !byLibID[epYesterdayLib] {
		t.Error("expected an episode that aired yesterday to be eligible for search with a 12h grace period")
	}
	if byLibID[epTomorrowLib] {
		t.Error("expected an episode airing tomorrow to NOT be eligible for search with a 12h grace period")
	}

	// graceHours=0 must preserve the original "eligible the instant the
	// release day starts" behavior for an item releasing today.
	targetsNoGrace, err := db.ListPendingLibrarySearchTargets(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundToday := false
	for _, tg := range targetsNoGrace {
		if tg.LibraryItemID == movieTodayNoGraceLib {
			foundToday = true
		}
	}
	if !foundToday {
		t.Error("expected a movie releasing today to be eligible immediately with graceHours=0 (original release-day behavior)")
	}

	// graceHours=48 must keep a same-day release out entirely (today's
	// midnight + 48h is always at least a day beyond "now").
	targetsLongGrace, err := db.ListPendingLibrarySearchTargets(ctx, 48)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targetsLongGrace {
		if tg.LibraryItemID == movieTodayLongGraceLib {
			t.Error("expected a movie releasing today to NOT be eligible yet with a 48h grace period")
		}
	}
}

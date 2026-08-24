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

	// -2 (not -1) so (release_date+1)+12h deterministically lands in the past
	// regardless of what time of day this test runs -- see the doc comment on
	// TestListPendingLibrarySearchTargetsAppliesReleaseGraceHours.
	releasedMovie, releasedQueueID := setupFailedMovie(t, ctx, sqlDB, "grace-failed-movie-released", -2)
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

// TestListFailedQueueRetryTargetsAnchorsGraceToEndOfReleaseDay mirrors
// TestListPendingLibrarySearchTargetsAppliesReleaseGraceHours's graceHours=0
// same-day case for this query specifically: a movie releasing TODAY must
// stay excluded even with a zero-hour grace period, since the anchor shift
// to (release_date+1) -- not the configured grace value -- is what
// guarantees a full calendar day elapses before eligibility. See that other
// test's doc comment for the full 2026-08-24 incident this guards.
func TestListFailedQueueRetryTargetsAnchorsGraceToEndOfReleaseDay(t *testing.T) {
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

	movieID, queueID := setupFailedMovie(t, ctx, sqlDB, "grace-failed-movie-today-zerograce", 0)
	defer sqlDB.ExecContext(ctx, `delete from movies where id = $1`, movieID)

	targets, err := db.ListFailedQueueRetryTargets(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		if tg.QueueItemID == queueID {
			t.Error("expected a failed movie item releasing today to NOT be eligible for retry yet even with graceHours=0 -- the release day itself must fully elapse first")
		}
	}
}

// TestListFailedQueueRetryTargetsRespectsDispatchBackoffUntil guards a real
// production incident (2026-08-12): consecutive_failure_searches (the
// counter this query's own cooldown escalates on) is reset to 0 by
// ReplaceSearchCandidates every time a search selects ANY candidate at all
// -- which it almost always does for an item with a large candidate pool,
// even though that selection goes on to fail moments later via a completely
// separate code path this counter is never told about. That left the
// cooldown permanently stuck at its base 1-hour tier for such an item, no
// matter how many times it had already failed -- completely uncoordinated
// with the *other*, correctly-escalating dispatch_attempt_count/
// dispatch_backoff_until mechanism (internal/workflow/service.go's
// dispatchBackoff), which had already pushed the same item out to a much
// longer pause. This test confirms the query now also skips an item whose
// dispatch_backoff_until is still in the future, even though its own
// last_searched_at/consecutive_failure_searches cooldown has fully elapsed
// (both null, i.e. "never searched" -- the most permissive case for the
// query's own cooldown).
func TestListFailedQueueRetryTargetsRespectsDispatchBackoffUntil(t *testing.T) {
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

	// -2 (not -1) so both movies deterministically clear the release-date
	// grace check regardless of what time of day this test runs, isolating
	// the assertions below to dispatch_backoff_until specifically -- see the
	// doc comment on TestListPendingLibrarySearchTargetsAppliesReleaseGraceHours.
	backedOffMovie, backedOffQueueID := setupFailedMovie(t, ctx, sqlDB, "backoff-failed-movie-backed-off", -2)
	clearMovie, clearQueueID := setupFailedMovie(t, ctx, sqlDB, "backoff-failed-movie-clear", -2)
	defer func() {
		for _, id := range []int64{backedOffMovie, clearMovie} {
			sqlDB.ExecContext(ctx, `delete from movies where id = $1`, id)
		}
	}()

	if _, err := sqlDB.ExecContext(ctx, `
		update queue_items set dispatch_backoff_until = now() + interval '6 hours' where id = $1`, backedOffQueueID,
	); err != nil {
		t.Fatal(err)
	}

	targets, err := db.ListFailedQueueRetryTargets(ctx, 0, 12)
	if err != nil {
		t.Fatal(err)
	}
	byQueueID := make(map[int64]bool, len(targets))
	for _, tg := range targets {
		byQueueID[tg.QueueItemID] = true
	}
	if byQueueID[backedOffQueueID] {
		t.Error("expected an item still within its dispatch_backoff_until window to be excluded from retry")
	}
	if !byQueueID[clearQueueID] {
		t.Error("expected an item with no dispatch_backoff_until set to remain eligible for retry")
	}
}

// TestListPendingLibrarySearchTargetsIncludesAvailableOrphanWithNoSelection
// guards the second half of the 2026-08-10 stuck-queue fix: a queue_items
// row can end up in state='requested' with selected_release_id=null while
// its library_item is still available=true (e.g. ClearQueueSelectedRelease
// bouncing a no-selection item back to requested after the item was already
// marked available some other way). The normal-pending branch used to
// require li.available=false, permanently excluding these orphans from ever
// being searched again -- confirmed live, ~37 stuck rows this way, on top of
// the ~340 fixed by the resume/stranded branches' available=false removal.
func TestListPendingLibrarySearchTargetsIncludesAvailableOrphanWithNoSelection(t *testing.T) {
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

	var movieID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into movies (title, release_date) values ('orphan-available-movie', current_date - interval '30 days')
		returning id`).Scan(&movieID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from movies where id = $1`, movieID)

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, movie_id, available)
		values ('movie', 'orphan-available-movie', $1, true)
		returning id`, movieID).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into queue_items (library_item_id, state, selected_release_id, idempotency_key)
		values ($1, 'requested', null, 'orphan-available-movie')`, libID,
	); err != nil {
		t.Fatal(err)
	}

	targets, err := db.ListPendingLibrarySearchTargets(ctx, 12)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		if tg.LibraryItemID == libID {
			return
		}
	}
	t.Fatalf("expected available=true orphan (no selected_release_id) to be eligible for search, got targets: %+v", targets)
}

func TestListPendingLibrarySearchTargetsSkipsAvailableResumeSelection(t *testing.T) {
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

	availableMovieID, availableLibID := setupPendingMovie(t, ctx, sqlDB, "resume-selected-available", -30)
	unavailableMovieID, unavailableLibID := setupPendingMovie(t, ctx, sqlDB, "resume-selected-unavailable", -30)
	defer func() {
		for _, id := range []int64{availableMovieID, unavailableMovieID} {
			sqlDB.ExecContext(ctx, `delete from movies where id = $1`, id)
		}
	}()

	makeSelected := func(libID int64, title string) int64 {
		t.Helper()
		var rcID int64
		if err := sqlDB.QueryRowContext(ctx, `
			insert into release_candidates (library_item_id, title, external_url, indexer_name)
			values ($1, $2, $3, 'test-indexer')
			returning id`, libID, title, "http://example/"+title,
		).Scan(&rcID); err != nil {
			t.Fatal(err)
		}
		var srID int64
		if err := sqlDB.QueryRowContext(ctx, `
			insert into selected_releases (library_item_id, release_candidate_id)
			values ($1, $2)
			returning id`, libID, rcID,
		).Scan(&srID); err != nil {
			t.Fatal(err)
		}
		return srID
	}
	availableSRID := makeSelected(availableLibID, "resume-selected-available-release")
	unavailableSRID := makeSelected(unavailableLibID, "resume-selected-unavailable-release")
	if _, err := sqlDB.ExecContext(ctx, `
		update library_items set available = true where id = $1`, availableLibID,
	); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		libID int64
		srID  int64
	}{
		{availableLibID, availableSRID},
		{unavailableLibID, unavailableSRID},
	} {
		if _, err := sqlDB.ExecContext(ctx, `
			update queue_items set state = 'requested', selected_release_id = $2 where library_item_id = $1`, item.libID, item.srID,
		); err != nil {
			t.Fatal(err)
		}
	}

	targets, err := db.ListPendingLibrarySearchTargets(ctx, 12)
	if err != nil {
		t.Fatal(err)
	}
	byLibID := make(map[int64]bool, len(targets))
	for _, tg := range targets {
		byLibID[tg.LibraryItemID] = true
	}
	if byLibID[availableLibID] {
		t.Error("expected available=true requested item with selected release to stay out of passive resume dispatch")
	}
	if !byLibID[unavailableLibID] {
		t.Error("expected unavailable requested item with selected release to remain eligible for passive resume dispatch")
	}
}

func TestListPendingTVShowLibraryItemIDsOrdersByOldestSearch(t *testing.T) {
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
		insert into tv_shows (title) values ('pending-tvshow-priority')
		returning id`,
	).Scan(&tvShowID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from tv_shows where id = $1`, tvShowID)

	insertEpisodeItem := func(title string, episodeNumber int, available bool, searchedAt string) int64 {
		t.Helper()
		var episodeID int64
		if err := sqlDB.QueryRowContext(ctx, `
			insert into episodes (tv_show_id, season_number, episode_number, title, air_date)
			values ($1, 1, $2, $3, current_date - interval '1 day')
			returning id`, tvShowID, episodeNumber, title,
		).Scan(&episodeID); err != nil {
			t.Fatal(err)
		}
		var libID int64
		if err := sqlDB.QueryRowContext(ctx, `
			insert into library_items (media_type, title, episode_id, available)
			values ('episode', $1, $2, $3)
			returning id`, title, episodeID, available,
		).Scan(&libID); err != nil {
			t.Fatal(err)
		}
		query := `insert into queue_items (library_item_id, state, idempotency_key, last_searched_at) values ($1, 'requested', $2, `
		args := []any{libID, title}
		if searchedAt == "" {
			query += `null)`
		} else {
			query += searchedAt + `)`
		}
		if _, err := sqlDB.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
		return libID
	}

	oldSearchLibID := insertEpisodeItem("pending-tvshow-old-search", 1, false, `now() - interval '2 hours'`)
	neverSearchedLibID := insertEpisodeItem("pending-tvshow-never-searched", 2, false, "")
	availableLibID := insertEpisodeItem("pending-tvshow-available", 3, true, "")

	got, err := db.ListPendingTVShowLibraryItemIDs(ctx, tvShowID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 unavailable pending episodes, got %d: %v", len(got), got)
	}
	if got[0] != neverSearchedLibID || got[1] != oldSearchLibID {
		t.Fatalf("expected null last_searched_at first then oldest search, got %v", got)
	}
	for _, id := range got {
		if id == availableLibID {
			t.Fatalf("expected available episode %d to be excluded, got %v", availableLibID, got)
		}
	}
}

// TestListPendingLibrarySearchTargetsAppliesReleaseGraceHours guards the
// 2026-07-26 grace-period feature and its 2026-08-24 anchor fix: search
// eligibility isn't "release date has passed" (a bare calendar-date
// comparison), it's "the release date's entire calendar day has elapsed,
// plus a configurable grace period on top" -- TMDB/TVDB only ever give a
// date, never a time, and a release posts at a specific moment that day
// (often evening, often in a source timezone well ahead of this server's),
// so anchoring the grace window to the release date's own midnight (the
// pre-2026-08-24 behavior) could mark an item searchable many hours BEFORE
// it had actually released anywhere -- confirmed live: an HBO Max episode's
// window opened 15 hours before its real air time once converted to this
// server's clock. The window is now anchored to the START OF THE NEXT DAY
// instead, so a same-day release is never "immediately" eligible even with
// graceHours=0 -- that's the point, not a gap: we don't know what time
// today it released, so we wait for today to fully elapse first.
//
// Offsets are chosen so every assertion is deterministic regardless of what
// time of day the test actually runs: "two days ago" is used (rather than
// "yesterday") for the always-eligible cases, since (twoDaysAgo+1)+12h lands
// at yesterday noon -- always in the past no matter when today the test
// executes -- whereas (yesterday+1)+12h lands at today noon, which would be
// flaky depending on the clock.
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

	movieTwoDaysAgo, movieTwoDaysAgoLib := setupPendingMovie(t, ctx, sqlDB, "grace-movie-twodaysago", -2)
	movieTomorrow, movieTomorrowLib := setupPendingMovie(t, ctx, sqlDB, "grace-movie-tomorrow", 1)
	movieToday, movieTodayLib := setupPendingMovie(t, ctx, sqlDB, "grace-movie-today", 0)
	movieTodayLongGrace, movieTodayLongGraceLib := setupPendingMovie(t, ctx, sqlDB, "grace-movie-today-longgrace", 0)
	epTwoDaysAgo, epTwoDaysAgoLib := setupPendingEpisode(t, ctx, sqlDB, "grace-episode-twodaysago", -2)
	epTomorrow, epTomorrowLib := setupPendingEpisode(t, ctx, sqlDB, "grace-episode-tomorrow", 1)
	defer func() {
		for _, id := range []int64{movieTwoDaysAgo, movieTomorrow, movieToday, movieTodayLongGrace} {
			sqlDB.ExecContext(ctx, `delete from movies where id = $1`, id)
		}
		for _, id := range []int64{epTwoDaysAgo, epTomorrow} {
			sqlDB.ExecContext(ctx, `delete from tv_shows where id = $1`, id)
		}
	}()

	// graceHours=12: a release/air date from two full days ago is always
	// eligible ((date+1)+12h lands at yesterday noon, always past); tomorrow's
	// is always ineligible ((date+1)+12h lands at the day-after-tomorrow noon,
	// always future).
	targets, err := db.ListPendingLibrarySearchTargets(ctx, 12)
	if err != nil {
		t.Fatal(err)
	}
	byLibID := make(map[int64]bool, len(targets))
	for _, tg := range targets {
		byLibID[tg.LibraryItemID] = true
	}
	if !byLibID[movieTwoDaysAgoLib] {
		t.Error("expected a movie released two days ago to be eligible for search with a 12h grace period")
	}
	if byLibID[movieTomorrowLib] {
		t.Error("expected a movie releasing tomorrow to NOT be eligible for search with a 12h grace period")
	}
	if !byLibID[epTwoDaysAgoLib] {
		t.Error("expected an episode that aired two days ago to be eligible for search with a 12h grace period")
	}
	if byLibID[epTomorrowLib] {
		t.Error("expected an episode airing tomorrow to NOT be eligible for search with a 12h grace period")
	}

	// graceHours=0 must now still exclude a same-day release: the anchor
	// shift alone (not the configured grace value) is what guarantees a full
	// calendar day elapses first, since (today+1)+0h is always tomorrow
	// midnight -- always in the future relative to any time today.
	targetsNoGrace, err := db.ListPendingLibrarySearchTargets(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targetsNoGrace {
		if tg.LibraryItemID == movieTodayLib {
			t.Error("expected a movie releasing today to NOT be eligible yet even with graceHours=0 -- the release day itself must fully elapse first")
		}
	}

	// graceHours=48 must also keep a same-day release out entirely.
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

// TestListPendingLibrarySearchTargetsBecomesEligibleTheDayAfterRelease guards
// the other side of the 2026-08-24 anchor fix: a release date that has just
// barely fully elapsed (yesterday, as of any time today) must become
// eligible with a zero grace period, once the anchor's implicit one-day wait
// is satisfied -- this isn't an unbounded extra delay, just "wait for the
// whole release day, then apply whatever grace is configured."
func TestListPendingLibrarySearchTargetsBecomesEligibleTheDayAfterRelease(t *testing.T) {
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

	movieID, libID := setupPendingMovie(t, ctx, sqlDB, "grace-movie-yesterday-zerograce", -1)
	defer sqlDB.ExecContext(ctx, `delete from movies where id = $1`, movieID)

	targets, err := db.ListPendingLibrarySearchTargets(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		if tg.LibraryItemID == libID {
			return
		}
	}
	t.Fatalf("expected a movie released yesterday to be eligible today with graceHours=0, got targets: %+v", targets)
}

// TestListPendingLibrarySearchTargetsAppliesBackoffToRequestedState guards a
// bug confirmed live (2026-08-07): ClearQueueSelectedRelease bounces a
// 'failed' item with no selection back to 'requested' (so RetryFailedQueue's
// periodic pass doesn't loop it forever), but the bounce leaves
// consecutive_failure_searches untouched. The cooldown escalation ladder used
// to key off `state != failed`, so once an item was bounced back to
// 'requested' it fell back to a flat 1h cooldown no matter how many times it
// had already failed -- defeating the escalation entirely and hammering
// Hydra hourly forever for items that had failed dozens of times (a full
// NCIS: LA back-catalog, PAW Patrol, The Odyssey, ...). The escalation must
// key off the counter regardless of current state.
func TestListPendingLibrarySearchTargetsAppliesBackoffToRequestedState(t *testing.T) {
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

	// Bounced-back item: state='requested', but consecutive_failure_searches
	// is high and last_searched_at is 2 hours ago -- inside the old flat 1h
	// window's "eligible" zone, but well short of the escalated >=10 tier's
	// 7-day cooldown, which should exclude it.
	// -2 (not -1) so the movie deterministically clears the release-date grace
	// check regardless of what time of day this test runs, isolating the
	// assertion below to the consecutive_failure_searches escalation
	// specifically -- see the doc comment on
	// TestListPendingLibrarySearchTargetsAppliesReleaseGraceHours.
	movieID, libID := setupPendingMovie(t, ctx, sqlDB, "backoff-requested-bounced", -2)
	defer sqlDB.ExecContext(ctx, `delete from movies where id = $1`, movieID)
	if _, err := sqlDB.ExecContext(ctx, `
		update queue_items
		set consecutive_failure_searches = 12, last_searched_at = now() - interval '2 hours'
		where library_item_id = $1`, libID,
	); err != nil {
		t.Fatal(err)
	}

	targets, err := db.ListPendingLibrarySearchTargets(ctx, 12)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		if tg.LibraryItemID == libID {
			t.Error("expected a 'requested' item with 12 prior consecutive failures to respect the 7-day escalated cooldown, not the flat 1h window")
		}
	}
}

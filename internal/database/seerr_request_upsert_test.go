package database

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestUpsertMovieRequestReusesLibraryItemWhenTmdbIDDrifts guards a real
// production incident confirmed live 2026-08-09: a TMDB ID merge/redirect
// made Seerr start reporting a different tmdb_id for an already-fulfilled
// movie request. UpsertMovieRequest's "library item is gone, recreate"
// fallback (meant for genuine user-initiated deletions) didn't distinguish
// that from "still fully intact, just a different tmdb_id today" -- it tried
// inserting a second movie/library_item/queue_item for the same request,
// colliding on queue_items.idempotency_key (keyed by the Seerr external_id,
// unaffected by tmdb_id drift) and permanently jamming every future sync with
// the same unique-constraint violation, since the failed transaction rolled
// back and left nothing to detect on the next attempt. The fix must resolve
// this via the request's own queue_items row instead of assuming deletion.
func TestUpsertMovieRequestReusesLibraryItemWhenTmdbIDDrifts(t *testing.T) {
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

	const externalID = "drift-movie-1228"
	const oldTmdbID, newTmdbID = 433466, 7551

	libraryItemID, created, err := db.UpsertMovieRequest(ctx, externalID, oldTmdbID, "The Grey Area", 2012)
	if err != nil {
		t.Fatalf("initial upsert: %v", err)
	}
	if !created {
		t.Fatal("expected the initial upsert to report created=true")
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libraryItemID)
	defer sqlDB.ExecContext(ctx, `delete from movies where tmdb_id in ($1, $2)`, oldTmdbID, newTmdbID)
	defer sqlDB.ExecContext(ctx, `delete from media_requests where external_id = $1`, externalID)

	// Seerr now reports a different tmdb_id for the exact same request --
	// the library_item/queue_item from the first call are still fully
	// intact, nothing was deleted.
	gotLibraryItemID, created, err := db.UpsertMovieRequest(ctx, externalID, newTmdbID, "The Grey Area", 2012)
	if err != nil {
		t.Fatalf("drifted upsert: %v", err)
	}
	if created {
		t.Error("expected drifted upsert to report created=false -- the request was already tracked")
	}
	if gotLibraryItemID != libraryItemID {
		t.Errorf("expected the drifted upsert to reuse library_item_id %d, got %d", libraryItemID, gotLibraryItemID)
	}

	var libraryItemCount int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from library_items where movie_id in (select id from movies where tmdb_id in ($1, $2))`, oldTmdbID, newTmdbID).Scan(&libraryItemCount); err != nil {
		t.Fatal(err)
	}
	if libraryItemCount != 1 {
		t.Errorf("expected exactly 1 library_item across old/new tmdb_id, got %d (a duplicate was created)", libraryItemCount)
	}
}

// TestUpsertMovieRequestRecreatesWhenLibraryItemGenuinelyDeleted guards the
// original fallback behavior the fix above must not break: if the
// library_item really was deleted (its queue_items row cascades away with
// it), UpsertMovieRequest must still recreate it rather than getting stuck.
func TestUpsertMovieRequestRecreatesWhenLibraryItemGenuinelyDeleted(t *testing.T) {
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

	const externalID = "deleted-movie-9001"
	const tmdbID = 90010

	libraryItemID, created, err := db.UpsertMovieRequest(ctx, externalID, tmdbID, "Deleted Movie", 2020)
	if err != nil {
		t.Fatalf("initial upsert: %v", err)
	}
	if !created {
		t.Fatal("expected the initial upsert to report created=true")
	}
	defer sqlDB.ExecContext(ctx, `delete from movies where tmdb_id = $1`, tmdbID)
	defer sqlDB.ExecContext(ctx, `delete from media_requests where external_id = $1`, externalID)

	// Simulate a genuine user-initiated deletion: remove the library_item,
	// cascading its queue_items row away too.
	if _, err := sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libraryItemID); err != nil {
		t.Fatal(err)
	}

	newLibraryItemID, created, err := db.UpsertMovieRequest(ctx, externalID, tmdbID, "Deleted Movie", 2020)
	if err != nil {
		t.Fatalf("recreate upsert: %v", err)
	}
	if !created {
		t.Error("expected the recreate upsert to report created=true")
	}
	if newLibraryItemID == libraryItemID {
		t.Error("expected a genuinely deleted library_item to be recreated with a new id")
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, newLibraryItemID)

	var queueItemCount int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from queue_items where library_item_id = $1`, newLibraryItemID).Scan(&queueItemCount); err != nil {
		t.Fatal(err)
	}
	if queueItemCount != 1 {
		t.Errorf("expected exactly 1 queue_item for the recreated library_item, got %d", queueItemCount)
	}
}

// TestUpsertEpisodeRequestReusesLibraryItemWhenTvdbIDDrifts mirrors
// TestUpsertMovieRequestReusesLibraryItemWhenTmdbIDDrifts for the TV path --
// see that test's comment for the production incident this guards.
func TestUpsertEpisodeRequestReusesLibraryItemWhenTvdbIDDrifts(t *testing.T) {
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

	const externalID = "drift-episode-777"
	const oldTvdbID, newTvdbID = 500001, 500002
	const tmdbID = 0

	libraryItemID, created, err := db.UpsertEpisodeRequest(ctx, externalID, oldTvdbID, tmdbID, "Drift Show", 2021, 1, 1, "Pilot")
	if err != nil {
		t.Fatalf("initial upsert: %v", err)
	}
	if !created {
		t.Fatal("expected the initial upsert to report created=true")
	}
	defer sqlDB.ExecContext(ctx, `delete from tv_shows where tvdb_id in ($1, $2)`, oldTvdbID, newTvdbID)
	defer sqlDB.ExecContext(ctx, `delete from media_requests where external_id = $1`, externalID)

	gotLibraryItemID, created, err := db.UpsertEpisodeRequest(ctx, externalID, newTvdbID, tmdbID, "Drift Show", 2021, 1, 1, "Pilot")
	if err != nil {
		t.Fatalf("drifted upsert: %v", err)
	}
	if created {
		t.Error("expected drifted upsert to report created=false -- the request was already tracked")
	}
	if gotLibraryItemID != libraryItemID {
		t.Errorf("expected the drifted upsert to reuse library_item_id %d, got %d", libraryItemID, gotLibraryItemID)
	}

	var libraryItemCount int
	if err := sqlDB.QueryRowContext(ctx, `
		select count(*) from library_items li
		join episodes e on e.id = li.episode_id
		join tv_shows ts on ts.id = e.tv_show_id
		where ts.tvdb_id in ($1, $2)`, oldTvdbID, newTvdbID).Scan(&libraryItemCount); err != nil {
		t.Fatal(err)
	}
	if libraryItemCount != 1 {
		t.Errorf("expected exactly 1 library_item across old/new tvdb_id, got %d (a duplicate was created)", libraryItemCount)
	}
}

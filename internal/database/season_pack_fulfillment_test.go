package database

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Regression coverage for the same defect class documented on
// FulfillEpisodeLibraryItem and createSeasonPackEpisodeItem: selected_releases
// has no unique constraint on library_item_id, so before both functions added
// an explicit "already fulfilled/linked" existence check, calling either of
// them twice for the same library item (e.g. RebuildPublications running
// again on process restart, or a season pack being re-selected/republished)
// silently created a second selected_releases row instead of no-op'ing --
// 94.5% of the table ended up as dead duplicates in production this way.

// TestFulfillEpisodeLibraryItemIsIdempotent calls FulfillEpisodeLibraryItem
// twice for the same target library item and asserts only one
// selected_releases row survives, the item is marked available, and its
// queue_item points at that single selected_release.
func TestFulfillEpisodeLibraryItemIsIdempotent(t *testing.T) {
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

	// Source: the season-pack triggering library item with its own selected
	// release (the pack's NZB/release_candidate).
	sourceLibID := setupRaceTestLibraryItem(t, ctx, sqlDB, "fulfill-idem-source", "available")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, sourceLibID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'Fulfill Idempotency Pack', 'http://example/fulfill-idem-pack', 'test-indexer')
		returning id`, sourceLibID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var sourceSelectedReleaseID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, sourceLibID, rcID).Scan(&sourceSelectedReleaseID); err != nil {
		t.Fatal(err)
	}

	var vfID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, reader_kind)
		values ($1, '/pack/Show.S01E01.mkv', 'Show.S01E01.mkv', 'archive')
		returning id`, sourceSelectedReleaseID).Scan(&vfID); err != nil {
		t.Fatal(err)
	}

	// Target: the episode library item being fulfilled by the pack. Starts
	// out unavailable/requested, same as a freshly-created episode item.
	targetLibID := setupRaceTestLibraryItem(t, ctx, sqlDB, "fulfill-idem-target", "requested")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, targetLibID)

	if err := db.FulfillEpisodeLibraryItem(ctx, targetLibID, sourceSelectedReleaseID, vfID); err != nil {
		t.Fatalf("first FulfillEpisodeLibraryItem call: %v", err)
	}
	if err := db.FulfillEpisodeLibraryItem(ctx, targetLibID, sourceSelectedReleaseID, vfID); err != nil {
		t.Fatalf("second FulfillEpisodeLibraryItem call: %v", err)
	}

	var count int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from selected_releases where library_item_id = $1`, targetLibID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 selected_releases row after calling FulfillEpisodeLibraryItem twice for the same library item, got %d", count)
	}

	var available bool
	if err := sqlDB.QueryRowContext(ctx, `select available from library_items where id = $1`, targetLibID).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if !available {
		t.Fatal("expected target library item to be marked available")
	}

	var selectedReleaseID int64
	if err := sqlDB.QueryRowContext(ctx, `select id from selected_releases where library_item_id = $1`, targetLibID).Scan(&selectedReleaseID); err != nil {
		t.Fatal(err)
	}

	var qState string
	var qSelectedReleaseID sql.NullInt64
	if err := sqlDB.QueryRowContext(ctx, `
		select state, selected_release_id from queue_items where library_item_id = $1`, targetLibID,
	).Scan(&qState, &qSelectedReleaseID); err != nil {
		t.Fatal(err)
	}
	if qState != "available" {
		t.Fatalf("expected queue_items.state = available, got %q", qState)
	}
	if !qSelectedReleaseID.Valid || qSelectedReleaseID.Int64 != selectedReleaseID {
		t.Fatalf("expected queue_items.selected_release_id = %d, got %v", selectedReleaseID, qSelectedReleaseID)
	}
}

// TestFulfillEpisodeLibraryItemNoopsWhenAlreadyFulfilledByDifferentRelease
// covers the case a plain "call twice with identical args" test can't reach:
// the existence check in FulfillEpisodeLibraryItem is keyed purely on
// library_item_id, so if the item was already fulfilled by some OTHER
// selected_release (e.g. an earlier pack selection), a later call for the
// same target with a *different* sourceSelectedReleaseID must still no-op
// rather than add a second row -- matching the documented "no-op if already
// fulfilled" contract regardless of which release triggered the second call.
func TestFulfillEpisodeLibraryItemNoopsWhenAlreadyFulfilledByDifferentRelease(t *testing.T) {
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

	libIDA := setupRaceTestLibraryItem(t, ctx, sqlDB, "fulfill-idem-source-a", "available")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libIDA)
	libIDB := setupRaceTestLibraryItem(t, ctx, sqlDB, "fulfill-idem-source-b", "available")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libIDB)

	var rcA, rcB int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'Fulfill Idempotency Pack A', 'http://example/fulfill-idem-pack-a', 'test-indexer')
		returning id`, libIDA).Scan(&rcA); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'Fulfill Idempotency Pack B', 'http://example/fulfill-idem-pack-b', 'test-indexer')
		returning id`, libIDB).Scan(&rcB); err != nil {
		t.Fatal(err)
	}
	var srcA, srcB int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libIDA, rcA).Scan(&srcA); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libIDB, rcB).Scan(&srcB); err != nil {
		t.Fatal(err)
	}

	targetLibID := setupRaceTestLibraryItem(t, ctx, sqlDB, "fulfill-idem-target-cross", "requested")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, targetLibID)

	if err := db.FulfillEpisodeLibraryItem(ctx, targetLibID, srcA, 0); err != nil {
		t.Fatalf("first FulfillEpisodeLibraryItem call (release A): %v", err)
	}
	if err := db.FulfillEpisodeLibraryItem(ctx, targetLibID, srcB, 0); err != nil {
		t.Fatalf("second FulfillEpisodeLibraryItem call (release B): %v", err)
	}

	var count int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from selected_releases where library_item_id = $1`, targetLibID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 selected_releases row when fulfilled twice by different source releases, got %d", count)
	}

	var survivingCandidateID int64
	if err := sqlDB.QueryRowContext(ctx, `select release_candidate_id from selected_releases where library_item_id = $1`, targetLibID).Scan(&survivingCandidateID); err != nil {
		t.Fatal(err)
	}
	if survivingCandidateID != rcA {
		t.Fatalf("expected the first fulfillment (release_candidate %d) to be the one that stuck, got %d", rcA, survivingCandidateID)
	}
}

// TestCreateSeasonPackEpisodeItemIsIdempotent calls the unexported
// createSeasonPackEpisodeItem twice for the same (tv_show, season, episode)
// and asserts exactly one episodes row, one library_items row, one
// selected_releases row, and one queue_items row result -- not two of any of
// them.
func TestCreateSeasonPackEpisodeItemIsIdempotent(t *testing.T) {
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

	const showTitle = "Season Pack Idempotency Show"
	var tvShowID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into tv_shows (title) values ($1) returning id`, showTitle,
	).Scan(&tvShowID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from tv_shows where id = $1`, tvShowID)

	// The whole-show library item that triggered the season pack search, and
	// the release_candidate representing the pack itself.
	triggerLibID := setupRaceTestLibraryItem(t, ctx, sqlDB, "season-pack-item-idem-trigger", "available")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, triggerLibID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'Season Pack Idempotency Release', 'http://example/season-pack-item-idem', 'test-indexer')
		returning id`, triggerLibID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}

	const season, episode = 1, 1
	if err := db.createSeasonPackEpisodeItem(ctx, tvShowID, showTitle, rcID, season, episode); err != nil {
		t.Fatalf("first createSeasonPackEpisodeItem call: %v", err)
	}
	if err := db.createSeasonPackEpisodeItem(ctx, tvShowID, showTitle, rcID, season, episode); err != nil {
		t.Fatalf("second createSeasonPackEpisodeItem call: %v", err)
	}

	var episodeCount int
	var episodeID int64
	if err := sqlDB.QueryRowContext(ctx, `
		select count(*), max(id) from episodes
		where tv_show_id = $1 and season_number = $2 and episode_number = $3`,
		tvShowID, season, episode,
	).Scan(&episodeCount, &episodeID); err != nil {
		t.Fatal(err)
	}
	if episodeCount != 1 {
		t.Fatalf("expected exactly 1 episodes row, got %d", episodeCount)
	}
	defer sqlDB.ExecContext(ctx, `delete from episodes where id = $1`, episodeID)

	var libItemCount int
	var libItemID int64
	if err := sqlDB.QueryRowContext(ctx, `
		select count(*), max(id) from library_items where episode_id = $1`, episodeID,
	).Scan(&libItemCount, &libItemID); err != nil {
		t.Fatal(err)
	}
	if libItemCount != 1 {
		t.Fatalf("expected exactly 1 library_items row for the episode, got %d", libItemCount)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libItemID)

	var selectedReleaseCount int
	if err := sqlDB.QueryRowContext(ctx, `
		select count(*) from selected_releases where library_item_id = $1`, libItemID,
	).Scan(&selectedReleaseCount); err != nil {
		t.Fatal(err)
	}
	if selectedReleaseCount != 1 {
		t.Fatalf("expected exactly 1 selected_releases row after calling createSeasonPackEpisodeItem twice for the same episode, got %d", selectedReleaseCount)
	}

	var queueItemCount int
	if err := sqlDB.QueryRowContext(ctx, `
		select count(*) from queue_items where library_item_id = $1`, libItemID,
	).Scan(&queueItemCount); err != nil {
		t.Fatal(err)
	}
	if queueItemCount != 1 {
		t.Fatalf("expected exactly 1 queue_items row, got %d", queueItemCount)
	}

	var libItemAvailable bool
	if err := sqlDB.QueryRowContext(ctx, `select available from library_items where id = $1`, libItemID).Scan(&libItemAvailable); err != nil {
		t.Fatal(err)
	}
	if !libItemAvailable {
		t.Fatal("expected the episode library item to be marked available")
	}
}

// TestCreateSeasonPackEpisodeItemsIsIdempotent exercises the exported,
// higher-level CreateSeasonPackEpisodeItems entry point (the one actually
// wired into production, iterating every virtual file in a season pack's
// release) instead of the unexported per-episode helper directly, calling it
// twice for the same release and asserting the episode it discovers still
// ends up with exactly one selected_releases row.
func TestCreateSeasonPackEpisodeItemsIsIdempotent(t *testing.T) {
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

	const showTitle = "Season Pack Items Idempotency Show"
	var tvShowID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into tv_shows (title) values ($1) returning id`, showTitle,
	).Scan(&tvShowID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from tv_shows where id = $1`, tvShowID)

	// Whole-show trigger item (season=0/episode=0 semantics -- modeled here
	// simply as a library item whose title resolves to the tv_show by name,
	// matching resolveSeasonPackShow's fallback lookup).
	triggerLibID := setupRaceTestLibraryItem(t, ctx, sqlDB, showTitle, "available")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, triggerLibID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'Season Pack Items Idempotency Release', 'http://example/season-pack-items-idem', 'test-indexer')
		returning id`, triggerLibID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var selectedReleaseID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, triggerLibID, rcID,
	).Scan(&selectedReleaseID); err != nil {
		t.Fatal(err)
	}

	if _, err := sqlDB.ExecContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, reader_kind)
		values ($1, '/pack/Show.S02E05.mkv', 'Show.S02E05.mkv', 'archive')`, selectedReleaseID,
	); err != nil {
		t.Fatal(err)
	}

	if err := db.CreateSeasonPackEpisodeItems(ctx, selectedReleaseID, triggerLibID); err != nil {
		t.Fatalf("first CreateSeasonPackEpisodeItems call: %v", err)
	}
	if err := db.CreateSeasonPackEpisodeItems(ctx, selectedReleaseID, triggerLibID); err != nil {
		t.Fatalf("second CreateSeasonPackEpisodeItems call: %v", err)
	}

	var episodeID int64
	if err := sqlDB.QueryRowContext(ctx, `
		select id from episodes where tv_show_id = $1 and season_number = 2 and episode_number = 5`, tvShowID,
	).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from episodes where id = $1`, episodeID)

	var libItemID int64
	if err := sqlDB.QueryRowContext(ctx, `select id from library_items where episode_id = $1`, episodeID).Scan(&libItemID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libItemID)

	var selectedReleaseCount int
	if err := sqlDB.QueryRowContext(ctx, `
		select count(*) from selected_releases where library_item_id = $1`, libItemID,
	).Scan(&selectedReleaseCount); err != nil {
		t.Fatal(err)
	}
	if selectedReleaseCount != 1 {
		t.Fatalf("expected exactly 1 selected_releases row after CreateSeasonPackEpisodeItems ran twice over the same release, got %d", selectedReleaseCount)
	}
}

// TestCreateSeasonPackEpisodeItemsSkipsImplausibleSeasonNumber guards a real
// production incident (2026-07-21): a search-ranking gap let an unrelated
// same-titled release ("One Piece (1999) S19E86") attach to the 2023
// live-action "ONE PIECE" whole-show library item (number_of_seasons=3).
// CreateSeasonPackEpisodeItems then blindly manufactured a blank "season 19"
// episode with no metadata to hold it, publishing it straight into the real
// show's library/Plex entry. As defense in depth against future
// upstream-matching bugs, a parsed season number far beyond the show's own
// known season count must not spawn a placeholder episode at all.
func TestCreateSeasonPackEpisodeItemsSkipsImplausibleSeasonNumber(t *testing.T) {
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

	const showTitle = "Season Pack Implausible Season Show"
	var tvShowID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into tv_shows (title, number_of_seasons) values ($1, 3) returning id`, showTitle,
	).Scan(&tvShowID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from tv_shows where id = $1`, tvShowID)

	triggerLibID := setupRaceTestLibraryItem(t, ctx, sqlDB, showTitle, "available")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, triggerLibID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'Season Pack Implausible Season Release', 'http://example/season-pack-implausible', 'test-indexer')
		returning id`, triggerLibID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var selectedReleaseID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, triggerLibID, rcID,
	).Scan(&selectedReleaseID); err != nil {
		t.Fatal(err)
	}

	if _, err := sqlDB.ExecContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, reader_kind)
		values ($1, '/pack/Unrelated.S19E86.mkv', 'Unrelated.S19E86.mkv', 'archive')`, selectedReleaseID,
	); err != nil {
		t.Fatal(err)
	}

	if err := db.CreateSeasonPackEpisodeItems(ctx, selectedReleaseID, triggerLibID); err != nil {
		t.Fatalf("CreateSeasonPackEpisodeItems: %v", err)
	}

	var count int
	if err := sqlDB.QueryRowContext(ctx, `
		select count(*) from episodes where tv_show_id = $1 and season_number = 19`, tvShowID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected no episode row created for implausible season 19 (show has 3 seasons), got %d", count)
	}
}

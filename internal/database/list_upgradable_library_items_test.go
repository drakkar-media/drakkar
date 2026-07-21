package database

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// setupUpgradableLibraryItem inserts a library item with a fully working
// selection at the given resolution, using a quality profile with
// allow_upgrade=true and the given cutoff_resolution ("" disables the
// cutoff). Returns the library item id.
func setupUpgradableLibraryItem(t *testing.T, ctx context.Context, sqlDB *sql.DB, title, cutoffResolution, currentResolution string) int64 {
	t.Helper()
	var profileID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into quality_profiles (name, allow_upgrade, cutoff_resolution)
		values ($1, true, $2)
		returning id`, title+"-profile", cutoffResolution).Scan(&profileID); err != nil {
		t.Fatal(err)
	}
	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available, quality_profile_id)
		values ('movie', $1, true, $2)
		returning id`, title, profileID).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, score, selected, resolution)
		values ($1, $2, 500, true, $3)
		returning id`, libID, title, currentResolution).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into queue_items (library_item_id, state, idempotency_key, selected_release_id)
		values ($1, 'available', $2, $3)`, libID, title, srID,
	); err != nil {
		t.Fatal(err)
	}
	return libID
}

// TestListUpgradableLibraryItemsExcludesItemsAtOrAboveCutoff guards a real
// gap found live (2026-07-20): the user configured cutoff_resolution=1080p
// on their profile, expecting -- per the field's own documented meaning --
// that an item already at 1080p or better would stop being swept into
// upgrade searches. The query never checked cutoff_resolution at all, so
// EVERY available item under an allow_upgrade profile was searched on every
// pass regardless of its current resolution.
func TestListUpgradableLibraryItemsExcludesItemsAtOrAboveCutoff(t *testing.T) {
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

	atCutoff := setupUpgradableLibraryItem(t, ctx, sqlDB, "cutoff-at-1080p", "1080p", "1080p")
	aboveCutoff := setupUpgradableLibraryItem(t, ctx, sqlDB, "cutoff-above-2160p", "1080p", "2160p")
	belowCutoff := setupUpgradableLibraryItem(t, ctx, sqlDB, "cutoff-below-720p", "1080p", "720p")
	noCutoffConfigured := setupUpgradableLibraryItem(t, ctx, sqlDB, "cutoff-disabled", "", "2160p")
	undetectedResolution := setupUpgradableLibraryItem(t, ctx, sqlDB, "cutoff-undetected", "1080p", "")
	defer func() {
		for _, id := range []int64{atCutoff, aboveCutoff, belowCutoff, noCutoffConfigured, undetectedResolution} {
			sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, id)
		}
		sqlDB.ExecContext(ctx, `delete from quality_profiles where name like 'cutoff-%-profile'`)
	}()

	ids, err := db.ListUpgradableLibraryItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[int64]bool, len(ids))
	for _, id := range ids {
		got[id] = true
	}

	if got[atCutoff] {
		t.Errorf("expected item at exactly cutoff resolution (1080p) to be excluded, got included")
	}
	if got[aboveCutoff] {
		t.Errorf("expected item above cutoff resolution (2160p vs cutoff 1080p) to be excluded, got included")
	}
	if !got[belowCutoff] {
		t.Errorf("expected item below cutoff resolution (720p vs cutoff 1080p) to still be eligible for upgrade search")
	}
	if !got[noCutoffConfigured] {
		t.Errorf("expected an item whose profile has no cutoff_resolution configured to remain eligible regardless of its current resolution")
	}
	if !got[undetectedResolution] {
		t.Errorf("expected an item with an undetected current resolution to remain eligible (can't confirm it has met a cutoff it can't detect)")
	}
}

// TestListUpgradableLibraryItemsOrdersLeastRecentlySearchedFirst guards a
// real production incident (2026-07-21): with no cap or ordering, a single
// SearchUpgrades pass walked every eligible item (thousands, once available)
// through NZBHydra2 sequentially -- one search every ~2s (the client's global
// throttle) -- so a single run could take hours of continuous back-to-back
// searching, hammering the indexer non-stop. Capping the batch only helps if
// each run makes rotating progress across the whole set (oldest/
// never-searched first) instead of reprocessing the same items every cycle.
func TestListUpgradableLibraryItemsOrdersLeastRecentlySearchedFirst(t *testing.T) {
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

	neverSearched := setupUpgradableLibraryItem(t, ctx, sqlDB, "rotation-never-searched", "1080p", "720p")
	searchedLongAgo := setupUpgradableLibraryItem(t, ctx, sqlDB, "rotation-searched-long-ago", "1080p", "720p")
	searchedRecently := setupUpgradableLibraryItem(t, ctx, sqlDB, "rotation-searched-recently", "1080p", "720p")
	defer func() {
		for _, id := range []int64{neverSearched, searchedLongAgo, searchedRecently} {
			sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, id)
		}
		sqlDB.ExecContext(ctx, `delete from quality_profiles where name like 'rotation-%-profile'`)
	}()

	if _, err := sqlDB.ExecContext(ctx, `
		update queue_items set last_searched_at = now() - interval '30 days' where library_item_id = $1`, searchedLongAgo,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		update queue_items set last_searched_at = now() - interval '1 minute' where library_item_id = $1`, searchedRecently,
	); err != nil {
		t.Fatal(err)
	}

	ids, err := db.ListUpgradableLibraryItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pos := make(map[int64]int, len(ids))
	for i, id := range ids {
		pos[id] = i
	}
	if _, ok := pos[neverSearched]; !ok {
		t.Fatal("expected never-searched item to be present")
	}
	if _, ok := pos[searchedLongAgo]; !ok {
		t.Fatal("expected long-ago-searched item to be present")
	}
	if _, ok := pos[searchedRecently]; !ok {
		t.Fatal("expected recently-searched item to be present")
	}
	if pos[neverSearched] > pos[searchedRecently] {
		t.Errorf("expected never-searched item (nulls first) to sort before recently-searched item; positions: never=%d recent=%d", pos[neverSearched], pos[searchedRecently])
	}
	if pos[searchedLongAgo] > pos[searchedRecently] {
		t.Errorf("expected long-ago-searched item to sort before recently-searched item; positions: long_ago=%d recent=%d", pos[searchedLongAgo], pos[searchedRecently])
	}
}

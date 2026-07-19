package database

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// setupWorkingLibraryItem inserts a library item that already has a fully
// working selection: release_candidates -> selected_releases -> virtual_files
// -> symlink_publications, mirroring a real published, playable item. Returns
// the library item id and the ids of the rows an upgrade search must not
// destroy unless it actually replaces them.
func setupWorkingLibraryItem(t *testing.T, ctx context.Context, sqlDB *sql.DB, title string) (libID, selectedReleaseID, releaseCandidateID, virtualFileID int64) {
	t.Helper()
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('movie', $1, true)
		returning id`, title).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into queue_items (library_item_id, state, idempotency_key)
		values ($1, 'available', $2)`, libID, title,
	); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, score, selected)
		values ($1, 'Existing Working Release', 500, true)
		returning id`, libID).Scan(&releaseCandidateID); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, libID, releaseCandidateID).Scan(&selectedReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		update queue_items set selected_release_id = $2 where library_item_id = $1`,
		libID, selectedReleaseID,
	); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, size_bytes, reader_kind)
		values ($1, $2, 'movie.mkv', 1000, 'direct_nzb')
		returning id`, selectedReleaseID, "releases/"+title+"/movie.mkv").Scan(&virtualFileID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		insert into symlink_publications (library_item_id, virtual_file_id, library_path, target_path)
		values ($1, $2, $3, 'target')`,
		libID, virtualFileID, "/media/"+title+".mkv",
	); err != nil {
		t.Fatal(err)
	}
	return libID, selectedReleaseID, releaseCandidateID, virtualFileID
}

// TestReplaceSearchCandidatesUpgradeSearchPreservesWorkingReleaseWhenNothingBetter
// guards the 2026-07-19 production fix: an upgrade search that finds nothing
// genuinely better than the current release must leave the existing
// selected_releases/virtual_files/symlink_publications rows -- the actual
// playable content -- completely untouched, rather than deleting them
// up front (which previously happened unconditionally, before a
// replacement was ever confirmed, breaking playback of perfectly good
// content on every upgrade pass that didn't find something strictly better).
func TestReplaceSearchCandidatesUpgradeSearchPreservesWorkingReleaseWhenNothingBetter(t *testing.T) {
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

	libID, wantSelectedReleaseID, wantReleaseCandidateID, wantVirtualFileID := setupWorkingLibraryItem(t, ctx, sqlDB, "upgrade-nothing-better")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	// Simulate the caller (applyUpgradeMinimums) having already rejected every
	// fresh candidate as not a genuine improvement.
	candidates := []SearchCandidateRecord{
		{Title: "Lateral Release", Score: 500, Rejected: true, RejectReason: "not_an_upgrade"},
		{Title: "Worse Release", Score: 300, Rejected: true, RejectReason: "not_an_upgrade"},
	}
	selectedReleaseID, err := db.ReplaceSearchCandidates(ctx, libID, candidates, true)
	if err != nil {
		t.Fatal(err)
	}
	if selectedReleaseID != nil {
		t.Fatalf("expected no new selection when nothing qualifies as an upgrade, got %d", *selectedReleaseID)
	}

	var survivingSelectedReleaseID, survivingReleaseCandidateID int64
	if err := sqlDB.QueryRowContext(ctx, `select id, release_candidate_id from selected_releases where library_item_id = $1`, libID).
		Scan(&survivingSelectedReleaseID, &survivingReleaseCandidateID); err != nil {
		t.Fatalf("expected the existing selected_releases row to survive untouched: %v", err)
	}
	if survivingSelectedReleaseID != wantSelectedReleaseID || survivingReleaseCandidateID != wantReleaseCandidateID {
		t.Fatalf("expected selected_releases row %d (candidate %d) to survive unchanged, got %d (candidate %d)",
			wantSelectedReleaseID, wantReleaseCandidateID, survivingSelectedReleaseID, survivingReleaseCandidateID)
	}

	var survivingVirtualFileID int64
	if err := sqlDB.QueryRowContext(ctx, `select id from virtual_files where selected_release_id = $1`, wantSelectedReleaseID).
		Scan(&survivingVirtualFileID); err != nil {
		t.Fatalf("expected the existing virtual_files row (the actual playable content) to survive: %v", err)
	}
	if survivingVirtualFileID != wantVirtualFileID {
		t.Fatalf("expected virtual_files row %d to survive, got %d", wantVirtualFileID, survivingVirtualFileID)
	}

	var symlinkCount int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from symlink_publications where virtual_file_id = $1`, wantVirtualFileID).Scan(&symlinkCount); err != nil {
		t.Fatal(err)
	}
	if symlinkCount != 1 {
		t.Fatalf("expected the published symlink to survive untouched, found %d rows", symlinkCount)
	}

	var state string
	var qSelectedReleaseID sql.NullInt64
	if err := sqlDB.QueryRowContext(ctx, `select state, selected_release_id from queue_items where library_item_id = $1`, libID).
		Scan(&state, &qSelectedReleaseID); err != nil {
		t.Fatal(err)
	}
	if state != string(QueueAvailable) {
		t.Fatalf("expected queue_items to be restored to 'available' (not marked failed), got %q", state)
	}
	if !qSelectedReleaseID.Valid || qSelectedReleaseID.Int64 != wantSelectedReleaseID {
		t.Fatalf("expected queue_items.selected_release_id to still point at %d, got %+v", wantSelectedReleaseID, qSelectedReleaseID)
	}
}

// TestReplaceSearchCandidatesUpgradeSearchReplacesWhenGenuinelyBetterFound is
// the other half: when a real replacement candidate IS selected, the old
// release must actually be superseded (old selected_releases/virtual_files
// gone, new selected_releases row in place) -- the fix must not leave stale
// duplicate rows around.
func TestReplaceSearchCandidatesUpgradeSearchReplacesWhenGenuinelyBetterFound(t *testing.T) {
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

	libID, oldSelectedReleaseID, oldReleaseCandidateID, oldVirtualFileID := setupWorkingLibraryItem(t, ctx, sqlDB, "upgrade-found-better")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	candidates := []SearchCandidateRecord{
		{Title: "Genuinely Better Release", Score: 900, Rejected: false},
	}
	selectedReleaseID, err := db.ReplaceSearchCandidates(ctx, libID, candidates, true)
	if err != nil {
		t.Fatal(err)
	}
	if selectedReleaseID == nil {
		t.Fatal("expected a new selection when a genuinely better candidate is found")
	}
	if *selectedReleaseID == oldSelectedReleaseID {
		t.Fatalf("expected a NEW selected_releases id, got the old one (%d)", oldSelectedReleaseID)
	}

	var oldStillExists bool
	if err := sqlDB.QueryRowContext(ctx, `select exists(select 1 from selected_releases where id = $1)`, oldSelectedReleaseID).Scan(&oldStillExists); err != nil {
		t.Fatal(err)
	}
	if oldStillExists {
		t.Fatalf("expected the old selected_releases row %d to be gone once replaced", oldSelectedReleaseID)
	}
	var oldCandidateStillExists bool
	if err := sqlDB.QueryRowContext(ctx, `select exists(select 1 from release_candidates where id = $1)`, oldReleaseCandidateID).Scan(&oldCandidateStillExists); err != nil {
		t.Fatal(err)
	}
	if oldCandidateStillExists {
		t.Fatalf("expected the old release_candidates row %d to be gone once replaced", oldReleaseCandidateID)
	}
	var oldVFStillExists bool
	if err := sqlDB.QueryRowContext(ctx, `select exists(select 1 from virtual_files where id = $1)`, oldVirtualFileID).Scan(&oldVFStillExists); err != nil {
		t.Fatal(err)
	}
	if oldVFStillExists {
		t.Fatalf("expected the old virtual_files row %d to cascade-delete once replaced", oldVirtualFileID)
	}

	var state string
	if err := sqlDB.QueryRowContext(ctx, `select state from queue_items where library_item_id = $1`, libID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(QueueSelected) {
		t.Fatalf("expected queue_items state 'selected' after a genuine replacement, got %q", state)
	}
}

// TestReplaceSearchCandidatesNonUpgradeSearchStillReplacesUnconditionally is a
// regression guard: a normal (non-upgrade) search -- e.g. the initial search
// for a newly-added item, or a backlog/failed-item retry -- must keep its
// existing unconditional delete-then-replace behavior. There is no "working
// content" to protect there, and this path must not accidentally start
// preserving stale candidates.
func TestReplaceSearchCandidatesNonUpgradeSearchStillReplacesUnconditionally(t *testing.T) {
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

	libID, _, _, _ := setupWorkingLibraryItem(t, ctx, sqlDB, "non-upgrade-replace")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	// Even a WORSE-scored candidate must win here, because upgradeSearch=false
	// means "not an upgrade" gating never applied in the first place.
	candidates := []SearchCandidateRecord{
		{Title: "Fresh Candidate", Score: 1, Rejected: false},
	}
	selectedReleaseID, err := db.ReplaceSearchCandidates(ctx, libID, candidates, false)
	if err != nil {
		t.Fatal(err)
	}
	if selectedReleaseID == nil {
		t.Fatal("expected a selection for a non-upgrade search with a non-rejected candidate")
	}

	var count int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from selected_releases where library_item_id = $1`, libID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 selected_releases row, got %d", count)
	}
}

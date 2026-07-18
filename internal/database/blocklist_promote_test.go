package database

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Regression tests for a gap found in the 2026-07-18 exhaustive audit:
// FailSelectedReleaseAndPromoteNext was the only "promote next candidate"
// function that skipped a candidate matching a live blocklist_items entry
// while scanning for a replacement. RejectReleaseCandidate,
// promoteRetryCandidate (backing PromoteBestRetryCandidate /
// PromoteAlternativeRetryCandidate), and PromoteExistingCandidate all
// filtered only on release_candidates.rejected = false -- which a sibling
// candidate (the same release re-posted under a different indexer/URL)
// never gets flipped on, since rejecting/blocklisting one candidate doesn't
// retroactively touch its siblings. That let an already-permanently
// blocklisted release be re-selected and re-fetched through any of those
// three paths.

// blocklistPromoteTestFixture creates a library item with two release
// candidates and blocklists rcPriority under its release_family key --
// simulating the identical content having already been rejected once under
// a different indexer/URL (the family key deliberately excludes indexer, so
// it matches a same-content repost regardless of which indexer served it).
// rcPriority is inserted first (and so wins any ORDER BY tiebreak on
// created_at/id) but must be skipped as blocklisted; rcFallback -- an
// unrelated candidate that doesn't match any blocklist entry -- is the one
// that should end up promoted instead.
func blocklistPromoteTestFixture(t *testing.T, ctx context.Context, sqlDB *sql.DB, namePrefix string) (libID, rcPriority, rcFallback int64) {
	t.Helper()
	libID = setupRaceTestLibraryItem(t, ctx, sqlDB, namePrefix, "selected")

	const title = "Blocklisted Family Release 2024"
	sizeBytes := int64(2_000_000_000)
	postedAt := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name, size_bytes, posted_at)
		values ($1, $2, $3, 'indexer-a', $4, $5)
		returning id`, libID, title, namePrefix+"-priority-url", sizeBytes, postedAt,
	).Scan(&rcPriority); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name, size_bytes, posted_at)
		values ($1, 'Unrelated Fallback Release 2024', $2, 'indexer-b', $3, $4)
		returning id`, libID, namePrefix+"-fallback-url", sizeBytes, postedAt,
	).Scan(&rcFallback); err != nil {
		t.Fatal(err)
	}

	scopeKey, err := resolveMediaScopeKey(ctx, sqlDB, libID)
	if err != nil {
		t.Fatal(err)
	}
	familyKey := scopeKey + "|" + blocklistReleaseFamilyKey(title, sizeBytes, postedAt)
	if _, err := sqlDB.ExecContext(ctx, `
		insert into blocklist_items (key, reason)
		values ($1, 'test_family_blocklisted')`, familyKey,
	); err != nil {
		t.Fatal(err)
	}

	return libID, rcPriority, rcFallback
}

func TestPromoteExistingCandidateSkipsBlocklistedSibling(t *testing.T) {
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

	libID, rcPriority, rcFallback := blocklistPromoteTestFixture(t, ctx, sqlDB, "promote-existing-blocklist")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	selectedReleaseID, err := db.PromoteExistingCandidate(ctx, libID)
	if err != nil {
		t.Fatal(err)
	}
	if selectedReleaseID == nil {
		t.Fatal("expected a candidate to be promoted (the non-blocklisted fallback), got none")
	}

	var promotedCandidateID int64
	if err := sqlDB.QueryRowContext(ctx, `select release_candidate_id from selected_releases where id = $1`, *selectedReleaseID).Scan(&promotedCandidateID); err != nil {
		t.Fatal(err)
	}
	if promotedCandidateID != rcFallback {
		t.Fatalf("expected the blocklisted candidate %d to be skipped in favor of %d, but %d was promoted", rcPriority, rcFallback, promotedCandidateID)
	}

	var priorityRejected bool
	if err := sqlDB.QueryRowContext(ctx, `select rejected from release_candidates where id = $1`, rcPriority).Scan(&priorityRejected); err != nil {
		t.Fatal(err)
	}
	if !priorityRejected {
		t.Fatal("expected the blocklisted candidate encountered during the scan to be marked rejected")
	}
}

func TestPromoteBestRetryCandidateSkipsBlocklistedSibling(t *testing.T) {
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

	libID, rcPriority, rcFallback := blocklistPromoteTestFixture(t, ctx, sqlDB, "promote-retry-blocklist")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	summary, err := db.PromoteBestRetryCandidate(ctx, libID)
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil {
		t.Fatal("expected a candidate to be promoted (the non-blocklisted fallback), got none")
	}
	if summary.ReleaseCandidateID != rcFallback {
		t.Fatalf("expected the blocklisted candidate %d to be skipped in favor of %d, but %d was promoted", rcPriority, rcFallback, summary.ReleaseCandidateID)
	}

	var priorityRejected bool
	if err := sqlDB.QueryRowContext(ctx, `select rejected from release_candidates where id = $1`, rcPriority).Scan(&priorityRejected); err != nil {
		t.Fatal(err)
	}
	if !priorityRejected {
		t.Fatal("expected the blocklisted candidate encountered during the scan to be marked rejected")
	}
}

func TestRejectReleaseCandidatePromotesAroundBlocklistedSibling(t *testing.T) {
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

	libID, rcPriority, rcFallback := blocklistPromoteTestFixture(t, ctx, sqlDB, "reject-around-blocklist")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	// A third, currently-selected candidate is the one actually being
	// rejected -- RejectReleaseCandidate then has to pick a replacement from
	// rcPriority/rcFallback, and must skip the blocklisted one.
	var rcCurrent int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name, selected)
		values ($1, 'Currently Selected Release', $2, 'test-indexer', true)
		returning id`, libID, "reject-around-blocklist-current-url",
	).Scan(&rcCurrent); err != nil {
		t.Fatal(err)
	}
	var selectedReleaseID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, libID, rcCurrent,
	).Scan(&selectedReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `update queue_items set selected_release_id = $2 where library_item_id = $1`, libID, selectedReleaseID); err != nil {
		t.Fatal(err)
	}

	summary, err := db.RejectReleaseCandidate(ctx, rcCurrent, "test_manual_reject")
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil {
		t.Fatal("expected a replacement candidate to be promoted (the non-blocklisted fallback), got none")
	}
	if summary.ReleaseCandidateID != rcFallback {
		t.Fatalf("expected the blocklisted candidate %d to be skipped in favor of %d, but %d was promoted", rcPriority, rcFallback, summary.ReleaseCandidateID)
	}

	var priorityRejected bool
	if err := sqlDB.QueryRowContext(ctx, `select rejected from release_candidates where id = $1`, rcPriority).Scan(&priorityRejected); err != nil {
		t.Fatal(err)
	}
	if !priorityRejected {
		t.Fatal("expected the blocklisted candidate encountered during the scan to be marked rejected")
	}
}

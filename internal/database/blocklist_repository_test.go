package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// openBlocklistTestDB opens a real Postgres connection for the blocklist
// admin CRUD tests, skipping when DRAKKAR_TEST_DATABASE_URL isn't set,
// following the same convention as workflow_repository_race_test.go.
func openBlocklistTestDB(t *testing.T) (*DB, *sql.DB, context.Context) {
	t.Helper()
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return &DB{SQL: sqlDB}, sqlDB, context.Background()
}

func TestCreateBlocklistItemDefaultsReasonWhenBlank(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	created, err := db.CreateBlocklistItem(ctx, BlocklistMutation{
		Key:    "external_url:http://example/blank-reason-test.nzb",
		Reason: "   ",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, created.ID)

	if created.Reason != "manual" {
		t.Errorf("Reason = %q, want manual", created.Reason)
	}
	if created.KeyType != "external_url" {
		t.Errorf("KeyType = %q, want external_url", created.KeyType)
	}
}

func TestCreateUpdateDeleteBlocklistItemRoundTrip(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	expiresAt := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	created, err := db.CreateBlocklistItem(ctx, BlocklistMutation{
		Key:       "release_signature:some movie 2024|indexer-a|1500|2024-05-01",
		Reason:    "test_manual_create",
		ExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, created.ID)

	if created.ID == 0 {
		t.Fatal("expected a non-zero ID")
	}
	if created.KeyType != "release_signature" {
		t.Errorf("KeyType = %q, want release_signature", created.KeyType)
	}
	if created.ExpiresAt == nil || !created.ExpiresAt.Equal(expiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", created.ExpiresAt, expiresAt)
	}

	newExpiresAt := time.Now().Add(96 * time.Hour).UTC().Truncate(time.Second)
	updated, err := db.UpdateBlocklistItem(ctx, created.ID, BlocklistMutation{
		Key:       "external_url:http://example/updated-key.nzb",
		Reason:    "test_manual_update",
		ExpiresAt: &newExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Key != "external_url:http://example/updated-key.nzb" {
		t.Errorf("Key = %q, want the updated key", updated.Key)
	}
	if updated.Reason != "test_manual_update" {
		t.Errorf("Reason = %q, want test_manual_update", updated.Reason)
	}
	if updated.KeyType != "external_url" {
		t.Errorf("KeyType = %q, want external_url after key type changed", updated.KeyType)
	}
	if updated.ExpiresAt == nil || !updated.ExpiresAt.Equal(newExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", updated.ExpiresAt, newExpiresAt)
	}

	if err := db.DeleteBlocklistItem(ctx, created.ID); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from blocklist_items where id = $1`, created.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected the item to be gone after delete, found %d rows", count)
	}

	if err := db.DeleteBlocklistItem(ctx, created.ID); err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows deleting an already-deleted item, got %v", err)
	}
}

func TestUpdateBlocklistItemUnknownIDReturnsErrNoRows(t *testing.T) {
	db, _, ctx := openBlocklistTestDB(t)

	_, err := db.UpdateBlocklistItem(ctx, 987654321, BlocklistMutation{Key: "external_url:http://example/nope", Reason: "x"})
	if err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows for a nonexistent id, got %v", err)
	}
}

func TestEnrichBlocklistExternalURLPopulatesMetadataFromMatchingCandidate(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	libID := setupRaceTestLibraryItem(t, ctx, sqlDB, "blocklist-enrich-external-url", "selected")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	postedAt := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	const externalURL = "http://example/enrich-external-url-test.nzb"
	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name, size_bytes, posted_at)
		values ($1, 'Enrich External URL Release', $2, 'indexer-enrich', 3000000000, $3)
		returning id`, libID, externalURL, postedAt,
	).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var selectedReleaseID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libID, rcID,
	).Scan(&selectedReleaseID); err != nil {
		t.Fatal(err)
	}

	created, err := db.CreateBlocklistItem(ctx, BlocklistMutation{
		Key:    blocklistKeyForExternalURL(externalURL),
		Reason: "test_enrich_external_url",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, created.ID)

	if created.ReleaseTitle != "Enrich External URL Release" {
		t.Errorf("ReleaseTitle = %q, want Enrich External URL Release", created.ReleaseTitle)
	}
	if created.IndexerName != "indexer-enrich" {
		t.Errorf("IndexerName = %q, want indexer-enrich", created.IndexerName)
	}
	if created.SizeBytes != 3000000000 {
		t.Errorf("SizeBytes = %d, want 3000000000", created.SizeBytes)
	}
	if created.SelectedReleaseID == nil || *created.SelectedReleaseID != selectedReleaseID {
		t.Errorf("SelectedReleaseID = %v, want %d", created.SelectedReleaseID, selectedReleaseID)
	}
	if created.LibraryItemID == nil || *created.LibraryItemID != libID {
		t.Errorf("LibraryItemID = %v, want %d", created.LibraryItemID, libID)
	}
}

func TestEnrichBlocklistSignaturePopulatesMetadataFromMatchingCandidate(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	libID := setupRaceTestLibraryItem(t, ctx, sqlDB, "blocklist-enrich-signature", "selected")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	const title = "Signature Enrichment Release 2024"
	postedAt := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	sizeBytes := int64(2_000_000_000)
	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name, size_bytes, posted_at)
		values ($1, $2, 'http://example/enrich-signature-test.nzb', 'indexersig', $3, $4)
		returning id`, libID, title, sizeBytes, postedAt,
	).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var selectedReleaseID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libID, rcID,
	).Scan(&selectedReleaseID); err != nil {
		t.Fatal(err)
	}

	key := blocklistReleaseSignatureKey(title, "indexersig", sizeBytes, postedAt)
	created, err := db.CreateBlocklistItem(ctx, BlocklistMutation{Key: key, Reason: "test_enrich_signature"})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, created.ID)

	if created.ReleaseTitle != title {
		t.Errorf("ReleaseTitle = %q, want %q", created.ReleaseTitle, title)
	}
	if created.SelectedReleaseID == nil || *created.SelectedReleaseID != selectedReleaseID {
		t.Errorf("SelectedReleaseID = %v, want %d", created.SelectedReleaseID, selectedReleaseID)
	}
}

func TestListBlocklistItemsPagedExcludesExpiredAndFiltersByReason(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	const reasonA = "test_paged_reason_a"
	const reasonB = "test_paged_reason_b"

	active, err := db.CreateBlocklistItem(ctx, BlocklistMutation{Key: "external_url:http://example/paged-active.nzb", Reason: reasonA})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, active.ID)

	otherReason, err := db.CreateBlocklistItem(ctx, BlocklistMutation{Key: "external_url:http://example/paged-other.nzb", Reason: reasonB})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, otherReason.ID)

	pastExpiry := time.Now().Add(-1 * time.Hour)
	expired, err := db.CreateBlocklistItem(ctx, BlocklistMutation{Key: "external_url:http://example/paged-expired.nzb", Reason: reasonA, ExpiresAt: &pastExpiry})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, expired.ID)

	page, err := db.ListBlocklistItemsPaged(ctx, BlocklistFilter{Reason: reasonA, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("expected exactly 1 active item for reasonA (expired one excluded), got %d (items=%+v)", page.Total, page.Items)
	}
	if page.Items[0].ID != active.ID {
		t.Errorf("expected the active item %d, got %d", active.ID, page.Items[0].ID)
	}

	pageAll, err := db.ListBlocklistItemsPaged(ctx, BlocklistFilter{Q: "paged-other", PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if pageAll.Total != 1 || pageAll.Items[0].ID != otherReason.ID {
		t.Fatalf("expected Q filter to match only the paged-other item, got total=%d items=%+v", pageAll.Total, pageAll.Items)
	}
}

func TestListBlocklistItemsPagedPagination(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	const reason = "test_pagination_reason"
	var ids []int64
	for i := 0; i < 5; i++ {
		created, err := db.CreateBlocklistItem(ctx, BlocklistMutation{
			Key:    fmt.Sprintf("external_url:http://example/pagination-%d-%d.nzb", time.Now().UnixNano(), i),
			Reason: reason,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, created.ID)
	}
	defer func() {
		for _, id := range ids {
			sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, id)
		}
	}()

	page1, err := db.ListBlocklistItemsPaged(ctx, BlocklistFilter{Reason: reason, Page: 1, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page1.Total != 5 {
		t.Fatalf("Total = %d, want 5", page1.Total)
	}
	if page1.TotalPages != 3 {
		t.Fatalf("TotalPages = %d, want 3", page1.TotalPages)
	}
	if len(page1.Items) != 2 {
		t.Fatalf("expected 2 items on page 1, got %d", len(page1.Items))
	}

	page3, err := db.ListBlocklistItemsPaged(ctx, BlocklistFilter{Reason: reason, Page: 3, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page3.Items) != 1 {
		t.Fatalf("expected 1 item (the remainder) on page 3, got %d", len(page3.Items))
	}

	// An invalid page/pageSize must clamp to sane defaults rather than error.
	pageInvalid, err := db.ListBlocklistItemsPaged(ctx, BlocklistFilter{Reason: reason, Page: 0, PageSize: -5})
	if err != nil {
		t.Fatal(err)
	}
	if pageInvalid.Page != 1 {
		t.Errorf("Page = %d, want clamped to 1", pageInvalid.Page)
	}
	if pageInvalid.PageSize != 50 {
		t.Errorf("PageSize = %d, want clamped default of 50", pageInvalid.PageSize)
	}
}

// TestListBlocklistItemsPagedPreflightReasonFuzzyMatch guards the
// segment-ID-stripping LIKE logic in ListBlocklistItemsPaged: a real
// "preflight: ..." reason stored in the DB embeds a per-segment identifier
// ("preflight: first segment <id> unavailable: <suffix>") that's stripped
// out for display; filtering by the stripped display value must still match
// the underlying row via the two-sided LIKE, while an unrelated preflight
// reason with a different suffix must not.
func TestListBlocklistItemsPagedPreflightReasonFuzzyMatch(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	matching, err := db.CreateBlocklistItem(ctx, BlocklistMutation{
		Key:    "external_url:http://example/preflight-fuzzy-match.nzb",
		Reason: "preflight: first segment deadbeef1234 unavailable: nzb parse error",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, matching.ID)

	unrelated, err := db.CreateBlocklistItem(ctx, BlocklistMutation{
		Key:    "external_url:http://example/preflight-fuzzy-unrelated.nzb",
		Reason: "preflight: last segment cafef00d5678 unavailable: archive header invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, unrelated.ID)

	page, err := db.ListBlocklistItemsPaged(ctx, BlocklistFilter{Reason: "preflight: nzb parse error", PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("expected the fuzzy preflight reason filter to match exactly 1 row, got %d (items=%+v)", page.Total, page.Items)
	}
	if page.Items[0].ID != matching.ID {
		t.Errorf("expected the matching segment-stripped item %d, got %d", matching.ID, page.Items[0].ID)
	}
}

func TestBlocklistStatsCountsAndGroupsByNormalizedReason(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	active, err := db.CreateBlocklistItem(ctx, BlocklistMutation{
		Key: "external_url:http://example/stats-active.nzb", Reason: "test_stats_active",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, active.ID)

	pastExpiry := time.Now().Add(-1 * time.Hour)
	expired, err := db.CreateBlocklistItem(ctx, BlocklistMutation{
		Key: "external_url:http://example/stats-expired.nzb", Reason: "test_stats_expired", ExpiresAt: &pastExpiry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, expired.ID)

	segA, err := db.CreateBlocklistItem(ctx, BlocklistMutation{
		Key: "external_url:http://example/stats-seg-a.nzb", Reason: "preflight: first segment aaaa1111 unavailable: same suffix",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, segA.ID)

	segB, err := db.CreateBlocklistItem(ctx, BlocklistMutation{
		Key: "external_url:http://example/stats-seg-b.nzb", Reason: "preflight: last segment bbbb2222 unavailable: same suffix",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, segB.ID)

	stats, err := db.BlocklistStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total < 4 {
		t.Fatalf("expected Total to include at least the 4 rows just inserted, got %d", stats.Total)
	}
	if stats.Expired < 1 {
		t.Fatalf("expected Expired to count at least the 1 expired row, got %d", stats.Expired)
	}
	if stats.ByReason["test_stats_active"] != 1 {
		t.Errorf("ByReason[test_stats_active] = %d, want 1", stats.ByReason["test_stats_active"])
	}
	if _, expiredCounted := stats.ByReason["test_stats_expired"]; expiredCounted {
		t.Errorf("expected the expired row's reason not to be counted in the active ByReason breakdown")
	}
	// segA and segB differ only by their per-segment identifier -- the
	// normalization regex should collapse both into the same bucket.
	if stats.ByReason["preflight: same suffix"] != 2 {
		t.Errorf("ByReason[preflight: same suffix] = %d, want 2 (segA + segB collapsed into one normalized bucket)", stats.ByReason["preflight: same suffix"])
	}
}

func TestDeleteAllBlocklistItemsOnlyRemovesActiveEntries(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	active, err := db.CreateBlocklistItem(ctx, BlocklistMutation{Key: "external_url:http://example/delete-all-active.nzb", Reason: "test_delete_all_active"})
	if err != nil {
		t.Fatal(err)
	}
	pastExpiry := time.Now().Add(-1 * time.Hour)
	expired, err := db.CreateBlocklistItem(ctx, BlocklistMutation{Key: "external_url:http://example/delete-all-expired.nzb", Reason: "test_delete_all_expired", ExpiresAt: &pastExpiry})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id in ($1, $2)`, active.ID, expired.ID)

	cleared, err := db.DeleteAllBlocklistItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cleared < 1 {
		t.Fatalf("expected at least 1 row cleared, got %d", cleared)
	}

	var activeCount, expiredCount int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from blocklist_items where id = $1`, active.ID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 0 {
		t.Errorf("expected the active item to be deleted, found %d", activeCount)
	}
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from blocklist_items where id = $1`, expired.ID).Scan(&expiredCount); err != nil {
		t.Fatal(err)
	}
	if expiredCount != 1 {
		t.Errorf("expected the already-expired item to survive DeleteAllBlocklistItems (it only clears active rows), found %d", expiredCount)
	}
}

func TestDeleteBlocklistItemsByReasonOnlyRemovesActiveMatchingRows(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	const reason = "test_delete_by_reason"
	match, err := db.CreateBlocklistItem(ctx, BlocklistMutation{Key: "external_url:http://example/delete-by-reason-match.nzb", Reason: reason})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, match.ID)

	nonMatch, err := db.CreateBlocklistItem(ctx, BlocklistMutation{Key: "external_url:http://example/delete-by-reason-nonmatch.nzb", Reason: "test_delete_by_reason_other"})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, nonMatch.ID)

	pastExpiry := time.Now().Add(-1 * time.Hour)
	expiredMatch, err := db.CreateBlocklistItem(ctx, BlocklistMutation{Key: "external_url:http://example/delete-by-reason-expired.nzb", Reason: reason, ExpiresAt: &pastExpiry})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where id = $1`, expiredMatch.ID)

	deleted, err := db.DeleteBlocklistItemsByReason(ctx, reason)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("expected exactly 1 row deleted (the active matching one), got %d", deleted)
	}

	var matchCount, nonMatchCount, expiredCount int
	sqlDB.QueryRowContext(ctx, `select count(*) from blocklist_items where id = $1`, match.ID).Scan(&matchCount)
	sqlDB.QueryRowContext(ctx, `select count(*) from blocklist_items where id = $1`, nonMatch.ID).Scan(&nonMatchCount)
	sqlDB.QueryRowContext(ctx, `select count(*) from blocklist_items where id = $1`, expiredMatch.ID).Scan(&expiredCount)

	if matchCount != 0 {
		t.Error("expected the active matching-reason item to be deleted")
	}
	if nonMatchCount != 1 {
		t.Error("expected the non-matching-reason item to survive")
	}
	if expiredCount != 1 {
		t.Error("expected the already-expired matching-reason item to survive (only active rows are targeted)")
	}
}

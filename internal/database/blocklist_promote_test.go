package database

import (
	"context"
	"database/sql"
	"testing"
	"time"
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
	t.Cleanup(func() {
		_, _ = sqlDB.ExecContext(context.Background(), `delete from blocklist_items where key = $1`, familyKey)
	})

	return libID, rcPriority, rcFallback
}

func attachSelectedBlocklistTestCandidate(t *testing.T, ctx context.Context, sqlDB *sql.DB, libraryItemID int64, namePrefix string) (releaseCandidateID, selectedReleaseID int64) {
	t.Helper()
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name, selected)
		values ($1, 'Currently Selected Release', $2, 'test-indexer', true)
		returning id`, libraryItemID, namePrefix+"-current-url",
	).Scan(&releaseCandidateID); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, libraryItemID, releaseCandidateID,
	).Scan(&selectedReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		update queue_items
		set state = 'selected', selected_release_id = $2
		where library_item_id = $1`, libraryItemID, selectedReleaseID); err != nil {
		t.Fatal(err)
	}
	return releaseCandidateID, selectedReleaseID
}

func TestLoadCandidateBlocklistOnlyReturnsMatchingLiveKeys(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)
	const scopeKey = "movie:991060"
	candidate := SearchCandidateRecord{
		Title:       "Targeted Blocklist Lookup 991060",
		ExternalURL: "http://example/targeted-blocklist-991060.nzb",
		IndexerName: "targeted-indexer",
		SizeBytes:   2_100_000_000,
		PostedAt:    time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC),
	}
	activeScopedKey := scopeKey + "|" + blocklistReleaseFamilyKey(candidate.Title, candidate.SizeBytes, candidate.PostedAt)
	activeGlobalKey := blocklistKeyForExternalURL(candidate.ExternalURL)
	expiredKey := scopeKey + "|" + blocklistReleaseSignatureKey(candidate.Title, candidate.IndexerName, candidate.SizeBytes, candidate.PostedAt)
	unrelatedKey := "external_url:http://example/unrelated-targeted-blocklist-991060.nzb"
	keys := []string{activeScopedKey, activeGlobalKey, expiredKey, unrelatedKey}
	_, _ = sqlDB.ExecContext(ctx, `delete from blocklist_items where key = any($1::text[])`, pgTextArray(keys))
	t.Cleanup(func() {
		_, _ = sqlDB.ExecContext(context.Background(), `delete from blocklist_items where key = any($1::text[])`, pgTextArray(keys))
	})

	if _, err := sqlDB.ExecContext(ctx, `
		insert into blocklist_items (key, reason, expires_at)
		values
			($1, 'scoped-active', null),
			($2, 'global-active', now() + interval '1 day'),
			($3, 'expired', now() - interval '1 day'),
			($4, 'unrelated-active', null)`,
		activeScopedKey, activeGlobalKey, expiredKey, unrelatedKey,
	); err != nil {
		t.Fatal(err)
	}

	duplicateBatch := []SearchCandidateRecord{candidate, candidate}
	if keys := candidateBatchBlocklistKeys(scopeKey, duplicateBatch); len(keys) != 6 {
		t.Fatalf("duplicate candidate batch produced %d lookup keys, want 6", len(keys))
	}
	blocked, err := loadCandidateBlocklist(ctx, db.SQL, scopeKey, duplicateBatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 2 {
		t.Fatalf("targeted lookup returned %d rows, want 2: %v", len(blocked), blocked)
	}
	if blocked[activeScopedKey] != "scoped-active" || blocked[activeGlobalKey] != "global-active" {
		t.Fatalf("targeted lookup missed active matches: %v", blocked)
	}
	if _, exists := blocked[expiredKey]; exists {
		t.Fatal("targeted lookup returned an expired entry")
	}
	if _, exists := blocked[unrelatedKey]; exists {
		t.Fatal("targeted lookup returned an unrelated entry")
	}
	if reason, found := blockedReleaseReason(scopeKey, blocked, candidate); !found || reason != "scoped-active" {
		t.Fatalf("blocked release reason = %q, found=%v", reason, found)
	}
}

func TestReplaceSearchCandidatesSkipsBlocklistedCandidate(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)
	libID := setupRaceTestLibraryItem(t, ctx, sqlDB, "replace-targeted-blocklist", "requested")
	t.Cleanup(func() {
		_, _ = sqlDB.ExecContext(context.Background(), `delete from library_items where id = $1`, libID)
	})
	scopeKey, err := resolveMediaScopeKey(ctx, sqlDB, libID)
	if err != nil {
		t.Fatal(err)
	}
	blockedCandidate := SearchCandidateRecord{
		Title:       "Replace Blocklisted Candidate 2026",
		ExternalURL: "replace-targeted-blocklist-priority-url",
		IndexerName: "indexer-a",
		SizeBytes:   2_200_000_000,
		PostedAt:    time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC),
		Score:       100,
	}
	fallbackCandidate := SearchCandidateRecord{
		Title:       "Replace Viable Fallback 2026",
		ExternalURL: "replace-targeted-blocklist-fallback-url",
		IndexerName: "indexer-b",
		SizeBytes:   2_300_000_000,
		PostedAt:    blockedCandidate.PostedAt,
		Score:       50,
	}
	blockedKey := scopeKey + "|" + blocklistReleaseFamilyKey(blockedCandidate.Title, blockedCandidate.SizeBytes, blockedCandidate.PostedAt)
	if _, err := sqlDB.ExecContext(ctx, `insert into blocklist_items (key, reason) values ($1, 'replace-blocked')`, blockedKey); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = sqlDB.ExecContext(context.Background(), `delete from blocklist_items where key = $1`, blockedKey)
	})

	selectedReleaseID, err := db.ReplaceSearchCandidates(ctx, libID, []SearchCandidateRecord{blockedCandidate, fallbackCandidate}, false)
	if err != nil {
		t.Fatal(err)
	}
	if selectedReleaseID == nil {
		t.Fatal("expected fallback candidate selection")
	}
	var selectedTitle string
	if err := sqlDB.QueryRowContext(ctx, `
		select rc.title
		from selected_releases sr
		join release_candidates rc on rc.id = sr.release_candidate_id
		where sr.id = $1`, *selectedReleaseID,
	).Scan(&selectedTitle); err != nil {
		t.Fatal(err)
	}
	if selectedTitle != fallbackCandidate.Title {
		t.Fatalf("selected %q, want %q", selectedTitle, fallbackCandidate.Title)
	}
	var rejected bool
	if err := sqlDB.QueryRowContext(ctx, `select rejected from release_candidates where library_item_id = $1 and title = $2`, libID, blockedCandidate.Title).Scan(&rejected); err != nil {
		t.Fatal(err)
	}
	if !rejected {
		t.Fatal("expected matching candidate to be persisted as rejected")
	}
}

func TestPromoteExistingCandidateSkipsBlocklistedSibling(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

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
	db, sqlDB, ctx := openBlocklistTestDB(t)

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
	db, sqlDB, ctx := openBlocklistTestDB(t)

	libID, rcPriority, rcFallback := blocklistPromoteTestFixture(t, ctx, sqlDB, "reject-around-blocklist")
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	// A third, currently-selected candidate is the one actually being
	// rejected -- RejectReleaseCandidate then has to pick a replacement from
	// rcPriority/rcFallback, and must skip the blocklisted one.
	rcCurrent, _ := attachSelectedBlocklistTestCandidate(t, ctx, sqlDB, libID, "reject-around-blocklist")

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

func TestFailSelectedReleaseAndPromoteNextSkipsBlocklistedSibling(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)
	libID, rcPriority, rcFallback := blocklistPromoteTestFixture(t, ctx, sqlDB, "fail-around-blocklist")
	t.Cleanup(func() {
		_, _ = sqlDB.ExecContext(context.Background(), `delete from library_items where id = $1`, libID)
	})
	_, selectedReleaseID := attachSelectedBlocklistTestCandidate(t, ctx, sqlDB, libID, "fail-around-blocklist")

	summary, err := db.FailSelectedReleaseAndPromoteNext(ctx, selectedReleaseID, "context deadline exceeded")
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil {
		t.Fatal("expected a replacement candidate to be promoted")
	}
	if summary.ReleaseCandidateID != rcFallback {
		t.Fatalf("expected blocklisted candidate %d to be skipped in favor of %d, but %d was promoted", rcPriority, rcFallback, summary.ReleaseCandidateID)
	}

	var priorityRejected bool
	if err := sqlDB.QueryRowContext(ctx, `select rejected from release_candidates where id = $1`, rcPriority).Scan(&priorityRejected); err != nil {
		t.Fatal(err)
	}
	if !priorityRejected {
		t.Fatal("expected blocklisted candidate to be marked rejected")
	}
}

// TestFailSelectedReleaseAndPromoteNextAssignsDistinctReasonsToEachBlockedCandidate
// guards markBlockedCandidatesRejected's batched multi-row UPDATE: with TWO
// independently-blocked candidates skipped in the same scan, each must land
// its OWN correct reject_reason on its OWN row -- not the other's, and not
// silently dropped -- exactly the kind of id/reason mix-up a hand-built
// multi-row VALUES query risks getting wrong.
func TestFailSelectedReleaseAndPromoteNextAssignsDistinctReasonsToEachBlockedCandidate(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)
	libID := setupRaceTestLibraryItem(t, ctx, sqlDB, "fail-multi-blocked", "selected")
	t.Cleanup(func() {
		_, _ = sqlDB.ExecContext(context.Background(), `delete from library_items where id = $1`, libID)
	})

	scopeKey, err := resolveMediaScopeKey(ctx, sqlDB, libID)
	if err != nil {
		t.Fatal(err)
	}

	insertBlockedCandidate := func(title, reason string) (rcID int64) {
		sizeBytes := int64(2_000_000_000)
		postedAt := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
		if err := sqlDB.QueryRowContext(ctx, `
			insert into release_candidates (library_item_id, title, external_url, indexer_name, size_bytes, posted_at)
			values ($1, $2, $3, 'indexer-a', $4, $5)
			returning id`, libID, title, "fail-multi-blocked-"+title, sizeBytes, postedAt,
		).Scan(&rcID); err != nil {
			t.Fatal(err)
		}
		familyKey := scopeKey + "|" + blocklistReleaseFamilyKey(title, sizeBytes, postedAt)
		if _, err := sqlDB.ExecContext(ctx, `
			insert into blocklist_items (key, reason) values ($1, $2)`, familyKey, reason,
		); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = sqlDB.ExecContext(context.Background(), `delete from blocklist_items where key = $1`, familyKey)
		})
		return rcID
	}

	rcBlockedA := insertBlockedCandidate("Blocked Release Alpha 2024", "test_reason_alpha")
	rcBlockedB := insertBlockedCandidate("Blocked Release Beta 2024", "test_reason_beta")

	var rcFallback int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'Unrelated Fallback Release 2024', 'fail-multi-blocked-fallback-url', 'indexer-b')
		returning id`, libID,
	).Scan(&rcFallback); err != nil {
		t.Fatal(err)
	}

	_, selectedReleaseID := attachSelectedBlocklistTestCandidate(t, ctx, sqlDB, libID, "fail-multi-blocked")

	summary, err := db.FailSelectedReleaseAndPromoteNext(ctx, selectedReleaseID, "context deadline exceeded")
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil || summary.ReleaseCandidateID != rcFallback {
		t.Fatalf("expected the unrelated fallback candidate to be promoted, got %+v", summary)
	}

	rows, err := sqlDB.QueryContext(ctx, `
		select id, rejected, reject_reason from release_candidates where id in ($1, $2)`, rcBlockedA, rcBlockedB,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[int64]string{}
	for rows.Next() {
		var id int64
		var rejected bool
		var reason string
		if err := rows.Scan(&id, &rejected, &reason); err != nil {
			t.Fatal(err)
		}
		if !rejected {
			t.Fatalf("expected candidate %d to be marked rejected", id)
		}
		got[id] = reason
	}
	if got[rcBlockedA] != "test_reason_alpha" {
		t.Errorf("candidate A reject_reason = %q, want %q", got[rcBlockedA], "test_reason_alpha")
	}
	if got[rcBlockedB] != "test_reason_beta" {
		t.Errorf("candidate B reject_reason = %q, want %q", got[rcBlockedB], "test_reason_beta")
	}
}

func TestFailSelectedReleaseAndPromoteNextMarksActiveItemUnavailable(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)
	libID := setupRaceTestLibraryItem(t, ctx, sqlDB, "fail-active-available", "selected")
	t.Cleanup(func() {
		_, _ = sqlDB.ExecContext(context.Background(), `delete from library_items where id = $1`, libID)
	})
	if _, err := sqlDB.ExecContext(ctx, `update library_items set available = true where id = $1`, libID); err != nil {
		t.Fatal(err)
	}
	_, selectedReleaseID := attachSelectedBlocklistTestCandidate(t, ctx, sqlDB, libID, "fail-active-available")
	if _, err := sqlDB.ExecContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name, score)
		values ($1, 'Replacement Release', 'fail-active-available-replacement-url', 'test-indexer', 100)`, libID,
	); err != nil {
		t.Fatal(err)
	}

	summary, err := db.FailSelectedReleaseAndPromoteNext(ctx, selectedReleaseID, "strict health: broken media")
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil {
		t.Fatal("expected replacement candidate to be promoted")
	}

	var available bool
	if err := sqlDB.QueryRowContext(ctx, `select available from library_items where id = $1`, libID).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if available {
		t.Fatal("expected item to be unavailable until the replacement is published")
	}
}

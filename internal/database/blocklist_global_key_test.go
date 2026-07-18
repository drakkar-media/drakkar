package database

import (
	"testing"
	"time"
)

// TestBlockedReleaseReasonMatchesGlobalManualKey guards against a gap found
// in the 2026-07-18 audit: the manual blocklist API (POST
// /api/blocklist/manual) has no library-item context to scope by, so its
// "external_url" and "release_signature" entries are written without a
// scopeKey prefix -- a deliberately global "block this everywhere" tool.
// blockedReleaseReason used to only check scopeKey-prefixed keys
// (blocklistKeysForRelease), so a manually-added global entry could never
// match anything and was silently inert.
func TestBlockedReleaseReasonMatchesGlobalManualKey(t *testing.T) {
	const scopeKey = "movie:123"
	candidate := SearchCandidateRecord{
		Title:       "Some Movie 2024",
		ExternalURL: "http://example/manually-blocked.nzb",
		IndexerName: "indexer-a",
		SizeBytes:   1_500_000_000,
		PostedAt:    time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
	}

	// Simulates a manual blocklist entry added via keyType=external_url,
	// which internal/api/router.go's manualBlocklistKey writes as exactly
	// this unscoped key.
	blocked := map[string]string{
		blocklistKeyForExternalURL(candidate.ExternalURL): "manual",
	}
	reason, isBlocked := blockedReleaseReason(scopeKey, blocked, candidate)
	if !isBlocked || reason != "manual" {
		t.Fatalf("expected a global external_url block to match, got reason=%q isBlocked=%v", reason, isBlocked)
	}

	// Simulates a manual blocklist entry added via keyType=release_signature.
	blockedBySignature := map[string]string{
		blocklistReleaseSignatureKey(candidate.Title, candidate.IndexerName, candidate.SizeBytes, candidate.PostedAt): "manual",
	}
	reason, isBlocked = blockedReleaseReason(scopeKey, blockedBySignature, candidate)
	if !isBlocked || reason != "manual" {
		t.Fatalf("expected a global release_signature block to match, got reason=%q isBlocked=%v", reason, isBlocked)
	}

	// An unrelated global key must not false-positive match.
	unrelated := map[string]string{
		blocklistKeyForExternalURL("http://example/other-release.nzb"): "manual",
	}
	if _, isBlocked := blockedReleaseReason(scopeKey, unrelated, candidate); isBlocked {
		t.Fatal("expected an unrelated global key not to match")
	}
}

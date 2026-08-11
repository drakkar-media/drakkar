package database

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestMessageIDSuffixRoundTrips guards the 2026-08-11 fix: blocklist entries
// for a "confirmed gone article" reason previously discarded which specific
// NNTP article failed, so a wrong verdict (e.g. a throttle-induced false
// 430) could never be re-checked or corrected later. withMessageIDSuffix/
// splitMessageIDSuffix must round-trip cleanly and leave the reason text
// unaffected when no message-id is present.
func TestMessageIDSuffixRoundTrips(t *testing.T) {
	annotated := withMessageIDSuffix("preflight: interior sample segment unavailable: article missing", "<msg1@example>")
	clean, msgID := splitMessageIDSuffix(annotated)
	if clean != "preflight: interior sample segment unavailable: article missing" {
		t.Fatalf("expected suffix stripped cleanly, got %q", clean)
	}
	if msgID != "<msg1@example>" {
		t.Fatalf("expected message-id round-tripped, got %q", msgID)
	}

	// No suffix present -- must pass through unchanged with an empty message-id.
	clean, msgID = splitMessageIDSuffix("manual_reject")
	if clean != "manual_reject" || msgID != "" {
		t.Fatalf("expected unchanged reason and empty message-id, got (%q, %q)", clean, msgID)
	}

	// Empty message-id must not add a suffix at all.
	if got := withMessageIDSuffix("archive_encrypted", ""); got != "archive_encrypted" {
		t.Fatalf("expected no suffix for an empty message-id, got %q", got)
	}
}

// TestFlushBlocklistKeysStoresMessageID is the end-to-end counterpart: a
// reason carrying a withMessageIDSuffix annotation must land in
// blocklist_items.message_id, with the stored reason text itself cleaned of
// the annotation.
func TestFlushBlocklistKeysStoresMessageID(t *testing.T) {
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

	key := "message-id-suffix-test-key"
	defer sqlDB.ExecContext(ctx, `delete from blocklist_items where key = $1`, key)

	reason := withMessageIDSuffix("preflight: interior sample segment unavailable: article missing", "<confirm-me@example>")
	db.flushBlocklistKeys([]string{key}, reason, 0)

	var storedReason string
	var storedMessageID sql.NullString
	if err := sqlDB.QueryRowContext(ctx, `select reason, message_id from blocklist_items where key = $1`, key).Scan(&storedReason, &storedMessageID); err != nil {
		t.Fatal(err)
	}
	if storedReason != "preflight: interior sample segment unavailable: article missing" {
		t.Fatalf("expected stored reason to be cleaned of the suffix, got %q", storedReason)
	}
	if !storedMessageID.Valid || storedMessageID.String != "<confirm-me@example>" {
		t.Fatalf("expected message_id to be stored, got %+v", storedMessageID)
	}
}

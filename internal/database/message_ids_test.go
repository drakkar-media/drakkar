package database

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPackedMessageIDsRoundTrip(t *testing.T) {
	ids := []string{
		"<abc@example.test>",
		"<def@example.test>",
		"<ghi@example.test>",
	}
	packed := packMessageIDs(ids)
	if len(packed) == 0 {
		t.Fatal("expected packed message IDs")
	}
	got, err := unpackMessageIDs(packed, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, ids) {
		t.Fatalf("ids mismatch: got=%v want=%v", got, ids)
	}
}

func TestUnpackMessageIDsFallsBackToLegacyArray(t *testing.T) {
	got, err := unpackMessageIDs(nil, `{"<a@example.test>","<b@example.test>"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"<a@example.test>", "<b@example.test>"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ids mismatch: got=%v want=%v", got, want)
	}
}

func TestRestoreNZBFileMessageIDsRestoresLegacyArray(t *testing.T) {
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

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `insert into library_items (media_type, title) values ('movie','restore-message-ids') returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)
	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `insert into release_candidates (library_item_id, title) values ($1, 'restore-message-ids') returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := sqlDB.QueryRowContext(ctx, `insert into selected_releases (library_item_id, release_candidate_id) values ($1, $2) returning id`, libID, rcID).Scan(&srID); err != nil {
		t.Fatal(err)
	}
	var nzbDocID int64
	if err := sqlDB.QueryRowContext(ctx, `insert into nzb_documents (selected_release_id, file_name) values ($1, 'restore-message-ids.nzb') returning id`, srID).Scan(&nzbDocID); err != nil {
		t.Fatal(err)
	}
	ids := []string{"<restore-a@example.test>", "<restore-b@example.test>"}
	var nzbFileID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into nzb_files (nzb_document_id, subject, message_ids, message_ids_packed, message_id_count)
		values ($1, 'restore-message-ids.mkv', '{}', $2, $3)
		returning id`,
		nzbDocID, packMessageIDs(ids), len(ids),
	).Scan(&nzbFileID); err != nil {
		t.Fatal(err)
	}

	updated, err := (&DB{SQL: sqlDB}).RestoreNZBFileMessageIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if updated == 0 {
		t.Fatal("expected at least one restored row")
	}

	var raw string
	var packedIsNull bool
	var count int
	if err := sqlDB.QueryRowContext(ctx, `
		select message_ids::text, message_ids_packed is null, message_id_count
		from nzb_files
		where id = $1`,
		nzbFileID,
	).Scan(&raw, &packedIsNull, &count); err != nil {
		t.Fatal(err)
	}
	if got := parsePostgresArray(raw); !reflect.DeepEqual(got, ids) {
		t.Fatalf("message_ids mismatch: got=%v want=%v", got, ids)
	}
	if !packedIsNull {
		t.Fatal("message_ids_packed was not cleared")
	}
	if count != len(ids) {
		t.Fatalf("message_id_count=%d want %d", count, len(ids))
	}
}

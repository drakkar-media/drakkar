package database

import (
	"reflect"
	"testing"
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

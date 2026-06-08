package fuse

import (
	"testing"
	"time"
)

func TestTopLevelExact(t *testing.T) {
	root := NewRoot()
	got := root.TopLevel()
	want := []string{".ids", "completed-symlinks", "content", "nzbs"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestOperationMatrix(t *testing.T) {
	root := NewRoot()
	cases := []struct {
		path string
		op   string
		ok   bool
	}{
		{"/.ids/content/file", "read", true},
		{"/.ids/content/file", "write", false},
		{"/completed-symlinks/link", "readlink", true},
		{"/completed-symlinks/link", "unlink", false},
		{"/content/releases/file", "open", true},
		{"/content/releases/file", "unlink", false},
		{"/nzbs/test.nzb", "write", true},
		{"/nzbs/test.nzb", "rename", false},
	}
	for _, tc := range cases {
		err := root.Allowed(tc.path, tc.op, false)
		if tc.ok && err != nil {
			t.Fatalf("%s %s expected ok, got %v", tc.path, tc.op, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s %s expected error", tc.path, tc.op)
		}
	}
	if err := root.Allowed("/content/releases/file", "unlink", true); err != nil {
		t.Fatalf("content unlink should be allowed when writable: %v", err)
	}
}

// TestIDsStableLookup verifies the /.ids namespace allows reads, rejects writes,
// and returns the same synthetic timestamp on every call (stable inode semantics).
func TestIDsStableLookup(t *testing.T) {
	root := NewRoot()

	// /.ids allows read-only operations.
	for _, op := range []string{"lookup", "getattr", "readdir", "open", "read"} {
		if err := root.Allowed("/.ids/content/abc123", op, false); err != nil {
			t.Fatalf("/.ids %s should be allowed, got %v", op, err)
		}
	}

	// /.ids rejects all mutation operations.
	for _, op := range []string{"write", "create", "mkdir", "rename", "unlink", "symlink"} {
		if err := root.Allowed("/.ids/content/abc123", op, false); err == nil {
			t.Fatalf("/.ids %s should be rejected", op)
		}
	}

	// Synthetic timestamp is stable across multiple calls (same value).
	ts1 := root.Timestamp()
	time.Sleep(time.Millisecond)
	ts2 := root.Timestamp()
	if !ts1.Equal(ts2) {
		t.Fatalf("timestamp not stable: %v vs %v", ts1, ts2)
	}
	// Timestamp must be the spec-mandated epoch (2000-01-01T00:00:00Z).
	want := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if !ts1.Equal(want) {
		t.Fatalf("expected synthetic timestamp %v, got %v", want, ts1)
	}
}

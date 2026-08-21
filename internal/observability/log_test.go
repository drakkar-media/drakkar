package observability

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverWithCleanupRunsCleanupOnPanic(t *testing.T) {
	cleanupRan := false
	var recoveredValue any

	func() {
		defer RecoverWithCleanup("test-goroutine", func(recovered any) {
			cleanupRan = true
			recoveredValue = recovered
		})
		panic("boom")
	}()

	if !cleanupRan {
		t.Fatal("expected cleanup to run after a panic")
	}
	if recoveredValue != "boom" {
		t.Fatalf("expected cleanup to receive the recovered value, got %v", recoveredValue)
	}
}

func TestRecoverWithCleanupSkipsCleanupWithoutPanic(t *testing.T) {
	cleanupRan := false

	func() {
		defer RecoverWithCleanup("test-goroutine", func(recovered any) {
			cleanupRan = true
		})
	}()

	if cleanupRan {
		t.Fatal("expected cleanup to be skipped when there was no panic")
	}
}

func TestRotatingFileRotatesAtSizeThreshold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drakkar.log")

	rf, err := newRotatingFile(path, 10, 2)
	if err != nil {
		t.Fatalf("newRotatingFile: %v", err)
	}

	if _, err := rf.Write([]byte("12345")); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if _, err := rf.Write([]byte("67890")); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	// size is now 10, at the threshold but not over — no rotation yet.
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("expected no backup yet, stat err = %v", err)
	}

	// This write would push size to 15 > maxSize 10, so it must rotate first.
	if _, err := rf.Write([]byte("abcde")); err != nil {
		t.Fatalf("write 3: %v", err)
	}

	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("expected backup file to exist: %v", err)
	}
	if string(backup) != "1234567890" {
		t.Fatalf("backup content = %q, want %q", backup, "1234567890")
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected current file to exist: %v", err)
	}
	if string(current) != "abcde" {
		t.Fatalf("current content = %q, want %q", current, "abcde")
	}
	if rf.size != 5 {
		t.Fatalf("size = %d, want 5", rf.size)
	}
}

// TestRotatingFileSurvivesFailedRotationRename guards a real gap: rotate()
// closes the current file handle up front, then renames the log to its
// backup path -- if that rename fails (simulated here by making the backup
// path an existing non-empty directory, which makes a file-onto-directory
// rename fail with EISDIR regardless of permissions), the old code returned
// early without ever reopening a file. r.f was left permanently closed, so
// every subsequent Write silently failed forever -- logging was gone for
// the rest of the process's life. A failed rotation must still leave a
// writable file behind.
func TestRotatingFileSurvivesFailedRotationRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drakkar.log")

	rf, err := newRotatingFile(path, 5, 1)
	if err != nil {
		t.Fatalf("newRotatingFile: %v", err)
	}

	// Occupy the rotation's target backup path with a non-empty directory,
	// so os.Rename(path, path+".1") is guaranteed to fail with EISDIR --
	// deterministic and independent of permissions (this test may run as
	// root, where permission-based failures wouldn't reliably trigger).
	backupDir := path + ".1"
	if err := os.Mkdir(backupDir, 0o755); err != nil {
		t.Fatalf("mkdir backup dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "occupied"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed backup dir: %v", err)
	}

	if _, err := rf.Write([]byte("12345")); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	// This write pushes size past maxSize, forcing rotate() -- which must
	// fail internally (the rename above can't succeed) but still leave a
	// writable file behind.
	n, err := rf.Write([]byte("abcde"))
	if err != nil {
		t.Fatalf("expected Write to still succeed despite the failed rotation (matching \"rotation failure is non-fatal\"), got: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected 5 bytes written, got %d", n)
	}

	// The real proof: logging must still work on a THIRD write, after
	// rotate() has already failed once. The old bug's symptom was r.f stuck
	// on a closed handle forever, not just a one-off error from the failed
	// rotation itself.
	if _, err := rf.Write([]byte("still-logging")); err != nil {
		t.Fatalf("expected logging to keep working after a failed rotation, got: %v", err)
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected the log file to still exist and be readable: %v", err)
	}
	if string(current) != "12345abcdestill-logging" {
		t.Fatalf("current content = %q, want all three writes appended", current)
	}
}

func TestRotatingFileKeepsOnlyMaxBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drakkar.log")

	rf, err := newRotatingFile(path, 1, 2)
	if err != nil {
		t.Fatalf("newRotatingFile: %v", err)
	}

	for _, chunk := range []string{"a", "b", "c", "d"} {
		if _, err := rf.Write([]byte(chunk)); err != nil {
			t.Fatalf("write %q: %v", chunk, err)
		}
	}

	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("expected no .3 backup with maxBackups=2, stat err = %v", err)
	}
	b1, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("expected .1 backup: %v", err)
	}
	b2, err := os.ReadFile(path + ".2")
	if err != nil {
		t.Fatalf("expected .2 backup: %v", err)
	}
	if string(b1) != "c" || string(b2) != "b" {
		t.Fatalf("backups = %q, %q; want %q, %q", b1, b2, "c", "b")
	}
}

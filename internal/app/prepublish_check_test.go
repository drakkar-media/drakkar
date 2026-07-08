package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitForReadableVideoContainerRejectsPersistentUnreadable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "content", "releases", "missing.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	err := waitForReadableVideoContainer(context.Background(), path, 2, 10*time.Millisecond)
	if !errors.Is(err, errContainerHeaderUnreadable) {
		t.Fatalf("expected unreadable header error, got %v", err)
	}
}

func TestWaitForReadableVideoContainerAcceptsFileWhenItAppearsLater(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "content", "releases", "later.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(25 * time.Millisecond)
		_ = os.WriteFile(path, []byte{0x1a, 0x45, 0xdf, 0xa3, 0x01, 0x02, 0x03, 0x04}, 0o644)
	}()

	if err := waitForReadableVideoContainer(context.Background(), path, 5, 20*time.Millisecond); err != nil {
		t.Fatalf("expected delayed readable file to pass, got %v", err)
	}
}

func TestWaitForReadableVideoContainerFailsFastOnInvalidMagic(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "content", "releases", "invalid.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a video container"), 0o644); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err := waitForReadableVideoContainer(context.Background(), path, 5, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected invalid container error")
	}
	if errors.Is(err, errContainerHeaderUnreadable) {
		t.Fatalf("expected invalid container error, got %v", err)
	}
	if time.Since(start) > 7*time.Second {
		t.Fatalf("expected invalid magic to stop after first header check, took %v", time.Since(start))
	}
}

func TestIsTransientHealthCheckErrTreatsWrappedContainerCancellationAsTransient(t *testing.T) {
	err := fmt.Errorf("invalid video container: %w", fmt.Errorf("%w: %v", errContainerHeaderUnreadable, context.Canceled))
	if !isTransientHealthCheckErr(err) {
		t.Fatalf("expected wrapped container cancellation to be transient, got %v", err)
	}
}

// TestVerifyOneFileBeforePublishAllowsInconclusiveRead guards against the
// regression where a pre-publish check that could never read the header
// (provider throttling, momentary VFS cache lag right after import) blocked
// publish outright — that blocklisted good releases on every hiccup and
// starved the download queue. Inconclusive must be non-fatal here; only a
// definitive "read real bytes, wrong format" may block publish.
func TestVerifyOneFileBeforePublishAllowsInconclusiveRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "content", "releases", "never-appears.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	err := verifyOneFileBeforePublish(context.Background(), path, "never-appears.mkv")
	if err == nil {
		t.Fatal("expected a non-nil error signalling inconclusive read")
	}
	if !errors.Is(err, errContainerHeaderUnreadable) {
		t.Fatalf("expected errContainerHeaderUnreadable, got %v", err)
	}
}

func TestVerifyOneFileBeforePublishBlocksOnDefinitivelyInvalidContainer(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "content", "releases", "fake.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a video container"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := verifyOneFileBeforePublish(context.Background(), path, "fake.mkv")
	if err == nil {
		t.Fatal("expected an error for a definitively invalid container")
	}
	if errors.Is(err, errContainerHeaderUnreadable) {
		t.Fatalf("expected a definitive (non-transient) error, got %v", err)
	}
}

func TestVerifyOneFileBeforePublishAcceptsValidContainer(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "content", "releases", "good.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{0x1a, 0x45, 0xdf, 0xa3, 0x01, 0x02, 0x03, 0x04}, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := verifyOneFileBeforePublish(context.Background(), path, "good.mkv"); err != nil {
		t.Fatalf("expected valid container to pass, got %v", err)
	}
}

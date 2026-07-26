package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rs/zerolog"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/rclone"
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

// TestVerifyContentBeforePublishFailNzbWithoutVideo guards the 2026-07-26
// feature: a release whose entries are all non-video (subtitles, .nfo,
// sample clips) previously always published successfully regardless of
// content -- ListVirtualFilesForRelease's own ErrNoVirtualFiles only catches
// a release with literally ZERO files, and this function's per-file loop
// only ever validates entries that already look like video, silently
// succeeding when none do. With failNzbWithoutVideo=true, a release with no
// recognized playable video file must be rejected instead of published;
// with it false (the default), behavior is unchanged from before this
// feature existed.
func TestVerifyContentBeforePublishFailNzbWithoutVideo(t *testing.T) {
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
	db := &database.DB{SQL: sqlDB}

	var libID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, title, available)
		values ('movie', 'prepublish-no-video-check', false)
		returning id`).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from library_items where id = $1`, libID)

	var rcID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, external_url, indexer_name)
		values ($1, 'prepublish-no-video-check', 'http://example/prepublish-no-video', 'test-indexer')
		returning id`, libID).Scan(&rcID); err != nil {
		t.Fatal(err)
	}
	var srID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2) returning id`, libID, rcID,
	).Scan(&srID); err != nil {
		t.Fatal(err)
	}
	// Only a subtitle file -- no recognized video extension at all.
	if _, err := sqlDB.ExecContext(ctx, `
		insert into virtual_files (selected_release_id, path, file_name, size_bytes, reader_kind)
		values ($1, $2, 'movie.srt', 4096, 'direct_nzb')`, srID, fmt.Sprintf("releases/%d/movie.srt", srID),
	); err != nil {
		t.Fatal(err)
	}

	rt := config.DefaultRuntime()
	rt.FuseMountPath = t.TempDir()
	rc := rclone.NewClient("http://127.0.0.1:1") // unreachable; RefreshMountPath's error is discarded by the caller
	logger := zerolog.Nop()

	if err := verifyContentBeforePublish(ctx, db, rt, rc, srID, false, logger); err != nil {
		t.Fatalf("expected no error with failNzbWithoutVideo=false (default/original behavior), got %v", err)
	}
	if err := verifyContentBeforePublish(ctx, db, rt, rc, srID, true, logger); err == nil {
		t.Fatal("expected an error with failNzbWithoutVideo=true for a release with no recognized video file")
	}
}

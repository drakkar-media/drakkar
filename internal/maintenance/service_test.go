package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/database"
)

// backdateSymlink sets a symlink's OWN mtime (not its target's -- os.Chtimes
// follows symlinks, which is the wrong file here) to simulate a genuinely
// old, long-orphaned file rather than one just created by the test itself.
func backdateSymlink(t *testing.T, path string, age time.Duration) {
	t.Helper()
	ts := unix.NsecToTimeval(time.Now().Add(-age).UnixNano())
	if err := unix.Lutimes(path, []unix.Timeval{ts, ts}); err != nil {
		t.Fatal(err)
	}
}

type repoStub struct {
	records   []database.SymlinkPublicationRecord
	deleted   []int64
	touched   []string
	listCalls int
}

func (r *repoStub) ListSymlinkPublicationRecords(ctx context.Context) ([]database.SymlinkPublicationRecord, error) {
	r.listCalls++
	return r.records, nil
}

func (r *repoStub) DeleteSymlinkPublication(ctx context.Context, publicationID int64) error {
	r.deleted = append(r.deleted, publicationID)
	return nil
}

func (r *repoStub) TouchMaintenanceCursor(ctx context.Context, taskName string, cursor string) error {
	r.touched = append(r.touched, taskName)
	return nil
}

func (r *repoStub) PruneStaleReleaseCandidates(ctx context.Context, olderThan time.Duration) (int64, error) {
	return 0, nil
}

func (r *repoStub) PruneOrphanedSelectedReleases(ctx context.Context, olderThan time.Duration) (int64, error) {
	return 0, nil
}

func (r *repoStub) RestoreNZBFileMessageIDs(ctx context.Context) (int64, error) {
	return 0, nil
}

func TestRemoveBrokenMediaSymlinks(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "broken.mkv")
	if err := os.Symlink(filepath.Join(root, "missing-target"), link); err != nil {
		t.Fatal(err)
	}
	repo := &repoStub{
		records: []database.SymlinkPublicationRecord{{ID: 11, LibraryPath: link, TargetPath: filepath.Join(root, "missing-target")}},
	}
	rt := config.DefaultRuntime()
	service := NewService(repo, rt)

	result, err := service.RemoveBrokenMediaSymlinks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedFiles != 1 || result.DeletedRows != 1 {
		t.Fatalf("unexpected result %+v", result)
	}
}

// TestRemoveBrokenMediaSymlinksSkipsTransientTargetStatError guards against
// treating a non-ENOENT stat error on the FUSE-mounted target (e.g. a
// transient rclone timeout) as proof the content is gone. Simulated here via
// a target path with a regular file as one of its parent components, which
// makes os.Stat fail with ENOTDIR rather than ENOENT (unlike permission
// bits, this is enforced for root too) -- the symlink and its DB record must
// survive so the item stays published instead of being incorrectly reset to
// missing.
func TestRemoveBrokenMediaSymlinksSkipsTransientTargetStatError(t *testing.T) {
	root := t.TempDir()
	notADir := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(notADir, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(notADir, "target.mkv")

	link := filepath.Join(root, "published.mkv")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	repo := &repoStub{
		records: []database.SymlinkPublicationRecord{{ID: 14, LibraryPath: link, TargetPath: target}},
	}
	rt := config.DefaultRuntime()
	service := NewService(repo, rt)

	result, err := service.RemoveBrokenMediaSymlinks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedFiles != 0 || result.DeletedRows != 0 {
		t.Fatalf("expected transient stat error to be skipped, got %+v", result)
	}
	if len(repo.deleted) != 0 {
		t.Fatalf("expected no publication rows deleted, got %v", repo.deleted)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("expected symlink to survive, err=%v", err)
	}
}

func TestRemoveOrphanedCompletedSymlinks(t *testing.T) {
	repo := &repoStub{
		records: []database.SymlinkPublicationRecord{{ID: 12, LibraryPath: "/missing/file.mkv", TargetPath: "/target"}},
	}
	rt := config.DefaultRuntime()
	service := NewService(repo, rt)

	result, err := service.RemoveOrphanedCompletedSymlinks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedRows != 1 {
		t.Fatalf("unexpected result %+v", result)
	}
}

// TestRemoveOrphanedCompletedSymlinksSkipsTransientLstatError guards against
// treating a non-ENOENT Lstat error on the symlink's own path (e.g. a
// transient filesystem hiccup) as proof it's gone -- the same anti-pattern
// already fixed in the sibling RemoveBrokenMediaSymlinks. Simulated here via
// an ENOTDIR (a parent path component is a regular file), which -- unlike
// permission bits -- is enforced for root too.
func TestRemoveOrphanedCompletedSymlinksSkipsTransientLstatError(t *testing.T) {
	root := t.TempDir()
	notADir := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(notADir, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(notADir, "published.mkv")

	repo := &repoStub{
		records: []database.SymlinkPublicationRecord{{ID: 15, LibraryPath: link, TargetPath: "/target"}},
	}
	rt := config.DefaultRuntime()
	service := NewService(repo, rt)

	result, err := service.RemoveOrphanedCompletedSymlinks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedRows != 0 {
		t.Fatalf("expected transient Lstat error to be skipped, got %+v", result)
	}
	if len(repo.deleted) != 0 {
		t.Fatalf("expected no publication rows deleted, got %v", repo.deleted)
	}
}

func TestRemoveOrphanedContent(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	tv := filepath.Join(root, "tv")
	if err := os.MkdirAll(movies, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tv, 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(movies, "orphan.mkv")
	if err := os.Symlink("/mnt/drakkar/vfs/content/releases/1/orphan.mkv", orphan); err != nil {
		t.Fatal(err)
	}
	// Backdated past orphanedContentGracePeriod -- a genuinely old, orphaned
	// symlink, not one that merely looks unknown because it was published a
	// moment after this run's known-publications snapshot was taken.
	backdateSymlink(t, orphan, 2*time.Hour)
	kept := filepath.Join(movies, "kept.mkv")
	if err := os.Symlink("/mnt/drakkar/vfs/content/releases/1/kept.mkv", kept); err != nil {
		t.Fatal(err)
	}
	repo := &repoStub{
		records: []database.SymlinkPublicationRecord{{ID: 13, LibraryPath: kept, TargetPath: "/mnt/drakkar/vfs/content/releases/1/kept.mkv"}},
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = movies
	rt.TVLibraryPath = tv
	service := NewService(repo, rt)

	result, err := service.RemoveOrphanedContent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedFiles != 1 {
		t.Fatalf("unexpected result %+v", result)
	}
	if _, err := os.Lstat(orphan); !os.IsNotExist(err) {
		t.Fatalf("expected orphan removed, err=%v", err)
	}
	if _, err := os.Lstat(kept); err != nil {
		t.Fatalf("expected kept link, err=%v", err)
	}
}

// TestRemoveOrphanedContentSkipsRecentlyPublishedSymlink guards the actual
// race: known publications are snapshotted once before the (potentially
// long) filesystem walk begins, so a symlink published moments ago -- after
// the snapshot, before the walk reached it -- looks identical to a
// genuinely orphaned one to the walk itself. It must survive this run; the
// NEXT run's fresh snapshot is what should judge it.
func TestRemoveOrphanedContentSkipsRecentlyPublishedSymlink(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	tv := filepath.Join(root, "tv")
	if err := os.MkdirAll(movies, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tv, 0o755); err != nil {
		t.Fatal(err)
	}
	// Not backdated -- mtime is "now", exactly like a symlink this same
	// maintenance pass's own snapshot just missed.
	justPublished := filepath.Join(movies, "just-published.mkv")
	if err := os.Symlink("/mnt/drakkar/vfs/content/releases/1/just-published.mkv", justPublished); err != nil {
		t.Fatal(err)
	}
	repo := &repoStub{} // empty: nothing known yet, matching a stale snapshot
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = movies
	rt.TVLibraryPath = tv
	service := NewService(repo, rt)

	result, err := service.RemoveOrphanedContent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedFiles != 0 {
		t.Fatalf("expected the recently-published symlink to be spared, got %+v", result)
	}
	if _, err := os.Lstat(justPublished); err != nil {
		t.Fatalf("expected symlink to survive, err=%v", err)
	}
}

// TestRunSymlinkMaintenanceFetchesRecordsOnce guards a real production gap:
// runStorageMaintenance (internal/app/app.go) called RemoveOrphanedContent,
// RemoveBrokenMediaSymlinks, and RemoveOrphanedCompletedSymlinks back to
// back every 6h, and each one independently issued its own full
// ListSymlinkPublicationRecords query -- three full-table scans purely to
// build the same "known paths" lookup three times over. RunSymlinkMaintenance
// must fetch the table exactly once and still run all three passes
// correctly off that single shared snapshot.
func TestRunSymlinkMaintenanceFetchesRecordsOnce(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	tv := filepath.Join(root, "tv")
	if err := os.MkdirAll(movies, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tv, 0o755); err != nil {
		t.Fatal(err)
	}

	// broken.mkv: a known publication whose symlink is genuinely missing --
	// should be caught by the broken-media-symlinks pass.
	broken := filepath.Join(movies, "broken.mkv")

	// orphan.mkv: an on-disk symlink with NO matching publication record at
	// all -- should be caught by the orphaned-content pass.
	orphan := filepath.Join(movies, "orphan.mkv")
	if err := os.Symlink("/mnt/drakkar/vfs/content/releases/1/orphan.mkv", orphan); err != nil {
		t.Fatal(err)
	}
	backdateSymlink(t, orphan, 2*time.Hour)

	// kept.mkv: a known publication whose symlink is present and points at a
	// real, existing target -- must survive every pass, including the
	// broken-media-symlinks pass's target-existence check.
	keptTarget := filepath.Join(root, "kept-target.mkv")
	if err := os.WriteFile(keptTarget, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(movies, "kept.mkv")
	if err := os.Symlink(keptTarget, kept); err != nil {
		t.Fatal(err)
	}

	repo := &repoStub{
		records: []database.SymlinkPublicationRecord{
			{ID: 21, LibraryPath: broken, TargetPath: filepath.Join(root, "missing-target")},
			{ID: 22, LibraryPath: kept, TargetPath: keptTarget},
		},
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = movies
	rt.TVLibraryPath = tv
	service := NewService(repo, rt)

	brokenResult, orphanedCompletedResult, orphanedContentResult, err := service.RunSymlinkMaintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if repo.listCalls != 1 {
		t.Fatalf("expected exactly 1 ListSymlinkPublicationRecords call across all 3 passes, got %d", repo.listCalls)
	}
	if brokenResult.DeletedRows != 1 {
		t.Fatalf("expected the broken-media-symlinks pass to delete the missing publication, got %+v", brokenResult)
	}
	// orphaned-completed-symlinks re-checks the same "library path missing"
	// condition as the broken pass above and re-deletes (a harmless no-op,
	// same pre-existing overlap between the two passes -- not the scope of
	// this fix) the same row from the shared snapshot.
	if orphanedCompletedResult.DeletedRows != 1 {
		t.Fatalf("expected the orphaned-completed-symlinks pass to also observe the missing publication, got %+v", orphanedCompletedResult)
	}
	if orphanedContentResult.DeletedFiles != 1 {
		t.Fatalf("expected the orphaned-content pass to remove the unknown symlink, got %+v", orphanedContentResult)
	}
	if _, err := os.Lstat(kept); err != nil {
		t.Fatalf("expected the valid, known symlink to survive all 3 passes, err=%v", err)
	}
}

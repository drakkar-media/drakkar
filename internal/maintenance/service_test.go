package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hjongedijk/drakkar/internal/config"
	"github.com/hjongedijk/drakkar/internal/database"
)

type repoStub struct {
	records []database.SymlinkPublicationRecord
	deleted []int64
	touched []string
}

func (r *repoStub) ListSymlinkPublicationRecords(ctx context.Context) ([]database.SymlinkPublicationRecord, error) {
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

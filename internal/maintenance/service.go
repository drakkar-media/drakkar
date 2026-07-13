package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/database"
)

type Repository interface {
	ListSymlinkPublicationRecords(ctx context.Context) ([]database.SymlinkPublicationRecord, error)
	DeleteSymlinkPublication(ctx context.Context, publicationID int64) error
	TouchMaintenanceCursor(ctx context.Context, taskName string, cursor string) error
	PruneStaleReleaseCandidates(ctx context.Context, olderThan time.Duration) (int64, error)
	PruneOrphanedSelectedReleases(ctx context.Context, olderThan time.Duration) (int64, error)
}

// releaseCandidateRetention is how long an unselected, unreferenced
// release_candidates row is kept before it's eligible for pruning.
const releaseCandidateRetention = 14 * 24 * time.Hour

// orphanedSelectedReleaseRetention is deliberately much shorter than
// releaseCandidateRetention: an orphaned selected_releases row represents
// actually-downloaded content (cascades to virtual_files/archives/
// nzb_documents) sitting unused on disk/in the DB, not just search metadata.
// 1 hour gives generous margin over the longest legitimate in-flight window
// elsewhere in the codebase (the 90-minute download-stale timeout).
const orphanedSelectedReleaseRetention = time.Hour

type Service struct {
	repo    Repository
	runtime config.Runtime
}

type Result struct {
	TaskName      string `json:"taskName"`
	DeletedFiles  int    `json:"deletedFiles"`
	DeletedRows   int    `json:"deletedRows"`
	ScannedFiles  int    `json:"scannedFiles"`
	ScannedRows   int    `json:"scannedRows"`
	ResetItems    int    `json:"resetItems"`
	RepairedItems int    `json:"repairedItems"`
	DegradedItems int    `json:"degradedItems"`
}

func NewService(repo Repository, runtime config.Runtime) *Service {
	return &Service{repo: repo, runtime: runtime}
}

func (s *Service) RemoveBrokenMediaSymlinks(ctx context.Context) (Result, error) {
	records, err := s.repo.ListSymlinkPublicationRecords(ctx)
	if err != nil {
		return Result{}, err
	}
	result := Result{TaskName: "broken-media-symlinks", ScannedRows: len(records)}
	for _, record := range records {
		info, err := os.Lstat(record.LibraryPath)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			if err == nil {
				_ = os.Remove(record.LibraryPath)
				result.DeletedFiles++
			}
			if err := s.repo.DeleteSymlinkPublication(ctx, record.ID); err != nil {
				return result, err
			}
			result.DeletedRows++
			continue
		}
		if _, err := os.Stat(record.TargetPath); err != nil {
			if err := os.Remove(record.LibraryPath); err == nil {
				result.DeletedFiles++
			}
			if err := s.repo.DeleteSymlinkPublication(ctx, record.ID); err != nil {
				return result, err
			}
			result.DeletedRows++
		}
	}
	return result, s.repo.TouchMaintenanceCursor(ctx, result.TaskName, time.Now().UTC().Format(time.RFC3339))
}

func (s *Service) RemoveOrphanedCompletedSymlinks(ctx context.Context) (Result, error) {
	records, err := s.repo.ListSymlinkPublicationRecords(ctx)
	if err != nil {
		return Result{}, err
	}
	result := Result{TaskName: "orphaned-completed-symlinks", ScannedRows: len(records)}
	for _, record := range records {
		if _, err := os.Lstat(record.LibraryPath); err == nil {
			continue
		}
		if err := s.repo.DeleteSymlinkPublication(ctx, record.ID); err != nil {
			return result, err
		}
		result.DeletedRows++
	}
	return result, s.repo.TouchMaintenanceCursor(ctx, result.TaskName, time.Now().UTC().Format(time.RFC3339))
}

// PruneStaleReleaseCandidates deletes old, never-selected release_candidates
// rows so search history doesn't grow unbounded. Rows tied to an actual grab
// (via selected_releases) are always preserved.
func (s *Service) PruneStaleReleaseCandidates(ctx context.Context) (Result, error) {
	deleted, err := s.repo.PruneStaleReleaseCandidates(ctx, releaseCandidateRetention)
	result := Result{TaskName: "stale-release-candidates", DeletedRows: int(deleted)}
	if err != nil {
		return result, err
	}
	return result, s.repo.TouchMaintenanceCursor(ctx, result.TaskName, time.Now().UTC().Format(time.RFC3339))
}

// PruneOrphanedSelectedReleases deletes selected_releases rows that no
// queue_item points to anymore (candidates abandoned via a path that didn't
// clean up properly), freeing the downloaded content and DB rows they
// cascade to. Never touches a selected_release still backing an active
// symlink_publication.
func (s *Service) PruneOrphanedSelectedReleases(ctx context.Context) (Result, error) {
	deleted, err := s.repo.PruneOrphanedSelectedReleases(ctx, orphanedSelectedReleaseRetention)
	result := Result{TaskName: "orphaned-selected-releases", DeletedRows: int(deleted)}
	if err != nil {
		return result, err
	}
	return result, s.repo.TouchMaintenanceCursor(ctx, result.TaskName, time.Now().UTC().Format(time.RFC3339))
}

func (s *Service) RemoveOrphanedContent(ctx context.Context) (Result, error) {
	records, err := s.repo.ListSymlinkPublicationRecords(ctx)
	if err != nil {
		return Result{}, err
	}
	known := make(map[string]struct{}, len(records))
	for _, record := range records {
		known[filepath.Clean(record.LibraryPath)] = struct{}{}
	}
	result := Result{TaskName: "orphaned-content"}
	for _, root := range []string{s.runtime.MovieLibraryPath, s.runtime.TVLibraryPath} {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if info.Mode()&os.ModeSymlink == 0 {
				return nil
			}
			result.ScannedFiles++
			clean := filepath.Clean(path)
			if _, ok := known[clean]; ok {
				return nil
			}
			if err := os.Remove(clean); err == nil {
				result.DeletedFiles++
			}
			return nil
		})
		if err != nil {
			return result, err
		}
	}
	return result, s.repo.TouchMaintenanceCursor(ctx, result.TaskName, time.Now().UTC().Format(time.RFC3339))
}

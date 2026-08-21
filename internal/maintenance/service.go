// Package maintenance implements the background cleanup tasks that keep the
// published library and the database consistent with each other over time:
// removing symlinks left behind by deleted or moved content, reconciling
// orphaned filesystem entries with their database records, and pruning
// stale/orphaned rows so history tables don't grow unbounded.
package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/database"
)

// Repository defines the database operations required by the maintenance
// tasks: enumerating and deleting symlink publication records, recording
// task run cursors, and pruning stale/orphaned candidate and release rows.
type Repository interface {
	ListSymlinkPublicationRecords(ctx context.Context) ([]database.SymlinkPublicationRecord, error)
	DeleteSymlinkPublication(ctx context.Context, publicationID int64) error
	TouchMaintenanceCursor(ctx context.Context, taskName string, cursor string) error
	PruneStaleReleaseCandidates(ctx context.Context, olderThan time.Duration) (int64, error)
	PruneOrphanedSelectedReleases(ctx context.Context, olderThan time.Duration) (int64, error)
	RestoreNZBFileMessageIDs(ctx context.Context) (int64, error)
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

// orphanedContentGracePeriod protects a just-published symlink from
// RemoveOrphanedContent's stale-snapshot race: known publications are
// snapshotted once before the (potentially long) filesystem walk begins, so
// a symlink published after the snapshot but before the walk reaches its
// path would otherwise look orphaned and get deleted even though it's
// brand new. Skipping anything modified more recently than this window
// gives the next run's fresh snapshot time to catch up.
const orphanedContentGracePeriod = time.Hour

// Service runs the periodic library/database maintenance tasks: pruning
// broken or orphaned symlinks and their records, pruning stale search
// history, and pruning abandoned selected releases.
type Service struct {
	repo    Repository
	runtime config.Runtime
}

// Result summarizes a single maintenance task run for reporting/logging.
// Fields are populated selectively depending on which task produced the
// result; a task that doesn't scan/reset/repair anything simply leaves the
// corresponding field at zero.
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

// NewService creates a Service backed by repo, using runtime's library paths
// as the roots scanned by RemoveOrphanedContent.
func NewService(repo Repository, runtime config.Runtime) *Service {
	return &Service{repo: repo, runtime: runtime}
}

// RemoveBrokenMediaSymlinks scans every known symlink publication and
// deletes both the on-disk symlink and its database record when the symlink
// is missing/replaced by a non-symlink, or when its target is confirmed gone
// (e.g. the backing virtual content was removed). record.TargetPath lives
// under the rclone FUSE VFS mount (config.DefaultFuseMountPath), which can
// return transient errors (dir-cache reload, brief backend timeout) that are
// not proof the content is gone -- only os.IsNotExist is treated as genuine
// deletion; any other stat error is skipped and retried on the next run.
func (s *Service) RemoveBrokenMediaSymlinks(ctx context.Context) (Result, error) {
	records, err := s.repo.ListSymlinkPublicationRecords(ctx)
	if err != nil {
		return Result{}, err
	}
	return s.removeBrokenMediaSymlinks(ctx, records)
}

func (s *Service) removeBrokenMediaSymlinks(ctx context.Context, records []database.SymlinkPublicationRecord) (Result, error) {
	result := Result{TaskName: "broken-media-symlinks", ScannedRows: len(records)}
	for _, record := range records {
		info, err := os.Lstat(record.LibraryPath)
		if err != nil {
			if !os.IsNotExist(err) {
				continue
			}
			if err := s.repo.DeleteSymlinkPublication(ctx, record.ID); err != nil {
				return result, err
			}
			result.DeletedRows++
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			_ = os.Remove(record.LibraryPath)
			result.DeletedFiles++
			if err := s.repo.DeleteSymlinkPublication(ctx, record.ID); err != nil {
				return result, err
			}
			result.DeletedRows++
			continue
		}
		if _, err := os.Stat(record.TargetPath); err != nil {
			if !os.IsNotExist(err) {
				continue
			}
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

// RemoveOrphanedCompletedSymlinks deletes symlink_publication records whose
// on-disk symlink no longer exists at all (e.g. removed manually or by an
// external process), so the database doesn't keep tracking a publication
// that no longer has any corresponding library entry. Only a genuine
// os.IsNotExist counts as "no longer exists" -- any other Lstat error (e.g. a
// transient filesystem hiccup) is skipped and retried on the next run,
// mirroring the same fix already applied to its sibling
// RemoveBrokenMediaSymlinks, which ran back-to-back with this function
// (runStorageMaintenance, internal/app/app.go) and had been silently
// deleting rows for still-valid symlinks on any non-not-found stat error.
func (s *Service) RemoveOrphanedCompletedSymlinks(ctx context.Context) (Result, error) {
	records, err := s.repo.ListSymlinkPublicationRecords(ctx)
	if err != nil {
		return Result{}, err
	}
	return s.removeOrphanedCompletedSymlinks(ctx, records)
}

func (s *Service) removeOrphanedCompletedSymlinks(ctx context.Context, records []database.SymlinkPublicationRecord) (Result, error) {
	result := Result{TaskName: "orphaned-completed-symlinks", ScannedRows: len(records)}
	for _, record := range records {
		_, err := os.Lstat(record.LibraryPath)
		if err == nil {
			continue
		}
		if !os.IsNotExist(err) {
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

// RestoreNZBFileMessageIDs moves message IDs written in the temporary packed
// format back to the legacy text[] column used by the rest of the system.
func (s *Service) RestoreNZBFileMessageIDs(ctx context.Context) (Result, error) {
	updated, err := s.repo.RestoreNZBFileMessageIDs(ctx)
	result := Result{TaskName: "restore-nzb-message-ids", DeletedRows: int(updated)}
	if err != nil {
		return result, err
	}
	return result, s.repo.TouchMaintenanceCursor(ctx, result.TaskName, time.Now().UTC().Format(time.RFC3339))
}

// RemoveOrphanedContent walks the movie and TV library roots and removes any
// symlink not backed by a known symlink_publication record, cleaning up
// library entries left behind by a publication whose database record was
// already removed (or never created) through some other path.
func (s *Service) RemoveOrphanedContent(ctx context.Context) (Result, error) {
	records, err := s.repo.ListSymlinkPublicationRecords(ctx)
	if err != nil {
		return Result{}, err
	}
	return s.removeOrphanedContent(ctx, records)
}

func (s *Service) removeOrphanedContent(ctx context.Context, records []database.SymlinkPublicationRecord) (Result, error) {
	known := make(map[string]struct{}, len(records))
	for _, record := range records {
		known[filepath.Clean(record.LibraryPath)] = struct{}{}
	}
	walkStartedAt := time.Now()
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
			// A symlink published after `known` was snapshotted but before
			// the walk reached this path would otherwise look orphaned and
			// get deleted despite being brand new -- skip anything too
			// recent and let the next run's fresh snapshot judge it fairly.
			if walkStartedAt.Sub(info.ModTime()) < orphanedContentGracePeriod {
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

// RunSymlinkMaintenance runs RemoveBrokenMediaSymlinks,
// RemoveOrphanedCompletedSymlinks, and RemoveOrphanedContent against a
// single shared ListSymlinkPublicationRecords fetch, instead of each
// independently re-querying the entire symlink_publications table.
//
// Confirmed live: runStorageMaintenance (internal/app/app.go) called these
// three back to back every 6h, each issuing its own full-table query over
// what can be an ~11,100-row table (the same library size cited in the
// internal/dav cache-TTL fix) purely to build the same "known paths" lookup
// three times over. Continues past an error from an earlier pass so a later
// pass still gets its chance to run, mirroring how these three already ran
// independently before this fix -- one pass's failure was never a reason to
// skip the others.
func (s *Service) RunSymlinkMaintenance(ctx context.Context) (broken, orphanedCompleted, orphanedContent Result, err error) {
	records, err := s.repo.ListSymlinkPublicationRecords(ctx)
	if err != nil {
		return Result{}, Result{}, Result{}, err
	}
	var brokenErr, orphanedCompletedErr, orphanedContentErr error
	broken, brokenErr = s.removeBrokenMediaSymlinks(ctx, records)
	orphanedCompleted, orphanedCompletedErr = s.removeOrphanedCompletedSymlinks(ctx, records)
	orphanedContent, orphanedContentErr = s.removeOrphanedContent(ctx, records)
	err = errors.Join(brokenErr, orphanedCompletedErr, orphanedContentErr)
	return broken, orphanedCompleted, orphanedContent, err
}

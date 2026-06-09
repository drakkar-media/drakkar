package library

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/hjongedijk/drakkar/internal/config"
	"github.com/hjongedijk/drakkar/internal/database"
	"github.com/hjongedijk/drakkar/internal/metrics"
	"github.com/hjongedijk/drakkar/internal/symlink"
)

var ErrNoVirtualFiles = errors.New("selected release has no publishable virtual files")

type Repository interface {
	ListVirtualFilesForRelease(ctx context.Context, selectedReleaseID int64) ([]database.ReleaseVirtualFile, error)
	ListSelectedReleasesForPublication(ctx context.Context) ([]int64, error)
	ListSelectedReleasesByLibraryItem(ctx context.Context, libraryItemID int64) ([]int64, error)
	ListPendingRepublishTargets(ctx context.Context) ([]database.PendingRepublishTarget, error)
	UpsertSymlinkPublication(ctx context.Context, libraryItemID, virtualFileID int64, libraryPath, targetPath string) error
	MarkReleaseAvailable(ctx context.Context, selectedReleaseID int64) error
	FindSeasonPackMatches(ctx context.Context, selectedReleaseID, triggeringLibraryItemID int64) ([]database.SeasonPackEpisodeMatch, error)
	FulfillEpisodeLibraryItem(ctx context.Context, libraryItemID, sourceSelectedReleaseID, virtualFileID int64) error
	CreateSeasonPackEpisodeItems(ctx context.Context, selectedReleaseID, triggeringLibraryItemID int64) error
}

type Publisher struct {
	repo            Repository
	syml            *symlink.Publisher
	runtime         config.Runtime
	postPublishHook func(context.Context, int64) error
}

type BulkRepublishResult struct {
	Processed        int     `json:"processed"`
	Republished      int     `json:"republished"`
	Failed           int     `json:"failed"`
	ProcessedLibrary []int64 `json:"processedLibrary,omitempty"`
	FailedLibrary    []int64 `json:"failedLibrary,omitempty"`
}

func NewPublisher(repo Repository, runtime config.Runtime) *Publisher {
	return &Publisher{
		repo:    repo,
		syml:    symlink.NewPublisher(),
		runtime: runtime,
	}
}

func (p *Publisher) SetPostPublishHook(fn func(context.Context, int64) error) {
	p.postPublishHook = fn
}

// PublishSelectedRelease publishes virtual files for a selected release.
// isNew should be true for fresh publishes (creates per-episode items for season
// packs) and false for startup rebuilds (skip redundant episode item creation).
func (p *Publisher) publishSelectedRelease(ctx context.Context, selectedReleaseID int64, isNew bool) error {
	files, err := p.repo.ListVirtualFilesForRelease(ctx, selectedReleaseID)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return ErrNoVirtualFiles
	}
	libraryItemIDs := make(map[int64]struct{})
	for _, file := range files {
		libraryPath := p.libraryPathFor(file)
		if libraryPath == "" {
			slog.Warn("skipping symlink: insufficient metadata", "virtual_file_id", file.VirtualFileID, "file", file.FileName)
		} else {
			// Symlink to the rclone VFS mount — same as nzbdav symlink mode for Plex.
			// rclone mounts Drakkar's WebDAV at RcloneMountPath; the file is at
			// content/{virtualFileID}/{filename} within that mount.
			target := filepath.Join(p.runtime.RcloneMountPath, "content", fmt.Sprintf("%d", file.VirtualFileID), file.FileName)
			if err := p.syml.Publish(libraryPath, target); err != nil {
				slog.Warn("symlink publish failed", "path", libraryPath, "err", err)
			} else if err := p.repo.UpsertSymlinkPublication(ctx, file.LibraryItemID, file.VirtualFileID, libraryPath, target); err != nil {
				return err
			}
		}
		libraryItemIDs[file.LibraryItemID] = struct{}{}
	}
	// Only call the post-publish hook (subtitle search/publish) for new publications.
	// During startup RebuildPublications, subtitles are already in place.
	if isNew && p.postPublishHook != nil {
		for libraryItemID := range libraryItemIDs {
			if err := p.postPublishHook(ctx, libraryItemID); err != nil {
				return err
			}
		}
	}
	metrics.M.PublishedVirtualFiles.Add(int64(len(files)))
	if err := p.repo.MarkReleaseAvailable(ctx, selectedReleaseID); err != nil {
		return err
	}
	// For season packs: fulfil any other episode library items that are covered
	// by virtual files in this release but were searched as separate items.
	// Also create per-episode library items for whole-show imports (season=0/episode=0).
	// Skip during startup rebuild — episode items already exist from the initial publish.
	if isNew && len(libraryItemIDs) == 1 {
		for triggeringID := range libraryItemIDs {
			p.fulfillSeasonPackEpisodes(ctx, selectedReleaseID, triggeringID, files)
			if err := p.repo.CreateSeasonPackEpisodeItems(ctx, selectedReleaseID, triggeringID); err != nil {
				_ = err // non-fatal
			}
		}
	}
	return nil
}

// PublishSelectedRelease publishes a new release (creates per-episode items for season packs).
func (p *Publisher) PublishSelectedRelease(ctx context.Context, selectedReleaseID int64) error {
	return p.publishSelectedRelease(ctx, selectedReleaseID, true)
}

func (p *Publisher) RebuildPublications(ctx context.Context) error {
	selectedReleaseIDs, err := p.repo.ListSelectedReleasesForPublication(ctx)
	if err != nil {
		return err
	}
	for _, selectedReleaseID := range selectedReleaseIDs {
		if err := p.publishSelectedRelease(ctx, selectedReleaseID, false); err != nil {
			slog.Warn("rebuild: publish failed", "selected_release_id", selectedReleaseID, "err", err)
		}
	}
	return nil
}

func (p *Publisher) RepublishLibraryItem(ctx context.Context, libraryItemID int64) error {
	selectedReleaseIDs, err := p.repo.ListSelectedReleasesByLibraryItem(ctx, libraryItemID)
	if err != nil {
		return err
	}
	for _, selectedReleaseID := range selectedReleaseIDs {
		if err := p.PublishSelectedRelease(ctx, selectedReleaseID); err != nil {
			return err
		}
	}
	return nil
}

func (p *Publisher) RepublishPendingLibrary(ctx context.Context) (BulkRepublishResult, error) {
	targets, err := p.repo.ListPendingRepublishTargets(ctx)
	if err != nil {
		return BulkRepublishResult{}, err
	}
	result := BulkRepublishResult{Processed: len(targets)}
	for _, target := range targets {
		result.ProcessedLibrary = append(result.ProcessedLibrary, target.LibraryItemID)
		if err := p.RepublishLibraryItem(ctx, target.LibraryItemID); err != nil {
			result.Failed++
			result.FailedLibrary = append(result.FailedLibrary, target.LibraryItemID)
			continue
		}
		result.Republished++
	}
	return result, nil
}

// fulfillSeasonPackEpisodes matches virtual files in a season pack to their
// individual episode library items and marks each one as available.
// This runs after a season pack is published so all episodes are fulfilled
// without each needing its own separate NZB download.
func (p *Publisher) fulfillSeasonPackEpisodes(ctx context.Context, selectedReleaseID, triggeringLibraryItemID int64, files []database.ReleaseVirtualFile) {
	matches, err := p.repo.FindSeasonPackMatches(ctx, selectedReleaseID, triggeringLibraryItemID)
	if err != nil || len(matches) == 0 {
		return
	}
	// Build a fast lookup: (season, episode) → virtual file
	type epKey struct{ season, episode int }
	fileByEpisode := map[epKey]database.ReleaseVirtualFile{}
	for _, f := range files {
		s, e := database.ParseEpisodeFromFilename(f.FileName)
		if s > 0 && e > 0 {
			fileByEpisode[epKey{s, e}] = f
		}
	}

	for _, m := range matches {
		vf, ok := fileByEpisode[epKey{m.SeasonNumber, m.EpisodeNumber}]
		if !ok {
			vf.VirtualFileID = m.VirtualFileID
		}
		// Publish the host symlink for this episode using its proper library item metadata.
		// We reuse the existing virtual file — no new NNTP fetching needed.
		enriched := database.ReleaseVirtualFile{
			VirtualFileID:     vf.VirtualFileID,
			SelectedReleaseID: selectedReleaseID,
			LibraryItemID:     m.LibraryItemID,
			MediaType:         "episode",
			Path:              m.VirtualFilePath,
			FileName:          m.FileName,
		}
		// Attempt to get episode-specific metadata via DB (show title, tvdb, season, episode).
		if meta, metaErr := p.repo.ListVirtualFilesForRelease(ctx, selectedReleaseID); metaErr == nil {
			for _, mf := range meta {
				if mf.VirtualFileID == vf.VirtualFileID && mf.ShowTitle != "" {
					enriched.ShowTitle = mf.ShowTitle
					enriched.ShowYear = mf.ShowYear
					enriched.ShowTVDBID = mf.ShowTVDBID
					enriched.SeasonNumber = m.SeasonNumber
					enriched.EpisodeNumber = m.EpisodeNumber
					break
				}
			}
		}
		libraryPath := p.libraryPathFor(enriched)
		if libraryPath != "" {
			target := filepath.Join(p.runtime.RcloneMountPath, "content", fmt.Sprintf("%d", m.VirtualFileID), enriched.FileName)
			if symlinkErr := p.syml.Publish(libraryPath, target); symlinkErr == nil {
				_ = p.repo.UpsertSymlinkPublication(ctx, m.LibraryItemID, m.VirtualFileID, libraryPath, target)
			}
		}
		_ = p.repo.FulfillEpisodeLibraryItem(ctx, m.LibraryItemID, selectedReleaseID, m.VirtualFileID)
		slog.Info("season pack: fulfilled episode",
			"library_item_id", m.LibraryItemID,
			"season", m.SeasonNumber, "episode", m.EpisodeNumber,
			"file", m.FileName)
	}
}

func (p *Publisher) libraryPathFor(file database.ReleaseVirtualFile) string {
	switch strings.ToLower(file.MediaType) {
	case "movie":
		if file.MovieTitle != "" {
			return symlink.MoviePath(p.runtime.MovieLibraryPath, file.MovieTitle, file.MovieYear, int(file.MovieTMDBID), file.FileName)
		}
	case "episode", "tv":
		season := file.SeasonNumber
		episode := file.EpisodeNumber
		if (season <= 0 || episode <= 0) && file.FileName != "" {
			season, episode = database.ParseEpisodeFromFilename(file.FileName)
		}
		if file.ShowTitle != "" && season > 0 && episode > 0 {
			return symlink.EpisodePath(p.runtime.TVLibraryPath, file.ShowTitle, file.ShowYear, int(file.ShowTVDBID), season, episode, file.FileName)
		}
	}
	return ""
}

func CompletedTargetVirtualPath(selectedReleaseID int64, fileName string) string {
	return filepath.Join("/content", "releases", fmt.Sprintf("%d", selectedReleaseID), fileName)
}

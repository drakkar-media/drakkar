package library

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/metrics"
	"github.com/drakkar-media/drakkar/internal/rclone"
	"github.com/drakkar-media/drakkar/internal/symlink"
)

var ErrNoVirtualFiles = errors.New("selected release has no publishable virtual files")

type Repository interface {
	ListVirtualFilesForRelease(ctx context.Context, selectedReleaseID int64) ([]database.ReleaseVirtualFile, error)
	ListSelectedReleasesForPublication(ctx context.Context) ([]int64, error)
	ListSelectedReleasesByLibraryItem(ctx context.Context, libraryItemID int64) ([]int64, error)
	FindSourceSelectedReleaseForItem(ctx context.Context, libraryItemID int64) (int64, error)
	GetEpisodeMetadataForLibraryItem(ctx context.Context, libraryItemID int64) (database.EpisodeMetadata, error)
	ListPendingRepublishTargets(ctx context.Context) ([]database.PendingRepublishTarget, error)
	UpsertSymlinkPublication(ctx context.Context, libraryItemID, virtualFileID int64, libraryPath, targetPath string) error
	MarkReleaseAvailable(ctx context.Context, selectedReleaseID int64) error
	FindSeasonPackMatches(ctx context.Context, selectedReleaseID, triggeringLibraryItemID int64) ([]database.SeasonPackEpisodeMatch, error)
	FulfillEpisodeLibraryItem(ctx context.Context, libraryItemID, sourceSelectedReleaseID, virtualFileID int64) error
	CreateSeasonPackEpisodeItems(ctx context.Context, selectedReleaseID, triggeringLibraryItemID int64) error
}

type Publisher struct {
	repo                  Repository
	runtime               config.Runtime
	syml                  *symlink.Publisher
	rclone                *rclone.Client
	postPublishHook       func(context.Context, int64) error
	mediaServerNotifyHook func(context.Context, int64) error
}

type BulkRepublishResult struct {
	Processed        int     `json:"processed"`
	Republished      int     `json:"republished"`
	Failed           int     `json:"failed"`
	ProcessedLibrary []int64 `json:"processedLibrary,omitempty"`
	FailedLibrary    []int64 `json:"failedLibrary,omitempty"`
}

func NewPublisher(repo Repository, runtime config.Runtime, rcloneRCAddr string) *Publisher {
	return &Publisher{
		repo:    repo,
		runtime: runtime,
		syml:    symlink.NewPublisher(),
		rclone:  rclone.NewClient(rcloneRCAddr),
	}
}

func (p *Publisher) SetPostPublishHook(fn func(context.Context, int64) error) {
	p.postPublishHook = fn
}

// SetMediaServerNotifyHook registers a callback that only refreshes media
// server (Plex/Jellyfin) libraries -- unlike postPublishHook it does not run
// subtitle search, so it's safe to fire on repair/republish passes as well as
// fresh publishes, without redundantly re-triggering subtitle work.
func (p *Publisher) SetMediaServerNotifyHook(fn func(context.Context, int64) error) {
	p.mediaServerNotifyHook = fn
}

// PublishSelectedRelease publishes virtual files for a selected release.
// isNew should be true for fresh publishes (creates per-episode items for season
// packs) and false for startup rebuilds (skip redundant episode item creation).
// notifyMediaServers requests a Plex/Jellyfin refresh even when isNew is
// false -- for targeted repair republishes (RepublishLibraryItem), not the
// full startup RebuildPublications sweep, which would otherwise hammer the
// media server with a refresh per already-known item on every restart.
func (p *Publisher) publishSelectedRelease(ctx context.Context, selectedReleaseID int64, isNew bool, notifyMediaServers bool) error {
	files, err := p.repo.ListVirtualFilesForRelease(ctx, selectedReleaseID)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return ErrNoVirtualFiles
	}
	libraryItemIDs := make(map[int64]struct{})
	for _, file := range files {
		// Season-pack guard: when a specific episode is expected (season > 0,
		// episode > 0) and the pack contains multiple files, only publish the file
		// whose filename parses to the expected episode. The other files are handled
		// by fulfillSeasonPackEpisodes. Without this guard every file in the pack
		// overwrites the same library path (e.g. S01E01.mkv) and the last file
		// alphabetically wins — typically the season finale.
		if strings.EqualFold(file.MediaType, "episode") &&
			file.SeasonNumber > 0 && file.EpisodeNumber > 0 && len(files) > 1 {
			fs, fe := database.ParseEpisodeFromFilename(file.FileName)
			if fs > 0 && fe > 0 && (fs != file.SeasonNumber || fe != file.EpisodeNumber) {
				slog.Debug("publish: skipping file — belongs to different episode",
					"file", file.FileName, "expectedSeason", file.SeasonNumber, "expectedEpisode", file.EpisodeNumber,
					"parsedSeason", fs, "parsedEpisode", fe)
				libraryItemIDs[file.LibraryItemID] = struct{}{}
				continue
			}
		}
		target := filepath.Join(p.runtime.FuseMountPath, "content", file.Path)
		libraryPath := p.libraryPathFor(file)
		if libraryPath == "" {
			slog.Debug("skipping host symlink: insufficient metadata", "virtual_file_id", file.VirtualFileID, "file", file.FileName)
		} else {
			if err := p.syml.Publish(libraryPath, target); err != nil {
				return err
			}
			if err := p.repo.UpsertSymlinkPublication(ctx, file.LibraryItemID, file.VirtualFileID, libraryPath, target); err != nil {
				return err
			}
			slog.Debug("publish: symlink published", "libraryPath", libraryPath, "target", target, "virtualFileId", file.VirtualFileID)
			_ = p.rclone.RefreshPath(ctx, filepath.Dir(libraryPath))
			// Also refresh the content directory the symlink points into —
			// rclone's VFS caches that subtree independently of the library
			// directory. Without this, a health check reading straight from
			// the content path right after publish could see a stale/empty
			// cached view and wrongly report the file as corrupt.
			//
			// Deliberately NOT also refreshing the "releases" parent
			// directory here: it lists every release in the library
			// (9,000+), and golang.org/x/net/webdav's PROPFIND handler Stats
			// every child to build the response regardless of requested
			// depth, so refreshing "releases" costs one DB query per release
			// every time -- confirmed live at ~8-11s and heavy sustained
			// CPU/DB load per call, enough to degrade Plex playback when it
			// ran on every publish.
			_ = p.rclone.RefreshMountPath(ctx, p.runtime.FuseMountPath, filepath.Dir(target))
		}
		libraryItemIDs[file.LibraryItemID] = struct{}{}
	}
	// Only call the post-publish hook (subtitle search/publish) for new publications.
	// During startup RebuildPublications, subtitles are already in place.
	// A targeted repair republish (notifyMediaServers) skips subtitle search
	// too, but still needs the media server told about the fixed symlink --
	// that's the narrower mediaServerNotifyHook below.
	shouldNotifyMediaServers := isNew || notifyMediaServers
	if isNew && p.postPublishHook != nil {
		for libraryItemID := range libraryItemIDs {
			if err := p.postPublishHook(ctx, libraryItemID); err != nil {
				return err
			}
		}
	} else if notifyMediaServers && p.mediaServerNotifyHook != nil {
		for libraryItemID := range libraryItemIDs {
			if err := p.mediaServerNotifyHook(ctx, libraryItemID); err != nil {
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
	// Runs on rebuild too — fills in symlinks for episodes created after the
	// initial publish (e.g. by CreateSeasonPackEpisodeItems).
	if len(libraryItemIDs) == 1 {
		for triggeringID := range libraryItemIDs {
			p.fulfillSeasonPackEpisodes(ctx, selectedReleaseID, triggeringID, files, shouldNotifyMediaServers)
		}
	}
	// Create per-episode library items for whole-show imports. Skip on rebuild —
	// those items were already created on the initial publish.
	if isNew && len(libraryItemIDs) == 1 {
		for triggeringID := range libraryItemIDs {
			if err := p.repo.CreateSeasonPackEpisodeItems(ctx, selectedReleaseID, triggeringID); err != nil {
				slog.Warn("publish: failed to create season pack episode items", "library_item_id", triggeringID, "err", err)
			}
			p.fulfillSeasonPackEpisodes(ctx, selectedReleaseID, triggeringID, files, shouldNotifyMediaServers)
		}
	}
	return nil
}

// PublishSelectedRelease publishes a new release (creates per-episode items for season packs).
func (p *Publisher) PublishSelectedRelease(ctx context.Context, selectedReleaseID int64) error {
	return p.publishSelectedRelease(ctx, selectedReleaseID, true, false)
}

func (p *Publisher) RebuildPublications(ctx context.Context) error {
	selectedReleaseIDs, err := p.repo.ListSelectedReleasesForPublication(ctx)
	if err != nil {
		return err
	}
	for _, selectedReleaseID := range selectedReleaseIDs {
		// isNew=false: skip per-episode item creation; those were already done on
		// initial publish. notifyMediaServers=false: this runs at startup for every
		// pending release -- the media server already knows about all of these from
		// before the restart, so re-notifying would just hammer it on every restart.
		if err := p.publishSelectedRelease(ctx, selectedReleaseID, false, false); err != nil {
			return err
		}
	}
	return nil
}

func (p *Publisher) RepublishLibraryItem(ctx context.Context, libraryItemID int64) error {
	selectedReleaseIDs, err := p.repo.ListSelectedReleasesByLibraryItem(ctx, libraryItemID)
	if err != nil {
		return err
	}
	if len(selectedReleaseIDs) == 0 {
		// Season-pack episode: virtual files live under the pack's selected release.
		// Rather than re-publishing the whole pack (which may have no show metadata),
		// use the episode item's own metadata to find and publish the matching VF directly.
		sourceID, err := p.repo.FindSourceSelectedReleaseForItem(ctx, libraryItemID)
		if err != nil || sourceID == 0 {
			return nil
		}
		return p.republishEpisodeFromSourceRelease(ctx, libraryItemID, sourceID)
	}
	for _, selectedReleaseID := range selectedReleaseIDs {
		// notifyMediaServers=true: this is a targeted repair of an item whose
		// symlink was missing or stale -- the media server needs to be told,
		// since it never learned about the correct file otherwise.
		if err := p.publishSelectedRelease(ctx, selectedReleaseID, false, true); err != nil {
			return err
		}
	}
	return nil
}

// republishEpisodeFromSourceRelease creates the missing symlink for an episode
// library item whose virtual files live under a season pack selected release.
// It queries the episode item's own metadata (show title, season, episode) and
// matches the corresponding virtual file by parsing the filename.
func (p *Publisher) republishEpisodeFromSourceRelease(ctx context.Context, libraryItemID, sourceReleaseID int64) error {
	meta, err := p.repo.GetEpisodeMetadataForLibraryItem(ctx, libraryItemID)
	if err != nil || meta.ShowTitle == "" || meta.SeasonNumber <= 0 || meta.EpisodeNumber <= 0 {
		return nil
	}
	files, err := p.repo.ListVirtualFilesForRelease(ctx, sourceReleaseID)
	if err != nil {
		return err
	}
	for _, f := range files {
		s, e := database.ParseEpisodeFromFilename(f.FileName)
		if s != meta.SeasonNumber || e != meta.EpisodeNumber {
			continue
		}
		enriched := f
		enriched.LibraryItemID = libraryItemID
		enriched.MediaType = "episode"
		enriched.ShowTitle = meta.ShowTitle
		enriched.ShowYear = meta.ShowYear
		enriched.ShowTVDBID = meta.ShowTVDBID
		enriched.SeasonNumber = meta.SeasonNumber
		enriched.EpisodeNumber = meta.EpisodeNumber
		target := filepath.Join(p.runtime.FuseMountPath, "content", enriched.Path)
		libraryPath := p.libraryPathFor(enriched)
		if libraryPath == "" {
			return nil
		}
		if err := p.syml.Publish(libraryPath, target); err != nil {
			return err
		}
		if err := p.repo.UpsertSymlinkPublication(ctx, libraryItemID, f.VirtualFileID, libraryPath, target); err != nil {
			return err
		}
		_ = p.rclone.RefreshPath(ctx, filepath.Dir(libraryPath))
		// Also refresh the content directory the symlink points into -- the
		// two sibling publish paths in this file (publishSelectedRelease,
		// fulfillSeasonPackEpisodes) already do this; this one (season-pack
		// episode borrowing a virtual file from the pack's source release)
		// was missed, found in the 2026-07-19 audit.
		_ = p.rclone.RefreshMountPath(ctx, p.runtime.FuseMountPath, filepath.Dir(target))
		if p.mediaServerNotifyHook != nil {
			if err := p.mediaServerNotifyHook(ctx, libraryItemID); err != nil {
				return err
			}
		}
		return nil
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
			slog.Debug("republish pending: item failed", "library_item_id", target.LibraryItemID, "err", err)
			result.Failed++
			result.FailedLibrary = append(result.FailedLibrary, target.LibraryItemID)
			continue
		}
		result.Republished++
	}
	slog.Info("republish pending: done", "processed", result.Processed, "republished", result.Republished, "failed", result.Failed)
	return result, nil
}

// fulfillSeasonPackEpisodes matches virtual files in a season pack to their
// individual episode library items and marks each one as available.
// This runs after a season pack is published so all episodes are fulfilled
// without each needing its own separate NZB download. notifyMediaServers
// mirrors the caller's own notify decision (true for fresh publishes and
// targeted repairs, false for the startup RebuildPublications sweep) --
// without it, sibling episodes fulfilled here never got a Plex/Jellyfin
// refresh of their own at all, since only the triggering episode's path was
// ever passed to the media-server hook.
func (p *Publisher) fulfillSeasonPackEpisodes(ctx context.Context, selectedReleaseID, triggeringLibraryItemID int64, files []database.ReleaseVirtualFile, notifyMediaServers bool) {
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
		virtualFileID := m.VirtualFileID
		if ok {
			virtualFileID = vf.VirtualFileID
		}
		// Prefer the file path from fileByEpisode (ordered by vf.path, so deterministic
		// and alphabetically last — proper "ShowName.SxxExx.Title.mkv" files sort after
		// bonus content like "Behind The Story - SxxExx.mkv"). Fall back to m.VirtualFilePath
		// only when fileByEpisode had no entry for this episode.
		enrichedPath := m.VirtualFilePath
		enrichedFileName := m.FileName
		if ok && vf.Path != "" {
			enrichedPath = vf.Path
			enrichedFileName = vf.FileName
		}
		// Publish the host symlink for this episode using its proper library item metadata.
		// We reuse the existing virtual file — no new NNTP fetching needed.
		enriched := database.ReleaseVirtualFile{
			VirtualFileID:     virtualFileID,
			SelectedReleaseID: selectedReleaseID,
			LibraryItemID:     m.LibraryItemID,
			MediaType:         "episode",
			Path:              enrichedPath,
			FileName:          enrichedFileName,
		}
		if meta, metaErr := p.repo.GetEpisodeMetadataForLibraryItem(ctx, m.LibraryItemID); metaErr == nil {
			enriched.ShowTitle = meta.ShowTitle
			enriched.ShowYear = meta.ShowYear
			enriched.ShowTVDBID = meta.ShowTVDBID
			enriched.SeasonNumber = meta.SeasonNumber
			enriched.EpisodeNumber = meta.EpisodeNumber
		}
		if enriched.SeasonNumber <= 0 || enriched.EpisodeNumber <= 0 {
			enriched.SeasonNumber = m.SeasonNumber
			enriched.EpisodeNumber = m.EpisodeNumber
		}
		target := filepath.Join(p.runtime.FuseMountPath, "content", enriched.Path)
		libraryPath := p.libraryPathFor(enriched)
		if libraryPath != "" {
			if symlinkErr := p.syml.Publish(libraryPath, target); symlinkErr == nil {
				if upsertErr := p.repo.UpsertSymlinkPublication(ctx, m.LibraryItemID, virtualFileID, libraryPath, target); upsertErr == nil {
					_ = p.rclone.RefreshPath(ctx, filepath.Dir(libraryPath))
					// Also refresh the content directory the symlink points into
					// (see the comment in publishSelectedRelease above for why the
					// "releases" parent directory is deliberately NOT refreshed too).
					_ = p.rclone.RefreshMountPath(ctx, p.runtime.FuseMountPath, filepath.Dir(target))
					if notifyMediaServers && p.mediaServerNotifyHook != nil {
						if err := p.mediaServerNotifyHook(ctx, m.LibraryItemID); err != nil {
							slog.Warn("season pack: media server notify failed", "library_item_id", m.LibraryItemID, "err", err)
						}
					}
				}
			}
		}
		if err := p.repo.FulfillEpisodeLibraryItem(ctx, m.LibraryItemID, selectedReleaseID, virtualFileID); err != nil {
			slog.Warn("season pack: failed to fulfill episode library item", "library_item_id", m.LibraryItemID, "err", err)
			continue
		}
		slog.Debug("season pack: fulfilled episode",
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
		// Season packs: the library item may have season=0/episode=0 when imported
		// as a whole-show request. Fall back to parsing the filename.
		if (season <= 0 || episode <= 0) && file.FileName != "" {
			season, episode = database.ParseEpisodeFromFilename(file.FileName)
		}
		if file.ShowTitle != "" && season > 0 && episode > 0 {
			return symlink.EpisodePath(
				p.runtime.TVLibraryPath,
				file.ShowTitle,
				file.ShowYear,
				int(file.ShowTVDBID),
				season,
				episode,
				file.FileName,
			)
		}
	}
	return ""
}

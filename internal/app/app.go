package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	// registers /debug/pprof/* on http.DefaultServeMux; only ever served on
	// the loopback-only debugServer below, never on a publicly reachable port.
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/drakkar-media/drakkar/internal/api"
	"github.com/drakkar-media/drakkar/internal/blocklist"
	"github.com/drakkar-media/drakkar/internal/cache"
	"github.com/drakkar-media/drakkar/internal/catalog"
	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/dav"
	"github.com/drakkar-media/drakkar/internal/hydra"
	"github.com/drakkar-media/drakkar/internal/jellyfin"
	"github.com/drakkar-media/drakkar/internal/library"
	"github.com/drakkar-media/drakkar/internal/maintenance"
	"github.com/drakkar-media/drakkar/internal/metrics"
	"github.com/drakkar-media/drakkar/internal/nntp"
	"github.com/drakkar-media/drakkar/internal/notifications"
	"github.com/drakkar-media/drakkar/internal/nzb"
	"github.com/drakkar-media/drakkar/internal/observability"
	"github.com/drakkar-media/drakkar/internal/opensubtitles"
	"github.com/drakkar-media/drakkar/internal/plex"
	"github.com/drakkar-media/drakkar/internal/policy"
	"github.com/drakkar-media/drakkar/internal/probe"
	"github.com/drakkar-media/drakkar/internal/queue"
	"github.com/drakkar-media/drakkar/internal/rclone"
	"github.com/drakkar-media/drakkar/internal/seerr"
	"github.com/drakkar-media/drakkar/internal/stream"
	"github.com/drakkar-media/drakkar/internal/subdl"
	"github.com/drakkar-media/drakkar/internal/subtitles"
	"github.com/drakkar-media/drakkar/internal/tmdb"
	"github.com/drakkar-media/drakkar/internal/tvdb"
	"github.com/drakkar-media/drakkar/internal/workflow"
	"github.com/drakkar-media/drakkar/internal/yenc"
	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
	"github.com/rs/zerolog"
)

const (
	maintenanceRecentTVTask    = "hydra_recent_tv"
	maintenanceRecentMovieTask = "hydra_recent_movie"
	taskSeerrSync              = "seerr_sync"
	taskPendingQueuePush       = "pending_queue_push"
	taskQueueHousekeeping      = "queue_housekeeping"     // merged: stale-queue-reset + retry_failed_queue
	taskPublishingMaintenance  = "publishing_maintenance" // merged: republish_pending + reset_orphaned_available
	taskHealthCheck            = "health_check"
	taskNZBHealthCheck         = "nzb_health_check"
	taskStorageMaintenance     = "storage_maintenance" // merged: cache_prune + library-cleanup
	taskContentMaintenance     = "content_maintenance" // merged: fill_missing_episodes + search_upgrades
	taskSyncPlexDetected       = "sync_plex_detected"
	taskArticleHealthCheck     = "article_health_check"
	taskBacklogSearch          = "backlog_search"
)

const (
	backgroundHealthCheckInterval = 15 * time.Minute
	backgroundDeepHealthBatchSize = 48
	pendingQueueDispatchInterval  = 30 * time.Second
	nonCriticalBacklogThreshold   = 1000
	nonCriticalQueueDepthLimit    = 500
)

func boundedTVRSSInterval(minutes int) time.Duration {
	interval := time.Duration(minutes) * time.Minute
	if interval < 15*time.Minute {
		return 15 * time.Minute
	}
	return interval
}

func boundedMovieRSSInterval(minutes int) time.Duration {
	interval := time.Duration(minutes) * time.Minute
	if interval < 30*time.Minute {
		return 30 * time.Minute
	}
	return interval
}

// refreshPlexPathWithRetry retries a transient Plex refresh failure (a brief
// restart, network blip) a couple of times with a short backoff, rather than
// leaving the item permanently unnoticed by Plex on the first hiccup -- no
// other mechanism ever retries this refresh on the caller's behalf.
func refreshPlexPathWithRetry(ctx context.Context, client *plex.Client, sectionKey, dir string) error {
	const maxAttempts = 3
	delay := 500 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = client.RefreshPathAuto(ctx, sectionKey, dir)
		if lastErr == nil {
			return nil
		}
		if attempt == maxAttempts {
			break
		}
		select {
		case <-time.After(delay):
			delay *= 2
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return lastErr
}

type runtimeStatus struct {
	mu     sync.RWMutex
	status api.Status
}

type fileSettingsService struct {
	path    string
	mu      sync.Mutex
	applier interface {
		ApplySettings(ctx context.Context, cfg config.Settings) error
	}
}

func (s *fileSettingsService) GetSettings(_ context.Context) (config.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return config.Load(s.path)
}

func (s *fileSettingsService) UpdateSettings(_ context.Context, cfg config.Settings) (config.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := config.Load(s.path)
	if err != nil {
		return config.Settings{}, err
	}
	merged := config.MergeSecrets(current, cfg)
	if err := config.Save(s.path, merged); err != nil {
		return config.Settings{}, err
	}
	loaded, err := config.Load(s.path)
	if err != nil {
		return config.Settings{}, err
	}
	if s.applier != nil {
		if err := s.applier.ApplySettings(context.Background(), loaded); err != nil {
			return config.Settings{}, err
		}
	}
	return loaded, nil
}

type taskScheduleStatusService struct {
	mu                          sync.RWMutex
	db                          *database.DB
	tvRssSyncIntervalMinutes    int
	movieRssSyncIntervalMinutes int
}

func (s *runtimeStatus) Status() api.Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *runtimeStatus) SetStatus(status api.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

func (s *taskScheduleStatusService) ListTaskSchedules(ctx context.Context) ([]api.TaskSchedule, error) {
	s.mu.RLock()
	tvRSSInterval := s.tvRssSyncIntervalMinutes
	movieRSSInterval := s.movieRssSyncIntervalMinutes
	s.mu.RUnlock()
	cursorRows, err := s.db.ListMaintenanceCursors(ctx)
	if err != nil {
		return nil, err
	}
	lastRuns := make(map[string]time.Time, len(cursorRows))
	for _, row := range cursorRows {
		if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(row.Cursor)); err == nil {
			lastRuns[row.TaskName] = ts
			continue
		}
		lastRuns[row.TaskName] = row.UpdatedAt
	}
	defs := []api.TaskSchedule{
		{ID: taskSeerrSync, Label: "Sync Seerr Requests", Group: "Indexing", Interval: "10m", Automated: true, LastRunState: "idle"},
		{ID: taskPendingQueuePush, Label: "Dispatch Pending Queue", Group: "Indexing", Interval: "30s", Automated: true, LastRunState: "idle"},
		{ID: maintenanceRecentTVTask, Label: "Recent TV Feed", Group: "Indexing", Interval: fmt.Sprintf("%dm", tvRSSInterval), Automated: true, LastRunState: "idle"},
		{ID: maintenanceRecentMovieTask, Label: "Recent Movie Feed", Group: "Indexing", Interval: fmt.Sprintf("%dm", movieRSSInterval), Automated: true, LastRunState: "idle"},
		{ID: taskQueueHousekeeping, Label: "Queue Housekeeping", Group: "Indexing", Interval: "10m", Automated: true, LastRunState: "idle"},
		{ID: taskPublishingMaintenance, Label: "Publishing Maintenance", Group: "Publishing", Interval: "30m", Automated: true, LastRunState: "idle"},
		{ID: taskHealthCheck, Label: "Run Health Check", Group: "Maintenance", Interval: "15m", Automated: true, LastRunState: "idle"},
		{ID: taskNZBHealthCheck, Label: "Deep NZB Article Check", Group: "Maintenance", Interval: "168h", Automated: true, LastRunState: "idle"},
		{ID: taskArticleHealthCheck, Label: "Article Health Check", Group: "Maintenance", Interval: "6h", Automated: true, LastRunState: "idle"},
		{ID: taskStorageMaintenance, Label: "Storage Maintenance", Group: "Maintenance", Interval: "6h", Automated: true, LastRunState: "idle"},
		{ID: taskContentMaintenance, Label: "Content Maintenance", Group: "Indexing", Interval: "6h", Automated: true, LastRunState: "idle"},
		{ID: taskBacklogSearch, Label: "Backlog Search", Group: "Indexing", Interval: "15m", Automated: true, LastRunState: "idle"},
	}
	for i := range defs {
		if runAt, ok := lastRuns[defs[i].ID]; ok {
			ts := runAt
			defs[i].LastRunAt = &ts
		}
	}
	return defs, nil
}

func (s *taskScheduleStatusService) SetRSSIntervals(tvMinutes, movieMinutes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tvRssSyncIntervalMinutes = tvMinutes
	s.movieRssSyncIntervalMinutes = movieMinutes
}

func Run(ctx context.Context, logger zerolog.Logger) error {
	// Route all slog output (nntp, library, api packages) through the same
	// zerolog logger so every line shares the same color/format.
	slog.SetDefault(slog.New(observability.NewSlogHandler(logger)))
	logger.Info().Str("yenc", yenc.DecoderInfo()).Msg("startup: yEnc decoder")
	// Enables /debug/pprof/block and /debug/pprof/mutex on the loopback-only
	// debug server below -- needed to diagnose streaming stalls that are
	// blocking (waiting on a channel/lock/connection), which plain CPU
	// profiling can't see since a blocked goroutine burns no CPU time.
	runtime.SetBlockProfileRate(1)
	runtime.SetMutexProfileFraction(1)

	rt := config.DefaultRuntime()
	if env := os.Getenv("DRAKKAR_SETTINGS_PATH"); env != "" {
		rt.SettingsPath = env
	}
	if env := os.Getenv("DRAKKAR_HTTP_ADDR"); env != "" {
		rt.HTTPAddress = env
	}
	if env := os.Getenv("DRAKKAR_WEBDAV_ADDR"); env != "" {
		rt.WebDAVAddress = env
	}

	cfg, err := config.LoadOrCreate(rt.SettingsPath)
	if err != nil {
		return err
	}
	// ApplySettings (which also does this) only runs on a live PUT
	// /api/settings update, never on startup load — without this, every
	// restart would silently reset back to the LevelInfo the initial logger
	// was constructed with in main.go, ignoring whatever logging.level was
	// already persisted in settings.json.
	observability.SetGlobalLevel(observability.Level(cfg.Logging.Level))
	if err := ensureRcloneConf(rt.SettingsPath); err != nil {
		logger.Warn().Err(err).Msg("could not auto-create rclone.conf")
	}
	if err := config.ValidatePaths(rt); err != nil {
		return err
	}
	for _, dir := range []string{
		rt.BlockCachePath,
		rt.HeaderCachePath,
		rt.RepairWorkspacePath,
		rt.StagingNZBPath,
		rt.FailedDiagnosticsPath,
		rt.LogsPath,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	db, err := database.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		return err
	}
	if err := db.ApplyMigrations(ctx, filepath.Join(".", "migrations")); err != nil {
		return err
	}
	var scheduledSrc *nntp.ScheduledSource
	db.ReadAhead = stream.NewReadAheadManager(rt.ReadAheadLimitBytes)
	db.ReadAhead.SetArticleBufferSize(cfg.Usenet.ArticleBufferSize)
	var (
		articleSources         []nntp.NamedArticleSource
		pooledSources          []*nntp.PooledSource
		totalWorkers           int
		maxDownloadConnections int
	)
	for _, provider := range cfg.Usenet.Providers {
		if !provider.Enabled || provider.Host == "" {
			continue
		}
		totalWorkers += max(provider.MaxConnections, 1)
	}
	if len(cfg.Usenet.Providers) > 0 {
		maxDownloadConnections = cfg.Usenet.MaxDownloadConnections
		if maxDownloadConnections <= 0 {
			maxDownloadConnections = totalWorkers
		}
		if maxDownloadConnections > totalWorkers {
			maxDownloadConnections = totalWorkers
		}
	}
	for _, provider := range cfg.Usenet.Providers {
		if !provider.Enabled || provider.Host == "" {
			continue
		}
		client := nntp.NewArticleClient(provider)
		// The pool itself is capped at MaxDownloadConnections (matching
		// nzbdav's Math.Min(providerConnections, maxDownloadConnections)) --
		// not the raw provider.MaxConnections. provider.MaxConnections is the
		// account's own ceiling (e.g. 100), which is not the same thing as
		// how many of those Drakkar should actually use concurrently:
		// confirmed live that running near the account ceiling (90 of 100)
		// caused corrupted reads under heavy concurrent load, misclassified
		// as permanent yEnc CRC failures (see calibrate.go's
		// confirmPermanentCRCMismatch). Previously maxDownloadConnections only
		// throttled scheduler/read-ahead accounting further downstream while
		// the pool itself still allowed up to the full account ceiling.
		poolSize := provider.MaxConnections
		if maxDownloadConnections > 0 && maxDownloadConnections < poolSize {
			poolSize = maxDownloadConnections
		}
		pooled := nntp.NewPooledSource(ctx, client.NewSession, poolSize)
		pooledSources = append(pooledSources, pooled)
		articleSources = append(articleSources, nntp.NamedArticleSource{
			Name:   provider.Name,
			Source: pooled,
		})
	}
	if len(articleSources) > 0 {
		fallback := nntp.NewFallbackSource(articleSources, 1)
		// Wrap with a 24-hour missing-article cache so known-expired (430) IDs
		// are never re-fetched from NNTP within the TTL window.
		cachedFallback := nntp.NewCachedFallbackSource(fallback)
		scheduled := nntp.NewScheduledSource(ctx, cachedFallback, maxDownloadConnections*3, maxDownloadConnections*8)
		// No separate background budget (matches nzbdav behaviour) — all priorities share the pool
		diskDecoded := nntp.NewDiskCachedDecodedSource(scheduled, rt.BlockCachePath, rt.DiskCacheLimitBytes)
		decoded := nntp.NewCachedDecodedSource(diskDecoded, rt.MemoryHotCacheMaxBytes)
		db.SegmentFetcher = nntp.NewSegmentFetcher(decoded)
		db.ReadAhead.SetConnectionBudget(maxDownloadConnections, cfg.Usenet.StreamingPriorityPct)
		scheduledSrc = scheduled
		// Evict stale missing-article cache entries hourly.
		go func() {
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					cachedFallback.Evict()
				}
			}
		}()
	}

	newValkeyClient := func() *redis.Client {
		return redis.NewClient(&redis.Options{
			Addr:     fmt.Sprintf("%s:%d", cfg.Valkey.Host, cfg.Valkey.Port),
			Password: cfg.Valkey.Password,
			DB:       0,
			// Valkey 8 does not implement Redis maint_notifications; disable the
			// go-redis auto probe so startup stays quiet and deterministic.
			MaintNotificationsConfig: &maintnotifications.Config{
				Mode: maintnotifications.ModeDisabled,
			},
		})
	}
	valkey := newValkeyClient()
	defer valkey.Close()
	if err := valkey.Ping(ctx).Err(); err != nil {
		return err
	}
	// BullMQ requires separate Redis connections for the queue client and the
	// worker client to avoid CLIENT SETNAME collisions (per gobullmq docs).
	wqWorkerClient := newValkeyClient()
	defer wqWorkerClient.Close()

	startedAt := time.Now().UTC()
	statusSvc := &runtimeStatus{status: api.StatusFromConfig(rt, cfg, startedAt, true)}
	queueSvc := queue.NewService(db, nzb.NewImporter(rt.StagingNZBPath, rt.NZBUploadLimitBytes))
	seerrClient := seerr.NewClient(cfg.Seerr)
	hydraClient := hydra.NewClient(cfg.NZBHydra2)
	hydraClient.SetSearchDelay(time.Duration(cfg.Indexer.SearchDelayMs) * time.Millisecond)
	workflowSvc := workflow.NewService(db, seerrClient, hydraClient)
	// importWorkers = number of concurrent downloads, each with ~10 NNTP connections.
	// Cap raised 4->8: the old cap of 4 meant a 100-connection plan only ever
	// used 40 connections for imports (10/worker x 4) regardless of how much
	// headroom was actually available — the connection increase couldn't help
	// because this ceiling, not connection count, was the bottleneck. 8 still
	// leaves real headroom under a 100-connection plan for concurrent
	// streaming reads (see streamingPriorityPercent) rather than using
	// everything for backlog imports.
	importWorkers := 1
	if maxDownloadConnections > 0 {
		importWorkers = maxDownloadConnections / 10
		if importWorkers < 1 {
			importWorkers = 1
		}
		if importWorkers > 8 {
			importWorkers = 8
		}
		workflowSvc.SetImportConcurrency(importWorkers) // keeps importSem sized for ImportNZBFromPush
	}
	if checker, ok := db.SegmentFetcher.(database.SegmentChecker); ok {
		workflowSvc.SetEarlyChecker(checker.Exists)
		workflowSvc.SetArticleChecker(checker.Exists)
	}
	workflowSvc.SetPreflightChecker(func(ctx context.Context, item database.QueueSnapshot) error {
		if item.NZBDocumentID == nil {
			return nil
		}
		// Use a context independent of the BullMQ job ctx so that the 30s lock
		// expiry doesn't cancel an in-progress NNTP preflight (O-09 workaround).
		preflightCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		return db.PreflightCheckFirstSegments(preflightCtx, *item.NZBDocumentID)
	})
	wq, err := newDynamicWorkQueue(cfg.Indexer.BackgroundSearchWorkers, valkey, wqWorkerClient, logger)
	if err != nil {
		return fmt.Errorf("workqueue init: %w", err)
	}
	workflowSvc.WorkQueue = wq
	workflowSvc.SetIndexerLimits(workflow.IndexerLimits{
		MinimumAgeMinutes: cfg.Indexer.MinimumAgeMinutes,
		RetentionDays:     cfg.Indexer.RetentionDays,
		MaximumSizeMB:     cfg.Indexer.MaximumSizeMB,
	})
	if strings.TrimSpace(cfg.Metadata.TMDB.APIKey) != "" {
		workflowSvc.SetTMDBClient(tmdb.NewClient(cfg.Metadata))
	}
	if strings.TrimSpace(cfg.Metadata.TVDB.APIKey) != "" {
		workflowSvc.SetTVDBClient(tvdb.NewClient(cfg.Metadata))
	}
	publicationSvc := library.NewPublisher(db, rt, cfg.Rclone.RCAddr)
	rcloneClient := rclone.NewClient(cfg.Rclone.RCAddr)
	maintenanceSvc := &maintenanceOpsService{
		base:           maintenance.NewService(db, rt),
		db:             db,
		workflowSvc:    workflowSvc,
		publicationSvc: publicationSvc,
		logger:         logger,
	}
	cacheSvc := cache.NewService(cache.NewFileCache(rt.BlockCachePath, rt.DiskCacheLimitBytes))
	catalogSvc := catalog.NewService(db, tmdb.NewClient(cfg.Metadata))
	var subtitleProviders []subtitles.Provider
	var probeProviders []probe.NamedProber
	if strings.TrimSpace(cfg.Seerr.URL) != "" && strings.TrimSpace(cfg.Seerr.APIKey) != "" {
		probeProviders = append(probeProviders, seerrClient)
	}
	if strings.TrimSpace(cfg.NZBHydra2.URL) != "" && strings.TrimSpace(cfg.NZBHydra2.APIKey) != "" {
		probeProviders = append(probeProviders, hydraClient)
	}
	if cfg.Subtitles.Enabled {
		if auth, ok := cfg.Subtitles.Providers["subdl"]; ok && auth.Enabled && strings.TrimSpace(auth.APIKey) != "" {
			client := subdl.NewClient(auth)
			subtitleProviders = append(subtitleProviders, client)
			probeProviders = append(probeProviders, client)
		}
		if auth, ok := cfg.Subtitles.Providers["opensubtitles"]; ok && auth.Enabled && strings.TrimSpace(auth.APIKey) != "" && strings.TrimSpace(auth.Username) != "" && strings.TrimSpace(auth.Password) != "" {
			client := opensubtitles.NewClient(auth)
			subtitleProviders = append(subtitleProviders, client)
			probeProviders = append(probeProviders, client)
		}
	}
	for _, provider := range cfg.Usenet.Providers {
		if !provider.Enabled || strings.TrimSpace(provider.Host) == "" || strings.TrimSpace(provider.Username) == "" || strings.TrimSpace(provider.Password) == "" {
			continue
		}
		probeProviders = append(probeProviders, nntp.NewArticleClient(provider))
	}
	subtitleSvc := subtitles.NewService(db, cfg.Subtitles.Languages, subtitleProviders...)
	policySvc := policy.NewService(db)
	blocklistSvc := blocklist.NewService(db)
	probeSvc := probe.NewService(probeProviders...)
	plexClient := plex.NewClient(cfg.Plex.URL, cfg.Plex.Token)
	jellyfinClient := jellyfin.NewClient(cfg.Jellyfin.URL, cfg.Jellyfin.APIKey)
	// notifyMediaServers refreshes Plex/Jellyfin for a single library item.
	// Retries the Plex call a couple of times -- a brief Plex restart/network
	// blip shouldn't permanently strand an item unnoticed, since nothing else
	// ever retries this on the caller's behalf. Deliberately detached from the
	// caller's context (its own bounded timeouts below) so cancellation of the
	// triggering request can't cut this retry short.
	notifyMediaServers := func(libraryItemID int64) {
		if plexClient.Enabled() {
			plexCtx, plexCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer plexCancel()
			var plexErr error
			// Targeted refresh: scan only the directories of the newly created symlinks.
			if paths, err := db.GetSymlinkPathsForLibraryItem(plexCtx, libraryItemID); err == nil && len(paths) > 0 {
				dirs := make(map[string]struct{})
				for _, p := range paths {
					dirs[filepath.Dir(p)] = struct{}{}
				}
				for dir := range dirs {
					if plexErr = refreshPlexPathWithRetry(plexCtx, plexClient, cfg.Plex.SectionKey, dir); plexErr != nil {
						break
					}
				}
			} else if cfg.Plex.SectionKey != "" {
				plexErr = plexClient.RefreshSection(plexCtx, cfg.Plex.SectionKey)
			}
			if plexErr != nil {
				logger.Warn().Err(plexErr).Int64("libraryItemId", libraryItemID).Msg("plex library refresh failed")
			}
		}
		if jellyfinClient.Enabled() {
			jfCtx, jfCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer jfCancel()
			if err := jellyfinClient.RefreshLibraries(jfCtx); err != nil {
				logger.Warn().Err(err).Int64("libraryItemId", libraryItemID).Msg("jellyfin library refresh failed")
			}
		}
	}
	publicationSvc.SetPostPublishHook(func(ctx context.Context, libraryItemID int64) error {
		// Run all post-publish work in a goroutine so nothing blocks the queue pipeline.
		go func() {
			defer observability.Recover("post-publish-hook")
			bgCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_ = subtitleSvc.RepublishStoredSubtitles(bgCtx, libraryItemID)
			subtitleSvc.TriggerAutomaticSearch(libraryItemID)
			notifyMediaServers(libraryItemID)
		}()
		return nil
	})
	// Unlike SetPostPublishHook, this fires on repair/republish passes too
	// (RepublishLibraryItem, season-pack sibling fulfillment) -- those never
	// ran the full hook above, so an item whose symlink was missing or stale
	// and got fixed would otherwise never get the media server told about it
	// at all, with no error or log to reveal the gap.
	publicationSvc.SetMediaServerNotifyHook(func(ctx context.Context, libraryItemID int64) error {
		go func() {
			defer observability.Recover("media-server-notify-hook")
			notifyMediaServers(libraryItemID)
		}()
		return nil
	})
	notifier := notifications.New(notifications.Config{
		DiscordWebhookURL: cfg.Notifications.DiscordWebhookURL,
		GenericWebhookURL: cfg.Notifications.GenericWebhookURL,
		OnGrab:            cfg.Notifications.OnGrab,
		OnAvailable:       cfg.Notifications.OnAvailable,
		OnFailed:          cfg.Notifications.OnFailed,
	}, nil)

	postImport := func(ctx context.Context, item database.QueueSnapshot) error {
		if item.SelectedRelease == nil {
			return nil
		}
		if err := verifyContentBeforePublish(ctx, db, rt, rcloneClient, *item.SelectedRelease, logger); err != nil {
			logger.Warn().
				Err(err).
				Int64("selectedReleaseId", *item.SelectedRelease).
				Int64("libraryItemId", item.LibraryItemID).
				Msg("pre-publish validation failed — rejecting release and searching for next candidate")
			return workflowSvc.FailAndBlocklistRelease(ctx, *item.SelectedRelease, "pre-publish validation: "+err.Error())
		}
		if err := publicationSvc.PublishSelectedRelease(ctx, *item.SelectedRelease); err != nil {
			return err
		}
		// Notify Seerr + outgoing notifications (best-effort).
		if input, err := db.GetLibrarySearchInput(ctx, item.LibraryItemID); err == nil {
			tmdbID := input.MovieTMDBID
			mediaType := input.MediaType
			if mediaType == "episode" || mediaType == "tv" {
				tmdbID = input.ShowTMDBID
			}
			if tmdbID > 0 {
				if notifyErr := seerrClient.NotifyAvailable(ctx, tmdbID, mediaType); notifyErr != nil {
					logger.Warn().Err(notifyErr).Int64("libraryItemId", item.LibraryItemID).Msg("seerr notify available failed")
				}
			}
			notifier.Send(ctx, notifications.Event{
				Type:      notifications.EventAvailable,
				Title:     input.Title,
				MediaType: mediaType,
			})
		}
		return nil
	}

	queueSvc.SetPostImportHook(postImport)
	workflowSvc.SetPostImportHook(postImport)
	workflowSvc.SetQueuePolicyProvider(policySvc)
	workflowSvc.SetLogger(logger)
	workflowSvc.SetDefaultProfileNames(cfg.Library.DefaultMovieProfile, cfg.Library.DefaultTvProfile)
	db.SetDefaultProfileNames(cfg.Library.DefaultMovieProfile, cfg.Library.DefaultTvProfile)
	broker := api.NewEventBroker()

	// live metrics collector — reads NNTP pool + scheduler + disk cache at query time
	var pooledSrcs []*nntp.PooledSource
	if len(pooledSources) > 0 {
		pooledSrcs = pooledSources
	}
	blockCache := cache.NewFileCache(rt.BlockCachePath, rt.DiskCacheLimitBytes)
	metricsColl := &liveMetricsCollector{
		readAhead:  db.ReadAhead,
		pools:      pooledSrcs,
		scheduled:  scheduledSrc,
		blockCache: blockCache,
	}
	taskScheduleSvc := &taskScheduleStatusService{db: db, tvRssSyncIntervalMinutes: cfg.Indexer.TvRssSyncIntervalMinutes, movieRssSyncIntervalMinutes: cfg.Indexer.MovieRssSyncIntervalMinutes}
	recentTaskMgr := newRecurringTaskManager(ctx, logger)
	liveSettings := &liveSettingsController{
		rt:            rt,
		startedAt:     startedAt,
		status:        statusSvc,
		taskSchedules: taskScheduleSvc,
		workflowSvc:   workflowSvc,
		hydraClient:   hydraClient,
		workQueue:     wq,
		recentTasks:   recentTaskMgr,
	}

	server := &http.Server{
		Addr:              rt.HTTPAddress,
		Handler:           api.Router(statusSvc, queueSvc, workflowSvc, publicationSvc, maintenanceSvc, cacheSvc, subtitleSvc, blocklistSvc, probeSvc, catalogSvc, broker, db, db.ReadAhead, db, taskScheduleSvc, policySvc, plexClient, jellyfinClient, &fileSettingsService{path: rt.SettingsPath, applier: liveSettings}, db, metricsColl),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Reset queue items that were left in transitional states by a previous crash.
	if n, err := db.ResetStuckQueueItems(ctx); err != nil {
		logger.Error().Err(err).Msg("could not reset stuck queue items")
	} else if n > 0 {
		logger.Info().Int("reset", n).Msg("reset stuck queue items to failed")
	}

	// Immediately recover all items interrupted by a restart (stale_worker /
	// interrupted_by_restart with an existing selected release).  These only
	// need a DB state flip — no Hydra call — so there is no reason to delay
	// them behind the 500-per-pass cap that RetryFailedQueue uses for items
	// that do call Hydra.
	if n, err := db.RecoverInterruptedDownloads(ctx); err != nil {
		logger.Error().Err(err).Msg("startup: interrupted download recovery error")
	} else if n > 0 {
		logger.Info().Int("recovered", n).Msg("startup: interrupted downloads recovered — re-queued for download")
	}

	// Start dedicated download workers — one per concurrent download slot.
	// These are the only goroutines that call fetchIndexAndRelease; all other
	// callers (BullMQ workers, CompleteSelectedQueue, HTTP retry) submit jobs
	// to the priority queue and wait, exactly like SABnzbd's download queue.
	for i := 0; i < importWorkers; i++ {
		go workflowSvc.RunDownloadWorker(ctx)
	}

	// Start the BullMQ worker pool in the background.
	go func() {
		if err := workflowSvc.WorkQueue.Start(ctx, func(qCtx context.Context, libraryItemID int64) {
			_ = qCtx
			jobCtx, cancel := context.WithTimeout(ctx, 90*time.Minute)
			defer cancel()
			if err := workflowSvc.ProcessLibraryItem(jobCtx, libraryItemID); err != nil {
				logger.Error().Err(err).Int64("libraryItemId", libraryItemID).Msg("workqueue: processing error (see above for details)")
			}
		}); err != nil && err != context.Canceled {
			logger.Error().Err(err).Msg("workqueue: worker stopped unexpectedly")
		}
	}()

	runRecentPass := func(mediaType string) {
		sr, err := workflowSvc.SearchRecentPending(ctx, mediaType)
		if err != nil {
			logger.Error().Err(err).Str("mediaType", mediaType).Msg("monitoring: recent search failed")
			return
		}
		if err := db.TouchMaintenanceCursor(ctx, maintenanceRecentTaskName(mediaType), time.Now().UTC().Format(time.RFC3339)); err != nil {
			logger.Error().Err(err).Str("mediaType", mediaType).Msg("monitoring: could not persist recent cursor")
		}
		if sr.Searched > 0 || sr.Selected > 0 {
			broker.Publish(map[string]any{"kind": "library.reconcile_background", "mediaType": mediaType, "searched": sr.Searched, "selected": sr.Selected})
			logger.Info().Str("mediaType", mediaType).Int("processed", sr.Processed).Int("searched", sr.Searched).Int("selected", sr.Selected).Msg("monitoring: recent search complete")
		}
	}

	// runStaleReset resets items stuck in transitional states.
	// Idle transitions (selected, searching, etc.) time out after 10 minutes.
	// Active download states (fetching_nzb, indexing, publishing) time out after
	// 90 minutes — large episodes plus fallback churn can legitimately run long.
	// Selected state gets 45 minutes because completion fast-lane now retries it
	// every minute; if it still sits selected that long, it is genuinely stale.
	// queue_housekeeping: stale-reset first, then retry-failed, every 10 min.
	// Merging halves goroutine count; 10m is a compromise between old 5m stale
	// and old 15m retry — stale items reset sooner, failed items retry sooner.
	runQueueHousekeeping := func() {
		n, err := db.ResetStaleQueueItems(ctx, 10*time.Minute, 90*time.Minute, 45*time.Minute)
		if err != nil {
			logger.Error().Err(err).Msg("monitoring: stale reset error")
		} else {
			if n > 0 {
				logger.Warn().Int("reset", n).Msg("monitoring: stale queue items reset")
				broker.Publish(map[string]any{"kind": "queue.stale_reset", "reset": n})
			}
		}
		rr, err := workflowSvc.RetryFailedQueue(ctx)
		if err != nil {
			logger.Error().Err(err).Msg("monitoring: retry failed queue error")
			return
		}
		_ = db.TouchMaintenanceCursor(ctx, taskQueueHousekeeping, time.Now().UTC().Format(time.RFC3339))
		if rr.Retried > 0 {
			broker.Publish(map[string]any{"kind": "queue.retry_background", "retried": rr.Retried})
		}
		logger.Info().Int("retried", rr.Retried).Msg("monitoring: queue housekeeping complete")
	}

	// runSyncOnce syncs Seerr requests.
	runSyncOnce := func() {
		result, err := workflowSvc.SyncRequests(ctx)
		if err != nil {
			logger.Error().Err(err).Msg("seerr sync failed")
			return
		}
		_ = db.TouchMaintenanceCursor(ctx, taskSeerrSync, time.Now().UTC().Format(time.RFC3339))
		if result.Created > 0 {
			broker.Publish(map[string]any{"kind": "requests.sync_background", "seen": result.Seen, "created": result.Created})
		}
		logger.Info().Int("seen", result.Seen).Int("created", result.Created).Msg("seerr sync complete")
	}

	// runPendingDispatch only resumes items that already have a selected release.
	// Servarr-style automatic behavior uses RSS for discovery and does not
	// actively search the full missing backlog on a timer.
	runPendingDispatch := func() {
		result, err := workflowSvc.DispatchAutomaticPending(ctx)
		if err != nil {
			logger.Error().Err(err).Msg("monitoring: pending dispatch error")
			return
		}
		_ = db.TouchMaintenanceCursor(ctx, taskPendingQueuePush, time.Now().UTC().Format(time.RFC3339))
		if result.Searched > 0 {
			broker.Publish(map[string]any{"kind": "library.pending_background", "searched": result.Searched, "selected": result.Selected, "failed": result.Failed})
		}
		logger.Info().Int("pending", result.Processed).Int("dispatched", result.Searched).Msg("monitoring: pending dispatch complete")
	}
	// runBacklogSearch actively searches all pending library items via Hydra2.
	// Unlike runPendingDispatch (which only resumes items with a selected release),
	// this pushes every unresolved item into the BullMQ search workers so old
	// content that never appears in RSS feeds gets found and downloaded.
	runBacklogSearch := func() {
		result, err := workflowSvc.SearchPendingLibrary(ctx)
		if err != nil {
			logger.Error().Err(err).Msg("backlog search: error")
			return
		}
		_ = db.TouchMaintenanceCursor(ctx, taskBacklogSearch, time.Now().UTC().Format(time.RFC3339))
		if result.Searched > 0 {
			broker.Publish(map[string]any{"kind": "library.backlog_search_background", "searched": result.Searched})
		}
		logger.Info().Int("processed", result.Processed).Int("searched", result.Searched).Msg("backlog search: complete")
	}

	shouldSkipNonCriticalMaintenance := func() (bool, string) {
		backlog, err := db.CountActiveSearchBacklog(ctx)
		if err == nil && backlog >= nonCriticalBacklogThreshold {
			return true, fmt.Sprintf("search backlog high (%d)", backlog)
		}
		if wq != nil {
			if depth := wq.Depth(ctx); depth >= nonCriticalQueueDepthLimit {
				return true, fmt.Sprintf("work queue depth high (%d)", depth)
			}
		}
		return false, ""
	}

	// publishing_maintenance: republish pending + reset orphaned, every 30 min.
	runPublishingMaintenance := func() {
		if skip, reason := shouldSkipNonCriticalMaintenance(); skip {
			logger.Info().Str("task", taskPublishingMaintenance).Str("reason", reason).Msg("scheduler: skipping non-critical task")
			return
		}
		if result, err := publicationSvc.RepublishPendingLibrary(ctx); err != nil {
			logger.Error().Err(err).Msg("monitoring: republish pending error")
		} else {
			if result.Republished > 0 {
				broker.Publish(map[string]any{"kind": "library.republish_background", "republished": result.Republished})
			}
			logger.Info().Int("republished", result.Republished).Msg("monitoring: republish pending complete")
		}
		if result, err := workflowSvc.ResetOrphanedAvailableItems(ctx); err != nil {
			logger.Error().Err(err).Msg("monitoring: reset orphaned available items error")
		} else {
			logger.Info().Int("found", result.Found).Int("reset", result.Reset).Msg("monitoring: reset orphaned available items complete")
		}
		_ = db.TouchMaintenanceCursor(ctx, taskPublishingMaintenance, time.Now().UTC().Format(time.RFC3339))
	}
	runHealthCheck := func() {
		entries, err := db.ListHealthEntries(ctx)
		if err != nil {
			logger.Error().Err(err).Msg("monitoring: health check list error")
			return
		}
		var checked, healthy int
		for _, e := range entries {
			checked++
			isOK := database.CheckSymlinkHealth(e.LibraryPath, e.TargetPath)
			if isOK {
				healthy++
				// A resolving symlink only proves os.Readlink matched a
				// content/ path — it never reads a byte of the target, so it
				// can't prove the content is actually playable. Writing
				// health_ok=true here unconditionally (every 15 minutes, for
				// every item) used to silently erase whatever the real deep
				// content check (runNZBHealthCheckBatch, below) had
				// determined, and reset its backoff clock so it effectively
				// never got the chance to re-run. Leave health_ok exactly as
				// the last real check left it; only a genuine negative
				// (below) or a successful repair is worth recording here.
				continue
			}
			_ = db.RecordHealthCheck(ctx, e.ID, false)
		}
		// Repair pass: re-publish any entries still marked broken after the scan above.
		var repaired int
		if publicationSvc != nil {
			broken, brokenErr := db.ListBrokenSymlinkEntries(ctx)
			if brokenErr == nil {
				for _, e := range broken {
					if ctx.Err() != nil {
						break
					}
					if err := publicationSvc.RepublishLibraryItem(ctx, e.LibraryItemID); err != nil {
						logger.Warn().Err(err).Int64("libraryItemId", e.LibraryItemID).Msg("health check: symlink repair failed")
						continue
					}
					isOK := database.CheckSymlinkHealth(e.LibraryPath, e.TargetPath)
					_ = db.RecordHealthCheck(ctx, e.ID, isOK)
					if isOK {
						repaired++
					}
				}
			}
		}
		deepResult, err := runNZBHealthCheckBatch(ctx, db, workflowSvc, publicationSvc, logger, backgroundDeepHealthBatchSize, false)
		if err != nil {
			logger.Error().Err(err).Msg("monitoring: background deep health check error")
		}
		calibrated, _ := db.CalibrateNZBOffsetsBatch(ctx, backgroundDeepHealthBatchSize)
		_ = db.TouchMaintenanceCursor(ctx, taskHealthCheck, time.Now().UTC().Format(time.RFC3339))
		broker.Publish(map[string]any{
			"kind":          "health.check_background",
			"checked":       checked,
			"healthy":       healthy,
			"deepChecked":   deepResult.ScannedRows,
			"repairedItems": deepResult.RepairedItems,
			"degradedItems": deepResult.DegradedItems,
			"resetItems":    deepResult.ResetItems,
		})
		logger.Info().
			Int("checked", checked).
			Int("healthy", healthy).
			Int("symlinksRepaired", repaired).
			Int("deepChecked", deepResult.ScannedRows).
			Int("repaired", deepResult.RepairedItems).
			Int("degraded", deepResult.DegradedItems).
			Int("reset", deepResult.ResetItems).
			Int("calibrated", calibrated).
			Msg("monitoring: health check complete")
	}

	// storage_maintenance: library cleanup then cache prune, every 6h.
	// The cheap DB-only orphan prunes run unconditionally, regardless of
	// backlog load: they're plain DELETE queries, not filesystem scans, so
	// they don't compete with active downloads/imports the way the
	// filesystem-heavy steps below do. Gating them on backlog load meant the
	// only cleanup pass for orphaned selected_releases rows was silently
	// skipped for days at a time exactly when chronic backlog made orphan
	// accumulation worst -- a safety net that only worked when the system
	// didn't need it.
	runStorageMaintenance := func() {
		if result, err := maintenanceSvc.PruneStaleReleaseCandidates(ctx); err != nil {
			logger.Error().Err(err).Msg("monitoring: release candidate prune error")
		} else if result.DeletedRows > 0 {
			logger.Info().Int("deletedRows", result.DeletedRows).Msg("monitoring: pruned stale release candidates")
		}
		if result, err := maintenanceSvc.PruneOrphanedSelectedReleases(ctx); err != nil {
			logger.Error().Err(err).Msg("monitoring: orphaned selected-release prune error")
		} else if result.DeletedRows > 0 {
			logger.Info().Int("deletedRows", result.DeletedRows).Msg("monitoring: pruned orphaned selected releases")
		}
		// recent_url_fetches only needs to outlive the 30-min per-URL fetch
		// cooldown it backstops; keep a comfortable multiple of that so a
		// prune pass never races an in-progress cooldown window.
		if deleted, err := db.PruneRecentURLFetches(ctx, 2*time.Hour); err != nil {
			logger.Error().Err(err).Msg("monitoring: recent URL fetch prune error")
		} else if deleted > 0 {
			logger.Info().Int64("deletedRows", deleted).Msg("monitoring: pruned recent URL fetch records")
		}
		if skip, reason := shouldSkipNonCriticalMaintenance(); skip {
			logger.Info().Str("task", taskStorageMaintenance).Str("reason", reason).Msg("scheduler: skipping non-critical (filesystem-heavy) storage maintenance")
			return
		}
		_, _ = maintenanceSvc.RemoveOrphanedContent(ctx)
		_, _ = maintenanceSvc.RemoveBrokenMediaSymlinks(ctx)
		_, _ = maintenanceSvc.RemoveOrphanedCompletedSymlinks(ctx)
		if result, err := cacheSvc.Prune(ctx); err != nil {
			logger.Error().Err(err).Msg("monitoring: cache prune error")
		} else {
			if result.DeletedFiles > 0 {
				broker.Publish(map[string]any{"kind": "cache.prune_background", "deletedFiles": result.DeletedFiles})
			}
			logger.Info().Int("deletedFiles", result.DeletedFiles).Msg("monitoring: cache prune complete")
		}
		_ = db.TouchMaintenanceCursor(ctx, taskStorageMaintenance, time.Now().UTC().Format(time.RFC3339))
	}

	// content_maintenance: fill missing episodes + upgrade search, every 6h.
	// Load-gated so it doesn't compete with active backlog processing.
	runContentMaintenance := func() {
		if skip, reason := shouldSkipNonCriticalMaintenance(); skip {
			logger.Info().Str("task", taskContentMaintenance).Str("reason", reason).Msg("scheduler: skipping non-critical task")
			return
		}
		if _, err := workflowSvc.FillMissingEpisodes(ctx); err != nil {
			logger.Error().Err(err).Msg("fill missing episodes failed")
		}
		if res, err := workflowSvc.SearchUpgrades(ctx); err != nil {
			logger.Error().Err(err).Msg("upgrade search failed")
		} else {
			logger.Info().Int("checked", res.Checked).Int("upgraded", res.Upgraded).Int("failed", res.Failed).Msg("upgrade search complete")
		}
		_ = db.TouchMaintenanceCursor(ctx, taskContentMaintenance, time.Now().UTC().Format(time.RFC3339))
	}

	// startRecurring/startRecurringWithStartupDelay used to be a second,
	// independent copy of the goroutine/ticker loop already implemented by
	// recurringTaskManager (live_settings.go) — only the RSS sync tasks went
	// through the manager (for Reschedule support), while every other
	// startup-scheduled task here ran its own duplicate scheduler with no
	// cancel/reschedule support. Both now share recentTaskMgr.
	startRecurring := recentTaskMgr.Start
	startRecurringWithStartupDelay := recentTaskMgr.StartWithStartupDelay

	// background worker: Seerr sync every 10 min. Sync imports requests only;
	// discovery happens via recent-feed polling or explicit/manual search.
	startRecurring(taskSeerrSync, 10*time.Minute, true, runSyncOnce)
	// Sync Plex-detected shows (partial Seerr media without requests) hourly.
	startRecurringWithStartupDelay(taskSyncPlexDetected, 60*time.Minute, 90*time.Second, func() {
		result, err := workflowSvc.SyncPlexDetectedShows(ctx)
		if err != nil {
			logger.Error().Err(err).Msg("sync plex detected shows failed")
			return
		}
		_ = db.TouchMaintenanceCursor(ctx, taskSyncPlexDetected, time.Now().UTC().Format(time.RFC3339))
		if result.Requested > 0 {
			broker.Publish(map[string]any{"kind": "requests.sync_background", "seen": result.Found, "created": result.Requested})
		}
		logger.Info().Int("found", result.Found).Int("requested", result.Requested).Int("skipped", result.Skipped).Msg("sync plex detected shows complete")
	})
	// Wake-driven dispatch: fires immediately when SyncRequests creates new items
	// (via workflowSvc.DispatchWakeCh()) and falls back to the tick interval.
	go func() {
		ticker := time.NewTicker(pendingQueueDispatchInterval)
		defer ticker.Stop()
		logger.Info().Str("task", taskPendingQueuePush).Dur("interval", pendingQueueDispatchInterval).Bool("startup", true).Msg("scheduler: task started")
		runPendingDispatch()
		for {
			select {
			case <-ctx.Done():
				return
			case <-workflowSvc.DispatchWakeCh():
				runPendingDispatch()
			case <-ticker.C:
				runPendingDispatch()
			}
		}
	}()

	tvRssInterval := boundedTVRSSInterval(cfg.Indexer.TvRssSyncIntervalMinutes)
	movieRssInterval := boundedMovieRSSInterval(cfg.Indexer.MovieRssSyncIntervalMinutes)

	recentTaskMgr.Start(maintenanceRecentTVTask, tvRssInterval, shouldRunRecentOnStartup(ctx, db, maintenanceRecentTVTask, tvRssInterval, time.Duration(cfg.NZBHydra2.FeedCacheTTLSeconds)*time.Second, time.Now().UTC()), func() {
		runRecentPass("tv")
	})
	recentTaskMgr.Start(maintenanceRecentMovieTask, movieRssInterval, shouldRunRecentOnStartup(ctx, db, maintenanceRecentMovieTask, movieRssInterval, time.Duration(cfg.NZBHydra2.FeedCacheTTLSeconds)*time.Second, time.Now().UTC()), func() {
		runRecentPass("movie")
	})

	startRecurring(taskQueueHousekeeping, 10*time.Minute, true, runQueueHousekeeping)
	startRecurringWithStartupDelay(taskPublishingMaintenance, 30*time.Minute, 2*time.Minute, runPublishingMaintenance)
	startRecurringWithStartupDelay(taskHealthCheck, backgroundHealthCheckInterval, 6*time.Minute, runHealthCheck)
	// runOnStartup checks the persisted cursor rather than a bare `false`:
	// this task's 168-hour (7-day) in-process timer resets to zero on every
	// restart, and a redeployed app restarts far more often than every 7
	// days -- with a bare `false` (no startup catch-up at all, unlike this
	// file's other long-interval tasks) this task could go without ever
	// completing a single run for as long as deploys keep happening more
	// often than the interval. Confirmed live: its maintenance_cursors row
	// was 10+ days stale despite the app having been redeployed many times
	// in that window. shouldRunRecentOnStartup runs it immediately only if
	// actually overdue, so it still won't fire on every restart once it's
	// caught up.
	startRecurring(taskNZBHealthCheck, 168*time.Hour, shouldRunRecentOnStartup(ctx, db, taskNZBHealthCheck, 168*time.Hour, 0, time.Now().UTC()), func() {
		if _, err := maintenanceSvc.DeepNZBHealthCheck(ctx); err != nil {
			logger.Error().Err(err).Msg("deep nzb health check failed")
			return
		}
		_ = db.TouchMaintenanceCursor(ctx, taskNZBHealthCheck, time.Now().UTC().Format(time.RFC3339))
	})
	startRecurringWithStartupDelay(taskArticleHealthCheck, 6*time.Hour, 15*time.Minute, func() {
		n, err := workflowSvc.ValidatePublishedArticles(ctx)
		if err != nil {
			logger.Error().Err(err).Msg("article health check failed")
			return
		}
		_ = db.TouchMaintenanceCursor(ctx, taskArticleHealthCheck, time.Now().UTC().Format(time.RFC3339))
		if n > 0 {
			logger.Warn().Int("reset", n).Msg("article health check: reset library items with unavailable articles")
		} else {
			logger.Info().Msg("article health check: all published articles reachable")
		}
	})
	startRecurringWithStartupDelay(taskStorageMaintenance, 6*time.Hour, 10*time.Minute, runStorageMaintenance)
	startRecurringWithStartupDelay(taskContentMaintenance, 6*time.Hour, 20*time.Minute, runContentMaintenance)
	// A cycle only takes ~9 minutes at the safe per-call search pace (2s
	// delay, ~260 deduped targets/cycle), so a 30-minute interval left ~20
	// minutes idle per cycle. Halved to close most of that gap without
	// removing it entirely — daily indexer API quotas are the actual
	// constraint here, not this scheduling interval, and there's no
	// visibility into where those caps are, so this is a deliberately
	// moderate change rather than running cycles back-to-back.
	startRecurring(taskBacklogSearch, 15*time.Minute, true, runBacklogSearch)

	webdavServer := &http.Server{
		Addr:              rt.WebDAVAddress,
		Handler:           dav.Handler(db, rt.MovieLibraryPath, rt.TVLibraryPath),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Loopback-only pprof server -- not published in docker-compose.yml, so
	// it's reachable solely via `docker exec ... wget http://127.0.0.1:6060/debug/pprof/...`.
	// Added to diagnose a live CPU/stall issue under 4K streaming load; kept
	// permanently since profiling access has no cost when nothing is polling it.
	debugServer := &http.Server{
		Addr:              "127.0.0.1:6060",
		Handler:           http.DefaultServeMux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info().Str("addr", rt.HTTPAddress).Msg("http server starting")
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	go func() {
		logger.Info().Str("addr", rt.WebDAVAddress).Msg("webdav server starting")
		if err := webdavServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error().Err(err).Msg("webdav server error")
		}
	}()
	go func() {
		logger.Info().Str("addr", debugServer.Addr).Msg("pprof debug server starting")
		if err := debugServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error().Err(err).Msg("pprof debug server error")
		}
	}()
	go func() {
		if err := publicationSvc.RebuildPublications(ctx); err != nil {
			logger.Error().Err(err).Msg("rebuild publications failed")
		}
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = webdavServer.Shutdown(shutdownCtx)
		_ = debugServer.Shutdown(shutdownCtx)
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	case err := <-errCh:
		return err
	}
}

func maintenanceRecentTaskName(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "tv", "episode":
		return maintenanceRecentTVTask
	default:
		return maintenanceRecentMovieTask
	}
}

func shouldRunRecentOnStartup(ctx context.Context, db *database.DB, taskName string, floor time.Duration, ttl time.Duration, now time.Time) bool {
	if ttl <= 0 {
		ttl = floor
	}
	cursor, err := db.GetMaintenanceCursor(ctx, taskName)
	if err != nil || strings.TrimSpace(cursor) == "" {
		return true
	}
	lastRun, err := time.Parse(time.RFC3339, cursor)
	if err != nil {
		return true
	}
	age := now.Sub(lastRun.UTC())
	skipWindow := floor
	if ttl > skipWindow {
		skipWindow = ttl
	}
	return age >= skipWindow
}

// ensureRcloneConf writes a default rclone WebDAV config alongside settings.json
// if it doesn't already exist. rclone.conf lives at {dataDir}/rclone/rclone.conf.
func ensureRcloneConf(settingsPath string) error {
	confPath := filepath.Join(filepath.Dir(settingsPath), "rclone", "rclone.conf")
	if _, err := os.Stat(confPath); err == nil {
		return nil // already exists
	}
	if err := os.MkdirAll(filepath.Dir(confPath), 0o755); err != nil {
		return err
	}
	const content = "[drakkar]\ntype = webdav\nurl = http://drakkar:8888\nvendor = other\n"
	return os.WriteFile(confPath, []byte(content), 0o644)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type liveMetricsCollector struct {
	readAhead  *stream.ReadAheadManager
	pools      []*nntp.PooledSource
	scheduled  *nntp.ScheduledSource
	blockCache *cache.FileCache
}

func (c *liveMetricsCollector) Collect() metrics.Snapshot {
	var nntpStats metrics.NNTPStats
	for _, p := range c.pools {
		open, idle := p.Stats()
		inUse := open - idle
		if inUse < 0 {
			inUse = 0
		}
		nntpStats.Active += int64(inUse)
		nntpStats.Idle += int64(idle)
	}
	var queueStats metrics.QueueStats
	if c.scheduled != nil {
		interactive, readAhead, background := c.scheduled.QueueDepths()
		queueStats.Interactive = int64(interactive)
		queueStats.Background = int64(readAhead + background)
	}
	var cacheStats metrics.CacheStats
	if c.blockCache != nil {
		if stats, err := c.blockCache.Stats(); err == nil {
			cacheStats.DiskBytes = stats.Bytes
		}
	}
	if c.readAhead != nil {
		metrics.M.ActiveStreams.Store(int64(c.readAhead.ActiveCount()))
	}
	return metrics.M.Collect(nntpStats, cacheStats, queueStats)
}

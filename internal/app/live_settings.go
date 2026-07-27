package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/drakkar-media/drakkar/internal/api"
	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/hydra"
	"github.com/drakkar-media/drakkar/internal/jellyfin"
	"github.com/drakkar-media/drakkar/internal/notifications"
	"github.com/drakkar-media/drakkar/internal/observability"
	"github.com/drakkar-media/drakkar/internal/plex"
	"github.com/drakkar-media/drakkar/internal/privacy"
	"github.com/drakkar-media/drakkar/internal/rclone"
	"github.com/drakkar-media/drakkar/internal/seerr"
	"github.com/drakkar-media/drakkar/internal/stream"
	"github.com/drakkar-media/drakkar/internal/tmdb"
	"github.com/drakkar-media/drakkar/internal/tvdb"
	"github.com/drakkar-media/drakkar/internal/workflow"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// recurringTaskManager owns the interval-loop goroutine for every named
// background task (RSS sync, health checks, maintenance sweeps) and allows a
// live settings change to reschedule a task's interval without restarting
// the process. Safe for concurrent use.
type recurringTaskManager struct {
	rootCtx context.Context
	logger  zerolog.Logger

	mu    sync.Mutex
	tasks map[string]*managedRecurringTask
}

// managedRecurringTask records one task's current schedule and the cancel
// function for its running interval-loop goroutine, so Reschedule can stop
// the old loop before starting a new one with an updated interval.
type managedRecurringTask struct {
	name         string
	interval     time.Duration
	runOnStartup bool
	fn           func()
	cancel       context.CancelFunc
}

func newRecurringTaskManager(rootCtx context.Context, logger zerolog.Logger) *recurringTaskManager {
	return &recurringTaskManager{
		rootCtx: rootCtx,
		logger:  logger,
		tasks:   make(map[string]*managedRecurringTask),
	}
}

// Start registers and starts a recurring task under name, running fn every
// interval. If a task is already registered under the same name, its
// existing interval-loop goroutine is canceled first so calling Start again
// replaces rather than duplicates the running loop.
func (m *recurringTaskManager) Start(name string, interval time.Duration, runOnStartup bool, fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.tasks[name]; ok && existing.cancel != nil {
		existing.cancel()
	}
	task := &managedRecurringTask{
		name:         name,
		interval:     interval,
		runOnStartup: runOnStartup,
		fn:           fn,
	}
	m.tasks[name] = task
	m.startLocked(task)
}

// Reschedule changes a registered task's interval, canceling and restarting
// its loop with the new period. A no-op if the task is unknown or the
// interval is unchanged, so a settings save that didn't touch this task's
// schedule doesn't reset its in-flight timer. The restarted loop always has
// runOnStartup disabled, since rescheduling should not trigger an
// out-of-cycle run.
func (m *recurringTaskManager) Reschedule(name string, interval time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[name]
	if !ok {
		return
	}
	if task.interval == interval {
		return
	}
	if task.cancel != nil {
		task.cancel()
	}
	task.interval = interval
	task.runOnStartup = false
	m.startLocked(task)
}

// StartWithStartupDelay is Start plus a one-shot extra run after
// startupDelay, separate from the task's own interval loop — useful to give
// a task a first run sooner than its steady-state interval without making
// every subsequent run happen on that shorter cadence.
func (m *recurringTaskManager) StartWithStartupDelay(name string, interval, startupDelay time.Duration, fn func()) {
	m.Start(name, interval, false, fn)
	go func() {
		defer observability.Recover(name + "-startup-delay")
		timer := time.NewTimer(startupDelay)
		defer timer.Stop()
		select {
		case <-m.rootCtx.Done():
			return
		case <-timer.C:
			fn()
		}
	}()
	m.logger.Info().Str("task", name).Dur("startupDelay", startupDelay).Msg("scheduler: delayed startup task armed")
}

// runProtected recovers a panic from a single tick of a recurring task so one
// bad run (e.g. an unexpected Hydra/TMDB/Seerr response shape) logs an error
// instead of silently ending the task's for-loop -- or, without any recovery
// at all, crashing the entire process and taking down every in-flight
// download/stream with it.
func runProtected(name string, fn func()) {
	defer observability.Recover(name)
	fn()
}

func (m *recurringTaskManager) startLocked(task *managedRecurringTask) {
	runCtx, cancel := context.WithCancel(m.rootCtx)
	task.cancel = cancel
	go func(name string, interval time.Duration, runOnStartup bool, fn func()) {
		if runOnStartup {
			runProtected(name, fn)
		}
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-timer.C:
				runProtected(name, fn)
				timer.Reset(interval)
			}
		}
	}(task.name, task.interval, task.runOnStartup, task.fn)
	m.logger.Info().Str("task", task.name).Dur("interval", task.interval).Bool("startup", task.runOnStartup).Msg("scheduler: task started")
}

// dynamicWorkQueue wraps workflow.WorkQueue behind a swap-pointer (the same
// pattern as dynamicArticleSource) so the background search worker pool can
// be resized at runtime, in response to a live settings change, without
// restarting the application. Safe for concurrent use.
type dynamicWorkQueue struct {
	queueClient  redis.Cmdable
	workerClient redis.Cmdable
	logger       zerolog.Logger

	mu      sync.RWMutex
	inner   *workflow.WorkQueue
	workers int

	// rootCtx and handler are saved from the original Start call so Resize
	// can restart the worker loop against the same parent context and
	// per-item handler after swapping in a newly-sized inner queue.
	rootCtx context.Context
	handler func(context.Context, int64)

	// workerCancel and workerDone track the currently-running worker loop's
	// cancel function and completion signal, so Resize can stop the old loop
	// only after the new one is live and await its exit without blocking the
	// resize itself.
	workerCancel context.CancelFunc
	workerDone   chan struct{}
	started      bool
}

func newDynamicWorkQueue(workers int, queueClient, workerClient redis.Cmdable, logger zerolog.Logger) (*dynamicWorkQueue, error) {
	inner, err := workflow.NewWorkQueue(workers, queueClient, workerClient)
	if err != nil {
		return nil, err
	}
	if workers < 1 {
		workers = 1
	}
	return &dynamicWorkQueue{
		queueClient:  queueClient,
		workerClient: workerClient,
		logger:       logger,
		inner:        inner,
		workers:      workers,
	}, nil
}

func (q *dynamicWorkQueue) Push(ctx context.Context, libraryItemID int64, priority int) {
	q.mu.RLock()
	inner := q.inner
	q.mu.RUnlock()
	if inner != nil {
		inner.Push(ctx, libraryItemID, priority)
	}
}

func (q *dynamicWorkQueue) Depth(ctx context.Context) int64 {
	q.mu.RLock()
	inner := q.inner
	q.mu.RUnlock()
	if inner == nil {
		return 0
	}
	return inner.Depth(ctx)
}

func (q *dynamicWorkQueue) Pause(ctx context.Context) error {
	q.mu.RLock()
	inner := q.inner
	q.mu.RUnlock()
	if inner == nil {
		return nil
	}
	return inner.Pause(ctx)
}

func (q *dynamicWorkQueue) Resume(ctx context.Context) error {
	q.mu.RLock()
	inner := q.inner
	q.mu.RUnlock()
	if inner == nil {
		return nil
	}
	return inner.Resume(ctx)
}

func (q *dynamicWorkQueue) IsPaused(ctx context.Context) (bool, error) {
	q.mu.RLock()
	inner := q.inner
	q.mu.RUnlock()
	if inner == nil {
		return false, nil
	}
	return inner.IsPaused(ctx)
}

// Start begins processing the queue with fn as the per-item handler and
// blocks until ctx is canceled. A second call while already started simply
// blocks on ctx without starting another worker loop, since the queue
// supports only one active handler at a time; Resize is the mechanism for
// changing worker count after Start.
func (q *dynamicWorkQueue) Start(ctx context.Context, fn func(context.Context, int64)) error {
	q.mu.Lock()
	if q.started {
		q.mu.Unlock()
		<-ctx.Done()
		return nil
	}
	q.started = true
	q.rootCtx = ctx
	q.handler = fn
	if err := q.startCurrentLocked(); err != nil {
		q.mu.Unlock()
		return err
	}
	q.mu.Unlock()

	<-ctx.Done()

	q.mu.Lock()
	cancel := q.workerCancel
	done := q.workerDone
	q.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	return nil
}

// Resize rebuilds the underlying workflow.WorkQueue with a new worker count
// and, if the queue has already been started, atomically swaps the running
// worker loop over to it. The new queue inherits the previous queue's
// paused state so a resize can never silently resume a queue an operator
// deliberately paused. A no-op if workers is unchanged. The old worker loop
// is canceled after the new one is live and its shutdown is awaited in the
// background so Resize itself does not block on it.
func (q *dynamicWorkQueue) Resize(workers int) error {
	if workers < 1 {
		workers = 1
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if workers == q.workers {
		return nil
	}

	newInner, err := workflow.NewWorkQueue(workers, q.queueClient, q.workerClient)
	if err != nil {
		return fmt.Errorf("resize workqueue: %w", err)
	}
	paused := false
	if q.inner != nil {
		paused, _ = q.inner.IsPaused(context.Background())
	}
	if paused {
		_ = newInner.Pause(context.Background())
	}

	oldCancel := q.workerCancel
	oldDone := q.workerDone

	q.inner = newInner
	q.workers = workers
	if q.started {
		if err := q.startCurrentLocked(); err != nil {
			return err
		}
	}
	if oldCancel != nil {
		oldCancel()
	}
	if oldDone != nil {
		go func(done chan struct{}) { <-done }(oldDone)
	}
	q.logger.Info().Int("workers", workers).Msg("workqueue: resized worker pool")
	return nil
}

// startCurrentLocked launches the worker loop for q.inner under a fresh
// cancelable context derived from q.rootCtx, recording the cancel function and
// completion channel so a later Resize or Start-triggered shutdown can stop it
// and wait for it to exit. Callers must hold q.mu.
func (q *dynamicWorkQueue) startCurrentLocked() error {
	if q.inner == nil {
		return nil
	}
	workerCtx, cancel := context.WithCancel(q.rootCtx)
	done := make(chan struct{})
	inner := q.inner
	handler := q.handler
	go func() {
		defer close(done)
		if err := inner.Start(workerCtx, handler); err != nil && err != context.Canceled {
			q.logger.Error().Err(err).Msg("workqueue: worker stopped unexpectedly")
		}
	}()
	q.workerCancel = cancel
	q.workerDone = done
	return nil
}

// privacyManagerConfig maps the persisted config.PrivacyConfig into the
// privacy package's own Config shape -- kept as a translation at the edge
// so internal/privacy has no dependency on internal/config.
func privacyManagerConfig(cfg config.PrivacyConfig) privacy.Config {
	return privacy.Config{
		Mode: privacy.Mode(cfg.Mode),
		SOCKS5: privacy.SOCKS5Config{
			Host:           cfg.SOCKS5.Host,
			Port:           cfg.SOCKS5.Port,
			Username:       cfg.SOCKS5.Username,
			Password:       cfg.SOCKS5.Password,
			TimeoutSeconds: cfg.SOCKS5.TimeoutSeconds,
		},
		WireGuardConfigText:     cfg.WireGuard.ConfigText,
		WireGuardTimeoutSeconds: cfg.WireGuard.TimeoutSeconds,
	}
}

// liveSettingsController applies a newly-saved config.Settings to every
// already-running component that depends on it, so a settings update takes
// effect immediately without a process restart. It holds references to
// exactly the subset of the application's live components that expose a
// SetConfig/Reload/Resize-style hook; components not listed here only pick
// up new settings on next startup.
type liveSettingsController struct {
	rt             config.Runtime
	startedAt      time.Time
	status         *runtimeStatus
	taskSchedules  *taskScheduleStatusService
	workflowSvc    *workflow.Service
	hydraClient    *hydra.Client
	workQueue      *dynamicWorkQueue
	recentTasks    *recurringTaskManager
	privacyMgr     *privacy.Manager
	nzbFetcher     *workflow.HTTPNZBFetcher
	articleSrc     *dynamicArticleSource
	readAhead      *stream.ReadAheadManager
	seerrClient    *seerr.Client
	tmdbClient     *tmdb.Client
	tvdbClient     *tvdb.Client
	plexClient     *plex.Client
	jellyfinClient *jellyfin.Client
	notifier       *notifications.Notifier
	rcloneClient   *rclone.Client
}

// ApplySettings pushes cfg into every live component this controller
// manages -- privacy routing, indexer clients, the Usenet fetch pipeline,
// the background search worker pool, and recurring task schedules -- so a
// settings save takes effect across the running application without a
// restart. Called unconditionally on every settings save regardless of what
// changed; individual components (e.g. dynamicArticleSource.Rebuild) are
// responsible for no-oping when their own relevant slice of cfg is
// unchanged. Returns the first error encountered, at which point later
// components in the sequence are not updated for this call.
func (c *liveSettingsController) ApplySettings(ctx context.Context, cfg config.Settings) error {
	if c == nil {
		return nil
	}
	observability.SetGlobalLevel(observability.Level(cfg.Logging.Level))
	if c.privacyMgr != nil {
		if err := c.privacyMgr.Reload(ctx, privacyManagerConfig(cfg.Privacy)); err != nil {
			return fmt.Errorf("privacy routing: %w", err)
		}
	}
	if c.nzbFetcher != nil {
		c.nzbFetcher.SetExcludedIndexers(cfg.Privacy.ExcludedIndexers)
		c.nzbFetcher.SetLocalHost(hostOf(cfg.NZBHydra2.URL))
	}
	if c.hydraClient != nil {
		c.hydraClient.SetSearchDelay(time.Duration(cfg.Indexer.SearchDelayMs) * time.Millisecond)
		c.hydraClient.SetConfig(cfg.NZBHydra2)
		{
			// Run unconditionally, matching ApplySettings' overall
			// "reapply everything on every save" convention -- when the
			// checkbox is off (or mode isn't SOCKS5), enabled is false and
			// NZBHydra2's own proxy is actively cleared back to "no proxy",
			// not just left alone. Best-effort and asynchronous: NZBHydra2
			// being briefly unreachable shouldn't block a settings save or
			// fail the rest of this reload sequence.
			hydraClient := c.hydraClient
			enabled := cfg.Privacy.SyncNZBHydra2Proxy && cfg.Privacy.Mode == config.PrivacyModeSOCKS5
			proxy := hydra.ProxyConfig{
				Host:     cfg.Privacy.SOCKS5.Host,
				Port:     cfg.Privacy.SOCKS5.Port,
				Username: cfg.Privacy.SOCKS5.Username,
				Password: cfg.Privacy.SOCKS5.Password,
			}
			go func() {
				syncCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if err := hydraClient.SyncProxy(syncCtx, enabled, proxy); err != nil {
					slog.Warn("nzbhydra2 proxy sync failed", "error", err)
				}
			}()
		}
	}
	if c.seerrClient != nil {
		c.seerrClient.SetConfig(cfg.Seerr)
	}
	if c.tmdbClient != nil {
		c.tmdbClient.SetConfig(cfg.Metadata)
	}
	if c.tvdbClient != nil {
		c.tvdbClient.SetConfig(cfg.Metadata)
	}
	if c.plexClient != nil {
		c.plexClient.SetConfig(cfg.Plex.URL, cfg.Plex.Token)
	}
	if c.jellyfinClient != nil {
		c.jellyfinClient.SetConfig(cfg.Jellyfin.URL, cfg.Jellyfin.APIKey)
	}
	if c.notifier != nil {
		c.notifier.SetConfig(notifications.Config{
			DiscordWebhookURL: cfg.Notifications.DiscordWebhookURL,
			GenericWebhookURL: cfg.Notifications.GenericWebhookURL,
			OnGrab:            cfg.Notifications.OnGrab,
			OnAvailable:       cfg.Notifications.OnAvailable,
			OnFailed:          cfg.Notifications.OnFailed,
		})
	}
	if c.rcloneClient != nil {
		c.rcloneClient.SetConfig(cfg.Rclone.RCAddr)
	}
	if c.articleSrc != nil {
		c.articleSrc.Rebuild(ctx, usenetConfigForPrivacyMode(cfg.Usenet, c.privacyMgr.Mode()), c.privacyMgr)
		if c.readAhead != nil {
			c.readAhead.SetArticleBufferSize(cfg.Usenet.ArticleBufferSize)
			if maxConns, pct := c.articleSrc.ConnectionBudget(); maxConns > 0 {
				c.readAhead.SetConnectionBudget(maxConns, pct)
			}
		}
	}
	if c.workflowSvc != nil {
		c.workflowSvc.SetIndexerLimits(workflow.IndexerLimits{
			MinimumAgeMinutes: cfg.Indexer.MinimumAgeMinutes,
			RetentionDays:     cfg.Indexer.RetentionDays,
			MaximumSizeMB:     cfg.Indexer.MaximumSizeMB,
			ReleaseGraceHours: cfg.Indexer.ReleaseGraceHours,
		})
	}
	if c.workQueue != nil {
		if err := c.workQueue.Resize(cfg.Indexer.BackgroundSearchWorkers); err != nil {
			return err
		}
	}
	if c.taskSchedules != nil {
		c.taskSchedules.SetRSSIntervals(cfg.Indexer.TvRssSyncIntervalMinutes, cfg.Indexer.MovieRssSyncIntervalMinutes)
	}
	if c.recentTasks != nil {
		c.recentTasks.Reschedule(maintenanceRecentTVTask, boundedTVRSSInterval(cfg.Indexer.TvRssSyncIntervalMinutes))
		c.recentTasks.Reschedule(maintenanceRecentMovieTask, boundedMovieRSSInterval(cfg.Indexer.MovieRssSyncIntervalMinutes))
	}
	if c.status != nil {
		current := c.status.Status()
		c.status.SetStatus(api.StatusFromConfig(c.rt, cfg, c.startedAt, current.Healthy))
	}
	return nil
}

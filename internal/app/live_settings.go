package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hjongedijk/drakkar/internal/api"
	"github.com/hjongedijk/drakkar/internal/config"
	"github.com/hjongedijk/drakkar/internal/hydra"
	"github.com/hjongedijk/drakkar/internal/workflow"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

type recurringTaskManager struct {
	rootCtx context.Context
	logger  zerolog.Logger

	mu    sync.Mutex
	tasks map[string]*managedRecurringTask
}

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

func (m *recurringTaskManager) startLocked(task *managedRecurringTask) {
	runCtx, cancel := context.WithCancel(m.rootCtx)
	task.cancel = cancel
	go func(interval time.Duration, runOnStartup bool, fn func()) {
		if runOnStartup {
			fn()
		}
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-timer.C:
				fn()
				timer.Reset(interval)
			}
		}
	}(task.interval, task.runOnStartup, task.fn)
	m.logger.Info().Str("task", task.name).Dur("interval", task.interval).Bool("startup", task.runOnStartup).Msg("scheduler: task started")
}

type dynamicWorkQueue struct {
	queueClient  redis.Cmdable
	workerClient redis.Cmdable
	logger       zerolog.Logger

	mu           sync.RWMutex
	inner        *workflow.WorkQueue
	workers      int
	rootCtx      context.Context
	handler      func(context.Context, int64)
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

type liveSettingsController struct {
	rt            config.Runtime
	startedAt     time.Time
	status        *runtimeStatus
	taskSchedules *taskScheduleStatusService
	workflowSvc   *workflow.Service
	hydraClient   *hydra.Client
	workQueue     *dynamicWorkQueue
	recentTasks   *recurringTaskManager
}

func (c *liveSettingsController) ApplySettings(_ context.Context, cfg config.Settings) error {
	if c == nil {
		return nil
	}
	if c.hydraClient != nil {
		c.hydraClient.SetSearchDelay(time.Duration(cfg.Indexer.SearchDelayMs) * time.Millisecond)
	}
	if c.workflowSvc != nil {
		c.workflowSvc.SetIndexerLimits(workflow.IndexerLimits{
			MinimumAgeMinutes: cfg.Indexer.MinimumAgeMinutes,
			RetentionDays:     cfg.Indexer.RetentionDays,
			MaximumSizeMB:     cfg.Indexer.MaximumSizeMB,
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

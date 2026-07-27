package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	gobullmq "go.codycody31.dev/gobullmq"
)

const bullmqQueueName = "drakkar:search"

// WorkQueuer is the interface consumed by Service. The real implementation
// is backed by BullMQ/Redis; tests can provide a lightweight stub.
type WorkQueuer interface {
	Push(ctx context.Context, libraryItemID int64, priority int)
	Depth(ctx context.Context) int64
	Pause(ctx context.Context) error
	Resume(ctx context.Context) error
	IsPaused(ctx context.Context) (bool, error)
	Start(ctx context.Context, fn func(ctx context.Context, libraryItemID int64)) error
}

type searchJob struct {
	LibraryItemID int64 `json:"libraryItemId"`
}

// WorkQueue wraps a BullMQ-backed queue for library item search jobs.
// Jobs are deduplicated by library item ID — pushing an item already waiting
// in the queue is a no-op (BullMQ ignores duplicate job IDs).
//
// Per gobullmq docs, the queue client and worker client must be separate
// Redis connections to avoid CLIENT SETNAME collisions.
type WorkQueue struct {
	queue        *gobullmq.Queue[searchJob]
	queueClient  redis.Cmdable // dedicated to Queue.Add calls
	workerClient redis.Cmdable // dedicated to Worker (separate connection)
	workers      int
}

// NewWorkQueue creates a BullMQ-backed work queue.
// queueClient is used for enqueueing; workerClient is used by the worker pool.
// Pass two separate *redis.Client instances to avoid CLIENT SETNAME collisions.
func NewWorkQueue(workers int, queueClient, workerClient redis.Cmdable) (*WorkQueue, error) {
	if workers < 1 {
		workers = 1
	}
	q, err := gobullmq.NewQueue[searchJob](bullmqQueueName, queueClient, &gobullmq.QueueOptions{
		Prefix: "bull",
	})
	if err != nil {
		return nil, fmt.Errorf("workqueue: create queue: %w", err)
	}
	return &WorkQueue{
		queue:        q,
		queueClient:  queueClient,
		workerClient: workerClient,
		workers:      workers,
	}, nil
}

// Push enqueues a library item search job. Lower caller priority values are
// more urgent (0 beats 10), matching the workflow service call sites. BullMQ
// uses 1 as its highest explicit priority, so we shift the caller value by 1.
// Duplicate job IDs are ignored by BullMQ, so pushing an already-queued item
// is safe and cheap.
func (q *WorkQueue) Push(ctx context.Context, libraryItemID int64, priority int) {
	bullPriority := toBullPriority(priority)
	_, _ = q.queue.Add(ctx, "search", searchJob{LibraryItemID: libraryItemID},
		gobullmq.AddWithJobID(fmt.Sprintf("%d", libraryItemID)),
		gobullmq.AddWithPriority(bullPriority),
		gobullmq.AddWithRemoveOnComplete(),
		gobullmq.AddWithRemoveOnFail(),
	)
}

// toBullPriority converts the workflow service's priority convention (lower is
// more urgent, negative values allowed) into BullMQ's convention (1 is the
// highest priority, 0 is not a valid explicit priority).
func toBullPriority(priority int) int {
	if priority < 0 {
		priority = 0
	}
	return priority + 1
}

// Depth returns the number of jobs currently waiting in the queue.
func (q *WorkQueue) Depth(ctx context.Context) int64 {
	n, _ := q.queue.GetWaitingCount(ctx)
	return n
}

// Pause stops the queue from handing out new jobs to workers. Jobs already
// in flight are not affected.
//
// gobullmq's underlying Lua script (internal/lua/pause_lua.go, generated,
// not ours to edit) performs the pause correctly -- renaming the wait key,
// setting the "paused" flag, publishing the event -- but has no explicit
// Lua `return`, so Redis replies with a nil bulk reply that go-redis's
// Eval().Result() surfaces as the redis.Nil sentinel error. Confirmed live:
// every Pause/Resume call failed with "redis: nil" even though the pause
// itself visibly took effect. redis.Nil here is not a real failure, so it
// is swallowed; any other error still propagates.
func (q *WorkQueue) Pause(ctx context.Context) error {
	if err := q.queue.Pause(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	return nil
}

// Resume reverses a prior Pause, allowing the queue to hand out jobs again.
// See Pause's doc comment for why a redis.Nil error is swallowed here too.
func (q *WorkQueue) Resume(ctx context.Context) error {
	if err := q.queue.Resume(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	return nil
}

// IsPaused reports whether the queue is currently paused.
func (q *WorkQueue) IsPaused(ctx context.Context) (bool, error) {
	return q.queue.IsPaused(ctx)
}

// Start launches the BullMQ worker pool. Blocks until ctx is cancelled.
func (q *WorkQueue) Start(ctx context.Context, fn func(ctx context.Context, libraryItemID int64)) error {
	processor := func(ctx context.Context, job *gobullmq.Job[searchJob]) (struct{}, error) {
		fn(ctx, job.Data().LibraryItemID)
		return struct{}{}, nil
	}
	worker, err := gobullmq.NewWorker[searchJob, struct{}](
		bullmqQueueName,
		q.workerClient,
		processor,
		&gobullmq.WorkerOptions{
			Concurrency:      q.workers,
			RemoveOnComplete: &gobullmq.KeepJobs{Count: 0},
			RemoveOnFail:     &gobullmq.KeepJobs{Count: 0},
			// Search jobs complete in seconds (NZBHydra HTTP call + async download
			// dispatch). A 2-minute lock is more than sufficient and ensures stalled
			// jobs are detected and re-queued quickly instead of blocking dedup for
			// 30 minutes. Lock renewal runs every LockDuration/4 = 30s.
			LockDuration:    2 * time.Minute,
			StalledInterval: 1 * time.Minute,
			// Allow one recovery attempt before failing: if the stalled check fires
			// while the lock is briefly between renewals, the job is moved back to
			// wait instead of immediately to failed (MaxStalledCount=0 default).
			MaxStalledCount: 2,
		},
	)
	if err != nil {
		return fmt.Errorf("workqueue: create worker: %w", err)
	}
	return worker.Run(ctx)
}

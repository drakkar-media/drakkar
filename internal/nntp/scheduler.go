package nntp

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/drakkar-media/drakkar/internal/observability"
	"github.com/drakkar-media/drakkar/internal/stream"
)

// defaultSlowFetchThreshold is how long a single article fetch/stat may take
// before handleRequestProtected logs it as suspiciously slow.
//
// Added during the 2026-08-09 A/V-sync-delay investigation: a live repro
// showed a ~6-11s stall before a fresh read served any bytes, with no error
// anywhere in the chain (direct provider connectivity, Drakkar's own webdav
// responses, and the DB were all independently confirmed fast) except one
// rclone-side "i/o timeout, low level retry" for the same file caught by
// chance in the logs -- i.e. something in this fetch path occasionally stalls
// for several seconds without ever producing a log line pointing at it. Every
// article fetch/stat, on every priority tier, passes through
// handleRequestProtected, making it the one choke point where timing every
// single request (rather than guessing which layer is slow) will catch the
// next occurrence with an actual messageID/priority/duration to chase.
const defaultSlowFetchThreshold = 2 * time.Second

// ErrSchedulerQueueFull is returned when a priority tier's queue is at
// capacity and cannot accept another request. Callers should treat this as a
// transient backpressure signal, not a fetch failure.
var ErrSchedulerQueueFull = errors.New("nntp scheduler queue full")

// ScheduledSource dispatches NNTP article fetches using a three-tier priority
// queue split across three independent worker lanes, one per tier:
//
//	high   (priority ≥ Interactive=100) — direct player reads            -- interactive lane
//	medium (priority ≥ ReadAhead=80)   — speculative prefetch            -- read-ahead lane
//	low    (priority < 80)             — background calibration / checks -- background lane
//
// Drakkar's calibration/health-check does a full body fetch+decode (a much
// heavier operation than a cheap STAT-only check), so giving it the full
// pool ceiling the way a lightweight STAT check safely could would
// reintroduce the over-concurrency that caused corrupted reads under heavy
// load (see calibrate.go's confirmPermanentCRCMismatch and the 2026-07-19
// incident). Instead it gets its own separate, independently-sized worker
// lane: never blocked behind interactive/read-ahead traffic, but still
// bounded, not run at up to the full account connection ceiling.
//
// interactive and read-ahead used to share one "foreground" lane/worker
// pool, with priority ordering (high checked before medium) as the only
// thing protecting interactive reads. That protects queued-but-not-yet-
// dispatched requests, but not against a worker already mid-fetch: once
// every foreground worker is busy processing NNTP round trips -- read-ahead
// ramped up toward its parallelism share is enough on its own to do this --
// a new interactive fetch has nothing to preempt and must wait for one to
// finish. Confirmed live (2026-08-10): a lingering pre-seek session's
// read-ahead (plus, before the two connected fixes shipped, its own
// foreground reads) occupied the shared lane while a new seek's interactive
// fetch queued behind it. Splitting interactive into its own dedicated
// lane means it always has guaranteed worker capacity that read-ahead can
// never occupy, at any load.
type ScheduledSource struct {
	source ArticleSource
	high   chan fetchRequest
	medium chan fetchRequest
	low    chan fetchRequest
	cancel context.CancelFunc

	// slowFetchThreshold is a per-instance field (not a package-level var) so
	// tests can tune it on their own ScheduledSource without racing a
	// dangling worker goroutine left running by some other, unrelated test
	// past its own test function's return -- see defaultSlowFetchThreshold's
	// doc comment for why this value exists at all.
	slowFetchThreshold time.Duration
}

type fetchRequest struct {
	ctx       context.Context
	messageID string
	priority  stream.FetchPriority
	op        fetchOperation
	resultCh  chan fetchResult
}

type fetchResult struct {
	body []byte
	err  error
}

type fetchOperation uint8

const (
	fetchOperationBody fetchOperation = iota
	fetchOperationStat
)

// NewScheduledSource starts a single shared worker pool serving all three
// tiers in priority order -- the simple, unsplit form used by tests that
// don't care about lane isolation. See NewScheduledSourceLanes for the real
// three-lane split production actually uses.
func NewScheduledSource(ctx context.Context, source ArticleSource, workers int, queueSize int) *ScheduledSource {
	return NewScheduledSourceLanes(ctx, source, workers, 0, 0, queueSize)
}

// NewScheduledSourceLanes starts three independent worker lanes --
// interactiveWorkers serving only high, readAheadWorkers serving only
// medium, backgroundWorkers serving only low -- so none of the three tiers
// can ever occupy another's dedicated capacity. See the ScheduledSource doc
// comment for why interactive and read-ahead must not share a lane, and why
// background needs its own separate one too.
//
// If backgroundWorkers <= 0, the read-ahead/background split is skipped
// entirely and interactiveWorkers goroutines serve all three tiers from one
// shared pool in priority order (the pre-three-lane-split behaviour), so
// simple callers (NewScheduledSource, most tests) that don't care about lane
// isolation keep working with a single worker-count knob.
func NewScheduledSourceLanes(ctx context.Context, source ArticleSource, interactiveWorkers, readAheadWorkers, backgroundWorkers, queueSize int) *ScheduledSource {
	if interactiveWorkers <= 0 {
		interactiveWorkers = 1
	}
	if queueSize <= 0 {
		queueSize = (interactiveWorkers + max(readAheadWorkers, 1) + max(backgroundWorkers, 1)) * 4
	}
	schedCtx, cancel := context.WithCancel(ctx)
	s := &ScheduledSource{
		source:             source,
		high:               make(chan fetchRequest, queueSize),
		medium:             make(chan fetchRequest, queueSize),
		low:                make(chan fetchRequest, queueSize),
		cancel:             cancel,
		slowFetchThreshold: defaultSlowFetchThreshold,
	}
	if backgroundWorkers > 0 {
		if readAheadWorkers <= 0 {
			readAheadWorkers = 1
		}
		for range interactiveWorkers {
			go s.interactiveWorker(schedCtx)
		}
		for range readAheadWorkers {
			go s.readAheadWorker(schedCtx)
		}
		for range backgroundWorkers {
			go s.backgroundWorker(schedCtx)
		}
	} else {
		// No dedicated lane split: fall back to one shared pool serving all
		// three tiers in priority order (matches the pre-split behaviour).
		for range interactiveWorkers {
			go s.worker(schedCtx)
		}
	}
	return s
}

// Close stops every worker goroutine this ScheduledSource owns. Used when
// the whole Usenet provider chain is rebuilt at runtime (e.g. a settings
// change) so the old scheduler's goroutines don't linger forever.
func (s *ScheduledSource) Close() {
	s.cancel()
}

// SetBackgroundBudget is kept for API compatibility but is now a no-op --
// all priorities share the pool and the scheduler's queue ordering provides
// natural priority, with no separate background budget needed.
func (s *ScheduledSource) SetBackgroundBudget(_ int, _ func() int) {}

// Body fetches an article body at the default interactive priority. Equivalent
// to BodyPriority with stream.PriorityInteractive.
func (s *ScheduledSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	return s.BodyPriority(ctx, messageID, stream.PriorityInteractive)
}

// BodyPriority enqueues a fetch on the tier selected by priority (see the
// ScheduledSource doc comment) and blocks until a worker completes it or ctx
// is cancelled.
//
// Errors:
//   - ErrSchedulerQueueFull: the selected tier's queue was at capacity; the
//     request was never enqueued.
func (s *ScheduledSource) BodyPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, error) {
	if s == nil || s.source == nil {
		return nil, errors.New("scheduled source unavailable")
	}
	// Fast-fail: cancelled read-ahead requests must not pile up in the medium
	// queue and delay interactive reads.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	req := fetchRequest{
		ctx:       ctx,
		messageID: messageID,
		priority:  priority,
		op:        fetchOperationBody,
		resultCh:  make(chan fetchResult, 1),
	}
	queue := s.queue(priority)
	select {
	case queue <- req:
	default:
		return nil, ErrSchedulerQueueFull
	}
	select {
	case result := <-req.resultCh:
		return result.body, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Stat enqueues an existence check on the background (low-priority) lane,
// since checks are never as latency-sensitive as an interactive read.
//
// Errors:
//   - ErrSchedulerQueueFull: the background queue was at capacity.
func (s *ScheduledSource) Stat(ctx context.Context, messageID string) error {
	if s == nil || s.source == nil {
		return errors.New("scheduled source unavailable")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	req := fetchRequest{
		ctx:       ctx,
		messageID: messageID,
		priority:  stream.PriorityBackground,
		op:        fetchOperationStat,
		resultCh:  make(chan fetchResult, 1),
	}
	select {
	case s.low <- req:
	default:
		return ErrSchedulerQueueFull
	}
	select {
	case result := <-req.resultCh:
		return result.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *ScheduledSource) queue(priority stream.FetchPriority) chan fetchRequest {
	switch {
	case priority >= stream.PriorityInteractive:
		return s.high
	case priority >= stream.PriorityReadAhead:
		return s.medium
	default:
		return s.low
	}
}

// worker exits when ctx is cancelled (process shutdown) instead of running
// forever. Each request is handled through handleRequestProtected so a panic
// from one bad fetch (e.g. an unexpected yEnc decode failure) is recovered
// per-request rather than ending the whole worker goroutine — after a single
// unrecovered panic, that worker would silently vanish from the pool for the
// rest of the process lifetime.
func (s *ScheduledSource) worker(ctx context.Context) {
	for {
		req, ok := s.next(ctx)
		if !ok {
			return
		}
		s.handleRequestProtected(req)
	}
}

// interactiveWorker only ever serves high (direct player reads) -- its own
// dedicated lane, so a read-ahead burst (or, before v0.2.98, a lingering
// pre-seek session's read-ahead) can never occupy every worker capable of
// serving an interactive fetch. See the ScheduledSource doc comment.
func (s *ScheduledSource) interactiveWorker(ctx context.Context) {
	for {
		select {
		case req := <-s.high:
			s.handleRequestProtected(req)
		case <-ctx.Done():
			return
		}
	}
}

// readAheadWorker only ever serves medium (speculative prefetch) -- its own
// dedicated lane, separately bounded from interactive so it can never starve
// it, and from background so a calibration/health-check burst can't starve
// read-ahead either.
func (s *ScheduledSource) readAheadWorker(ctx context.Context) {
	for {
		select {
		case req := <-s.medium:
			s.handleRequestProtected(req)
		case <-ctx.Done():
			return
		}
	}
}

// backgroundWorker only ever serves low (calibration/health-check) -- its own
// dedicated lane, never blocked behind interactive/read-ahead traffic, and
// separately bounded rather than sharing their worker counts.
func (s *ScheduledSource) backgroundWorker(ctx context.Context) {
	for {
		select {
		case req := <-s.low:
			s.handleRequestProtected(req)
		case <-ctx.Done():
			return
		}
	}
}

// handleRequestProtected executes req against the underlying source and
// delivers the result, dropping already-cancelled requests before they touch
// the connection pool. The resultCh send is non-blocking/select-guarded
// against req.ctx.Done() so a caller that gave up waiting can never wedge a
// worker goroutine.
func (s *ScheduledSource) handleRequestProtected(req fetchRequest) {
	defer observability.Recover("nntp-scheduler-worker")
	// Skip cancelled requests immediately (seek happened, context cancelled)
	// before ever touching the connection pool.
	if req.ctx.Err() != nil {
		select {
		case req.resultCh <- fetchResult{err: req.ctx.Err()}:
		default:
		}
		return
	}
	var (
		body []byte
		err  error
	)
	start := time.Now()
	switch req.op {
	case fetchOperationStat:
		err = fetchArticleStat(req.ctx, s.source, req.messageID, req.priority)
	default:
		body, err = fetchArticleBody(req.ctx, s.source, req.messageID, req.priority)
	}
	if elapsed := time.Since(start); elapsed >= s.slowFetchThreshold {
		slog.Warn("nntp: slow article fetch", "messageID", req.messageID, "priority", req.priority, "op", req.op, "elapsed", elapsed, "err", err)
	}
	select {
	case req.resultCh <- fetchResult{body: body, err: err}:
	case <-req.ctx.Done():
	}
}

// QueueDepths returns the number of pending requests at each priority level.
func (s *ScheduledSource) QueueDepths() (interactive, readAhead, background int) {
	return len(s.high), len(s.medium), len(s.low)
}

// next picks the highest-priority pending request, blocking until one is
// available or ctx is cancelled (process shutdown, reported via ok=false).
// Release order: High → Medium → Low.
func (s *ScheduledSource) next(ctx context.Context) (req fetchRequest, ok bool) {
	for {
		select {
		case req := <-s.high:
			return req, true
		default:
		}
		select {
		case req := <-s.high:
			return req, true
		case req := <-s.medium:
			return req, true
		default:
		}
		select {
		case req := <-s.high:
			return req, true
		case req := <-s.medium:
			return req, true
		case req := <-s.low:
			return req, true
		case <-ctx.Done():
			return fetchRequest{}, false
		}
	}
}

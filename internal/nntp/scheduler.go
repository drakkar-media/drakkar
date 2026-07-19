package nntp

import (
	"context"
	"errors"

	"github.com/drakkar-media/drakkar/internal/observability"
	"github.com/drakkar-media/drakkar/internal/stream"
)

var ErrSchedulerQueueFull = errors.New("nntp scheduler queue full")

// ScheduledSource dispatches NNTP article fetches using a three-tier priority
// queue split across two independent worker lanes, matching nzbdav's actual
// architecture: nzbdav's DownloadingNntpClient PrioritizedSemaphore covers
// only download/streaming BODY fetches (capped at "Max Download
// Connections"), while its health check (STAT-only, no body fetch/decode)
// bypasses that semaphore entirely and uses the raw connection pool directly
// (HealthCheckService.cs) so it's never blocked behind download/streaming
// traffic.
//
//	high   (priority ≥ Interactive=100) — direct player reads     -- foreground lane
//	medium (priority ≥ ReadAhead=80)   — speculative prefetch     -- foreground lane
//	low    (priority < 80)             — background calibration / checks -- background lane
//
// Drakkar's calibration/health-check does a full body fetch+decode (unlike
// nzbdav's cheap STAT-only check), so giving it the full pool ceiling the way
// nzbdav does its STAT checks would reintroduce the over-concurrency that
// caused corrupted reads under heavy load (see calibrate.go's
// confirmPermanentCRCMismatch and the 2026-07-19 incident). Instead it gets
// its own separate, independently-sized worker lane: never blocked behind
// foreground (high/medium) traffic, but still bounded, not run at up to the
// full account connection ceiling.
type ScheduledSource struct {
	source ArticleSource
	high   chan fetchRequest
	medium chan fetchRequest
	low    chan fetchRequest
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

// NewScheduledSource starts two independent worker lanes: `workers` goroutines
// serving only high/medium (foreground: interactive + read-ahead), and
// `backgroundWorkers` goroutines serving only low (background: calibration /
// health-check) — see the ScheduledSource doc comment for why these must not
// share a pool. If backgroundWorkers <= 0, low falls back to being served by
// the foreground workers too (old behaviour), so existing callers that don't
// care about the split keep working.
func NewScheduledSource(ctx context.Context, source ArticleSource, workers int, queueSize int) *ScheduledSource {
	return NewScheduledSourceLanes(ctx, source, workers, 0, queueSize)
}

// NewScheduledSourceLanes is NewScheduledSource with an explicit, independent
// background-lane worker count. See the ScheduledSource doc comment.
func NewScheduledSourceLanes(ctx context.Context, source ArticleSource, workers, backgroundWorkers, queueSize int) *ScheduledSource {
	if workers <= 0 {
		workers = 1
	}
	if queueSize <= 0 {
		queueSize = workers * 4
	}
	s := &ScheduledSource{
		source: source,
		high:   make(chan fetchRequest, queueSize),
		medium: make(chan fetchRequest, queueSize),
		low:    make(chan fetchRequest, queueSize),
	}
	if backgroundWorkers > 0 {
		for range workers {
			go s.foregroundWorker(ctx)
		}
		for range backgroundWorkers {
			go s.backgroundWorker(ctx)
		}
	} else {
		// No dedicated background lane: fall back to one shared pool serving
		// all three tiers in priority order (matches the pre-split behaviour).
		for range workers {
			go s.worker(ctx)
		}
	}
	return s
}

// SetBackgroundBudget is kept for API compatibility but is now a no-op.
// nzbdav has no separate background budget — all priorities share the pool
// and the scheduler's queue ordering provides natural priority.
func (s *ScheduledSource) SetBackgroundBudget(_ int, _ func() int) {}

func (s *ScheduledSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	return s.BodyPriority(ctx, messageID, stream.PriorityInteractive)
}

func (s *ScheduledSource) BodyPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, error) {
	if s == nil || s.source == nil {
		return nil, errors.New("scheduled source unavailable")
	}
	// Fast-fail: cancelled read-ahead requests must not pile up in the medium
	// queue and delay interactive reads (matches nzbdav's cancellation path).
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

// foregroundWorker only ever serves high/medium (interactive + read-ahead) --
// used when a dedicated background lane exists, so low-priority
// calibration/health-check work can never occupy a foreground worker slot.
func (s *ScheduledSource) foregroundWorker(ctx context.Context) {
	for {
		req, ok := s.nextForeground(ctx)
		if !ok {
			return
		}
		s.handleRequestProtected(req)
	}
}

// backgroundWorker only ever serves low (calibration/health-check) -- its own
// dedicated lane, never blocked behind foreground traffic, and separately
// bounded rather than sharing the foreground worker count.
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

// nextForeground picks the highest-priority pending high/medium request,
// blocking until one is available or ctx is cancelled. Mirrors next but never
// looks at low -- see foregroundWorker.
func (s *ScheduledSource) nextForeground(ctx context.Context) (req fetchRequest, ok bool) {
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
	case <-ctx.Done():
		return fetchRequest{}, false
	}
}

func (s *ScheduledSource) handleRequestProtected(req fetchRequest) {
	defer observability.Recover("nntp-scheduler-worker")
	// Skip cancelled requests immediately (seek happened, context cancelled).
	// nzbdav removes cancelled waiters from the semaphore queue; we do the
	// same here before touching the connection pool.
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
	switch req.op {
	case fetchOperationStat:
		err = fetchArticleStat(req.ctx, s.source, req.messageID)
	default:
		body, err = fetchArticleBody(req.ctx, s.source, req.messageID, req.priority)
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
// This mirrors nzbdav's PrioritizedSemaphore release order: High → Medium → Low.
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

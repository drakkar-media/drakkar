package stream

import (
	"context"
	"sync"
	"time"

	"github.com/drakkar-media/drakkar/internal/metrics"
)

// Read-ahead fetches always run at PriorityReadAhead (80), strictly below
// PriorityInteractive (100) used for the player's actual current-position
// reads -- the priority queue, not this ceiling, is what protects live
// playback from being starved by prefetch. That means this ceiling only
// needs to bound resource usage, not fight interactive reads for a slot, so
// it can scale with the user's own configured connection budget rather than
// sitting at a flat conservative cap regardless of it. A low ceiling here
// was found to bottleneck sustained throughput for high-bitrate remux/UHD
// content: measured live against a genuine ~84.5 Mbps 2160p remux
// (19.46GB / 30m42s), sustained fetch throughput topped out around 72-76
// Mbps at parallelism=30 -- short of the required rate, so the read-ahead
// buffer can never build a cushion and playback stalls repeatedly. Raised
// to 60 to test whether more concurrent segment fetches close that gap or
// whether the ceiling is actually the provider's own account-level
// bandwidth cap (in which case this won't help).
const (
	defaultMaxReadAheadParallelism  = 4
	absoluteMaxReadAheadParallelism = 60
	minReadAheadParallelism         = 1
	defaultArticleBufferSize        = 40

	// readAheadRampWindows is how many read-ahead windows a session ramps up
	// over before reaching its full per-stream parallelism share. Added
	// while chasing a live A/V-sync-delay report on Venom: Let There Be
	// Carnage (2026-08-09): visible block/stutter artifacts right at stream
	// start, immediately followed by a persistent desync that only a seek
	// (not a pause/resume) fixed -- the signature of a genuine data-delivery
	// hiccup at open, not a per-file decode bug (both the RAR header-skip
	// detection and the yEnc segment-length calibration for this exact
	// release were independently verified correct against the real fetched
	// bytes). A live repro caught every priority tier (interactive,
	// read-ahead, AND the health-check's own background probe) going slow
	// simultaneously, together with unrelated titles' health-check probes
	// failing in the same few seconds -- the same signature this codebase's
	// health-check pacing comment already attributes to provider-side rate
	// limiting triggered by aggregate connection/request load. A single
	// stream jumping straight from 0 to its full parallelism share
	// (routinely ~20 connections) in one instant, right at the moment
	// read-ahead first fills its window, is exactly the kind of spike that
	// can trip that kind of throttling. Ramping the first few windows up
	// gradually doesn't lower the eventual ceiling (full parallelism is
	// reached within readAheadRampWindows real reads, typically well under
	// a second of playback), it just avoids bursting to it in one instant.
	readAheadRampWindows = 4

	// readAheadRampFloor is the concurrency a session's very first read-ahead
	// window starts at -- and, critically, the ramp never kicks in at all
	// when the stream's own full share is already at or below this (e.g. a
	// low connection budget, or many concurrent streams splitting a modest
	// total). The spike this ramp exists to avoid only happens when a
	// single window's fetch count jumps by a lot in one instant; a share
	// that's already small has nothing meaningful to spike from.
	readAheadRampFloor = 4
)

// FetchPriority orders competing segment fetches so interactive playback
// reads are never starved by prefetch or background work.
type FetchPriority int

// Higher values win contention for fetch capacity. Read-ahead always fetches
// below interactive playback reads (see the const block's leading comment
// for why the read-ahead parallelism ceiling doesn't need to reserve
// capacity for interactive reads itself).
const (
	PriorityBackground  FetchPriority = 10
	PriorityReadAhead   FetchPriority = 80
	PriorityInteractive FetchPriority = 100
)

// PrioritySegmentFetcher extends SegmentFetcher with a priority-aware fetch,
// used by read-ahead so its prefetch requests never outrank the player's
// interactive reads for the same underlying NNTP connection budget.
type PrioritySegmentFetcher interface {
	SegmentFetcher
	FetchRangePriority(ctx context.Context, segment SegmentRange, priority FetchPriority) ([]byte, error)
}

// SessionVirtualMediaFile extends VirtualMediaFile with the session
// lifecycle hooks needed to drive read-ahead prefetch for one open stream.
// Implementations must tolerate being called on a nil receiver (StartSession
// et al. are no-ops in that case) since not every VirtualMediaFile
// implementation supports sessions.
type SessionVirtualMediaFile interface {
	VirtualMediaFile
	StartSession(sessionID string)
	NotifyRead(sessionID string, offset int64)
	Seek(sessionID string, offset int64)
	StopSession(sessionID string)
	RegisterMeta(sessionID string, meta SessionMeta)
}

// ReadAheadManager tracks active stream sessions and schedules background
// prefetch windows ahead of each session's current read position.
//
// Prefetch parallelism is shared across all active sessions (see schedule)
// and is bounded by SetConnectionBudget so read-ahead cannot exceed the
// operator's configured NNTP connection budget. Safe for concurrent use,
// including on a nil *ReadAheadManager (all methods are no-ops in that
// case), so callers with an optional manager don't need nil checks.
type ReadAheadManager struct {
	windowBytes int64

	mu             sync.Mutex
	sessions       map[string]*readAheadSession
	maxParallelism int
	articleLimit   int
}

// SessionMeta carries display metadata for an open stream session.
type SessionMeta struct {
	VirtualFileID int64
	FileName      string
	FileSizeBytes int64
	OpenedAt      time.Time
}

// SessionSnapshot is a point-in-time view of one active session, safe to read outside the lock.
type SessionSnapshot struct {
	SessionID     string    `json:"sessionId"`
	VirtualFileID int64     `json:"virtualFileId"`
	FileName      string    `json:"fileName"`
	FileSizeBytes int64     `json:"fileSizeBytes"`
	OpenedAt      time.Time `json:"openedAt"`
	CurrentOffset int64     `json:"currentOffset"`
}

type readAheadSession struct {
	spans          []SegmentSpan
	fetcher        PrioritySegmentFetcher
	cancel         context.CancelFunc
	meta           SessionMeta
	currentOffset  int64
	windowsStarted int
}

// NewReadAheadManager creates a ReadAheadManager that prefetches up to
// windowBytes ahead of each session's current read position. A negative
// windowBytes is clamped to 0, which disables prefetch (schedule becomes a
// no-op) while still tracking sessions for ActiveSessions/ActiveCount.
func NewReadAheadManager(windowBytes int64) *ReadAheadManager {
	if windowBytes < 0 {
		windowBytes = 0
	}
	return &ReadAheadManager{
		windowBytes:    windowBytes,
		sessions:       make(map[string]*readAheadSession),
		maxParallelism: defaultMaxReadAheadParallelism,
		articleLimit:   defaultArticleBufferSize,
	}
}

// SetConnectionBudget sizes read-ahead parallelism off a share of total NNTP
// concurrency. Interactive playback reads are protected by priority (100 vs
// read-ahead's 80), not by starving this budget, so it can afford to be a
// real fraction of streamingBudget rather than a token slice of it.
func (m *ReadAheadManager) SetConnectionBudget(totalConnections int, streamingPriorityPct int) {
	if m == nil {
		return
	}
	limit := defaultMaxReadAheadParallelism
	if totalConnections > 0 {
		if streamingPriorityPct <= 0 || streamingPriorityPct > 100 {
			streamingPriorityPct = 80
		}
		streamingBudget := int(float64(totalConnections) * float64(streamingPriorityPct) / 100.0)
		if streamingBudget < 1 {
			streamingBudget = 1
		}
		// Previously halved here, but that left real throughput ceiling
		// untested for high-bitrate remux content (see the const block
		// above) -- interactive reads are already protected by priority,
		// not by holding back a reserved share of the budget, so give
		// read-ahead the full streaming budget.
		limit = streamingBudget
		if limit < minReadAheadParallelism {
			limit = minReadAheadParallelism
		}
		if limit > absoluteMaxReadAheadParallelism {
			limit = absoluteMaxReadAheadParallelism
		}
	}
	m.mu.Lock()
	m.maxParallelism = limit
	m.mu.Unlock()
}

// SetArticleBufferSize caps the number of segment ranges fetched within a
// single scheduled read-ahead window, regardless of how many the window's
// byte span would otherwise resolve to. A limit <= 0 resets to
// defaultArticleBufferSize rather than disabling the cap, since an unbounded
// window could otherwise fan out one goroutine per segment for a very large
// window/small-segment combination.
func (m *ReadAheadManager) SetArticleBufferSize(limit int) {
	if m == nil {
		return
	}
	if limit <= 0 {
		limit = defaultArticleBufferSize
	}
	m.mu.Lock()
	m.articleLimit = limit
	m.mu.Unlock()
}

// Register starts tracking a new read-ahead session for sessionID, replacing
// (and cancelling the in-flight prefetch of) any existing session already
// registered under the same ID.
//
// spans is copied rather than retained, so the caller's slice may be reused
// or mutated after this call returns even though StoredRarReader/
// DirectNzbReader spans are themselves mutated in place elsewhere. meta is
// variadic only so callers without display metadata yet can omit it; at most
// the first element is used, and OpenedAt defaults to now if left zero.
func (m *ReadAheadManager) Register(sessionID string, spans []SegmentSpan, fetcher PrioritySegmentFetcher, meta ...SessionMeta) {
	if m == nil || sessionID == "" || fetcher == nil {
		return
	}
	m.mu.Lock()
	if existing := m.sessions[sessionID]; existing != nil && existing.cancel != nil {
		existing.cancel()
	}
	sessionSpans := make([]SegmentSpan, len(spans))
	copy(sessionSpans, spans)
	var m0 SessionMeta
	if len(meta) > 0 {
		m0 = meta[0]
	}
	if m0.OpenedAt.IsZero() {
		m0.OpenedAt = time.Now().UTC()
	}
	m.sessions[sessionID] = &readAheadSession{
		spans:   sessionSpans,
		fetcher: fetcher,
		meta:    m0,
	}
	m.mu.Unlock()
}

// RegisterMeta attaches display metadata to an already-registered session.
func (m *ReadAheadManager) RegisterMeta(sessionID string, meta SessionMeta) {
	if m == nil || sessionID == "" {
		return
	}
	m.mu.Lock()
	if s := m.sessions[sessionID]; s != nil {
		s.meta = meta
	}
	m.mu.Unlock()
}

// ActiveSessions returns a snapshot of all currently open stream sessions.
func (m *ReadAheadManager) ActiveSessions() []SessionSnapshot {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SessionSnapshot, 0, len(m.sessions))
	for id, s := range m.sessions {
		out = append(out, SessionSnapshot{
			SessionID:     id,
			VirtualFileID: s.meta.VirtualFileID,
			FileName:      s.meta.FileName,
			FileSizeBytes: s.meta.FileSizeBytes,
			OpenedAt:      s.meta.OpenedAt,
			CurrentOffset: s.currentOffset,
		})
	}
	return out
}

// NotifyRead records sessionID's current read position and schedules the
// next read-ahead window from it. Callers report every interactive read
// through here so prefetch always chases the player's actual position rather
// than the position last assumed.
func (m *ReadAheadManager) NotifyRead(sessionID string, offset int64) {
	if m != nil && sessionID != "" && offset >= 0 {
		m.mu.Lock()
		if s := m.sessions[sessionID]; s != nil {
			s.currentOffset = offset
		}
		m.mu.Unlock()
	}
	m.schedule(sessionID, offset)
}

// Seek cancels sessionID's in-flight read-ahead window without scheduling a
// replacement.
//
// A new window is deliberately not started here: the interactive ReadAt that
// follows a seek fetches at PriorityInteractive (100), and starting a new
// read-ahead window immediately (at PriorityReadAhead, 80) would only spawn
// goroutines that compete with that fetch for the same connection budget at
// the worst possible moment. NotifyRead, called by the FUSE handle right
// after the interactive read returns, schedules the next window from the
// correct post-seek offset instead.
func (m *ReadAheadManager) Seek(sessionID string, offset int64) {
	metrics.M.ReadAheadCancellations.Add(1)
	if m == nil || sessionID == "" {
		return
	}
	m.mu.Lock()
	session := m.sessions[sessionID]
	if session != nil && session.cancel != nil {
		session.cancel()
		session.cancel = nil
	}
	m.mu.Unlock()
}

// ActiveCount returns the number of live read-ahead sessions.
func (m *ReadAheadManager) ActiveCount() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// Stop ends sessionID's read-ahead session, removing it from tracking and
// cancelling any in-flight prefetch for it.
func (m *ReadAheadManager) Stop(sessionID string) {
	if m == nil || sessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	delete(m.sessions, sessionID)
	if session != nil && session.cancel != nil {
		session.cancel()
	}
}

// schedule cancels sessionID's previous read-ahead window (if any) and
// launches a new one covering up to windowBytes ahead of offset, clamped to
// the remaining file length. It is called from NotifyRead on every
// interactive read, so windows are constantly superseded as the read
// position advances; the old window's context is cancelled before the new
// one starts so a stale fetch in flight doesn't hold a connection-budget slot
// past its usefulness.
//
// Prefetch runs in a background goroutine and divides the manager's
// maxParallelism across currently active sessions (floor of
// minReadAheadParallelism) so no single stream can starve the others'
// read-ahead of connection budget. articleLimit further caps how many
// segment ranges a single window will fetch, independent of parallelism.
func (m *ReadAheadManager) schedule(sessionID string, offset int64) {
	if m == nil || sessionID == "" || m.windowBytes == 0 || offset < 0 {
		return
	}
	m.mu.Lock()
	session := m.sessions[sessionID]
	if session == nil || session.fetcher == nil {
		m.mu.Unlock()
		return
	}
	if session.cancel != nil {
		session.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	session.cancel = cancel
	if session.windowsStarted < readAheadRampWindows {
		session.windowsStarted++
	}
	windowIndex := session.windowsStarted
	spans := make([]SegmentSpan, len(session.spans))
	copy(spans, session.spans)
	fetcher := session.fetcher
	window := m.windowBytes
	maxParallelism := m.maxParallelism
	articleLimit := m.articleLimit
	m.mu.Unlock()

	go func() {
		if len(spans) == 0 {
			return
		}
		fileEnd := spans[len(spans)-1].End
		if offset >= fileEnd {
			return
		}
		if remaining := fileEnd - offset; remaining < window {
			window = remaining
		}
		ranges, err := ResolveRange(spans, offset, window)
		if err != nil {
			return
		}
		if articleLimit > 0 && len(ranges) > articleLimit {
			ranges = ranges[:articleLimit]
		}
		// Divide read-ahead parallelism across active streams so every player
		// gets a fair share of NNTP connections (reference: 80% streaming
		// priority split per-stream). Minimum 4 per stream so slow networks
		// still prefetch ahead.
		activeStreams := m.ActiveCount()
		if activeStreams < 1 {
			activeStreams = 1
		}
		parallelism := maxParallelism / activeStreams
		if parallelism < minReadAheadParallelism {
			parallelism = minReadAheadParallelism
		}
		// Ramp up gradually over this session's first few windows instead of
		// bursting straight to its full share the instant read-ahead starts
		// filling its window -- see readAheadRampWindows/readAheadRampFloor's
		// doc comments. A no-op whenever the full share is already at or
		// below the floor.
		if windowIndex < readAheadRampWindows && parallelism > readAheadRampFloor {
			ramped := readAheadRampFloor + (parallelism-readAheadRampFloor)*windowIndex/readAheadRampWindows
			if ramped < readAheadRampFloor {
				ramped = readAheadRampFloor
			}
			if ramped < parallelism {
				parallelism = ramped
			}
		}
		sem := make(chan struct{}, parallelism)
		var wg sync.WaitGroup
	readAhead:
		for _, segment := range ranges {
			select {
			case <-ctx.Done():
				break readAhead
			case sem <- struct{}{}:
			}
			wg.Add(1)
			go func(seg SegmentRange) {
				defer wg.Done()
				defer func() { <-sem }()
				if ctx.Err() != nil {
					return
				}
				_, _ = fetcher.FetchRangePriority(ctx, seg, PriorityReadAhead)
			}(segment)
		}
		wg.Wait()
	}()
}

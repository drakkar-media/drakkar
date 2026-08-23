package stream

import (
	"context"
	"sync"
	"sync/atomic"
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
	defaultMaxReadAheadParallelism        = 4
	absoluteMaxReadAheadParallelism       = 60
	minReadAheadParallelism               = 1
	defaultArticleBufferSize              = 40
	highBitrateFileSizeThreshold    int64 = 2 << 30
	highBitrateMinReadAheadBytes    int64 = 128 << 20
	highBitrateMaxArticleBufferSize       = 256

	// readAheadRampFloor is the concurrency every read-ahead window starts
	// at immediately -- launches beyond this floor, up to the window's full
	// parallelism share, are staggered by readAheadRampStaggerInterval
	// instead of firing in the same instant. The ramp is a no-op whenever
	// the stream's own full share is already at or below this floor (e.g. a
	// low connection budget, or many concurrent streams splitting a modest
	// total): the burst this ramp exists to avoid only happens when a
	// window's fetch count jumps by a lot in one instant, and a share
	// that's already small has nothing meaningful to spike from.
	//
	// Originally added while chasing a live A/V-sync-delay report on a
	// high-bitrate 2160p stream (2026-08-09): visible block/stutter
	// artifacts right at stream start, immediately followed by a persistent
	// desync that only a seek (not pause/resume) fixed -- the signature of
	// a genuine data-delivery hiccup at open, not a per-file decode bug
	// (both the RAR header-skip detection and the yEnc segment-length
	// calibration for this exact release were independently verified
	// correct against the real fetched bytes). A live repro caught every
	// priority tier (interactive, read-ahead, AND the health-check's own
	// background probe) going slow simultaneously, together with unrelated
	// titles' health-check probes failing in the same few seconds -- the
	// same signature this codebase's health-check pacing comment already
	// attributes to provider-side rate limiting triggered by aggregate
	// connection/request load. A single stream jumping straight from 0 to
	// its full parallelism share (routinely ~20 connections) in one
	// instant, right at the moment read-ahead first fills its window, is
	// exactly the kind of spike that can trip that kind of throttling.
	//
	// Originally only applied to a session's first few windows (tracked via
	// a per-session window counter, reset on Seek). Confirmed live
	// (2026-08-20, another 2160p stream, ~1h45m into an uninterrupted
	// playback session): 20 concurrent read-ahead fetches -- exactly that
	// session's full parallelism share -- all timed out simultaneously at
	// the 30s ceiling against the sole configured provider, immediately
	// followed by a genuine connection reset and, seconds later, a 7.3s
	// stall on the interactive-priority lane itself (the felt playback
	// freeze). The session had been open far longer than the handful of
	// windows the old per-session ramp covered -- every window past the
	// first few had already been bursting to full share instantly for over
	// an hour, retriggering the exact throttle signature the ramp was built
	// to avoid, on a roughly 2-minute cycle (windowBytes/2 of playback) for
	// the rest of any sufficiently long stream. The ramp now applies to
	// every window unconditionally instead of just a session's first few,
	// since the provider has no way to know (or care) whether a burst is a
	// stream's 1st window or its 50th.
	readAheadRampFloor = 8

	// readAheadRampStaggerInterval paces launches beyond readAheadRampFloor
	// within a single window: one additional concurrent fetch is allowed to
	// start every interval until the window's full parallelism share is
	// reached, rather than all of them firing in the same instant. At the
	// absoluteMaxReadAheadParallelism ceiling (60), ramping from floor to
	// full costs (60-8)*readAheadRampStaggerInterval =~ 2.6s -- negligible
	// against a window that covers tens of seconds to minutes of playback,
	// but enough to no longer present as a single simultaneous burst to the
	// provider.
	readAheadRampStaggerInterval = 50 * time.Millisecond
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
	spans         []SegmentSpan
	fetcher       PrioritySegmentFetcher
	cancel        context.CancelFunc
	meta          SessionMeta
	currentOffset int64
	windowStart   int64
	rescheduleAt  int64
	windowValid   bool
	generation    uint64
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

// RegisterMeta attaches display metadata to an already-registered session,
// and stops read-ahead for any other session already open on the same
// VirtualFileID -- but, as of the fix below, deliberately leaves that other
// session's foreground serving loop alone.
//
// This used to also tear the foreground loop down immediately (meta.Cancel),
// on the theory that a seek from a WebDAV client (e.g. Plex) opens a
// brand-new GET/Range request while the old one may still be lingering (see
// deadlineResponseWriter in internal/dav for why that lingering can itself
// take a while to resolve), and read-ahead cancellation alone left that old
// connection's real Read loop running for as long as its client (rclone)
// kept draining it -- confirmed live 2026-08-10, up to ~163s of
// no-longer-needed transfer holding NNTP connection budget a new seek's
// fetch was waiting on.
//
// That fix rested on an assumption confirmed FALSE live 2026-08-23: two
// sessions open on the same VirtualFileID are not reliably "the same
// logical playback, mid-seek" -- they're just as often two genuinely
// independent, still-wanted reads of the same file (a second viewer, or any
// internal code doing its own OpenVirtualMediaFile read against a file a
// real client also happens to be streaming; there is nothing in a raw
// WebDAV GET that distinguishes the two, since every read -- regardless of
// which end-user or process it belongs to -- arrives through the single
// shared rclone mount). Confirmed with a direct, reproducible test: opening
// a second, unrelated read against a file already mid-transfer cut the
// first one off after 322 bytes of a requested 500MB, with zero trace in
// this app's own logs (meta.Cancel's teardown was never itself logged).
// Since rclone retries a failed read by reopening the file -- which
// re-enters this exact path -- two genuinely concurrent readers of the same
// file could each keep killing the other's retry indefinitely.
//
// Silently corrupting an active, wanted transfer is a worse failure mode
// than the resource waste the removed cancellation avoided: the existing,
// independent write-idle-timeout (deadlineResponseWriter, internal/dav)
// already bounds how long ANY connection -- superseded or not -- can hold
// its NNTP budget while making zero real progress, so a genuinely abandoned
// session still gets cleaned up without this function guessing at intent
// from information it structurally cannot have. Read-ahead cancellation
// stays: it only stops speculative prefetch for data nothing has actually
// asked for yet, so it can never cut off an active transfer.
func (m *ReadAheadManager) RegisterMeta(sessionID string, meta SessionMeta) {
	if m == nil || sessionID == "" {
		return
	}
	m.mu.Lock()
	if s := m.sessions[sessionID]; s != nil {
		s.meta = meta
	}
	var stale []string
	if meta.VirtualFileID != 0 {
		for id, s := range m.sessions {
			if id != sessionID && s.meta.VirtualFileID == meta.VirtualFileID {
				stale = append(stale, id)
			}
		}
	}
	for _, id := range stale {
		if s := m.sessions[id]; s != nil {
			if s.cancel != nil {
				s.cancel()
			}
			delete(m.sessions, id)
		}
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
// correct post-seek offset instead. That next window ramps up gradually like
// every other window (see readAheadRampFloor) since it lands on unfetched
// territory just as surely as a new session does -- no separate ramp-reset
// bookkeeping is needed here now that every window ramps unconditionally.
func (m *ReadAheadManager) Seek(sessionID string, offset int64) {
	metrics.M.ReadAheadCancellations.Add(1)
	if m == nil || sessionID == "" {
		return
	}
	m.mu.Lock()
	session := m.sessions[sessionID]
	if session != nil {
		if session.cancel != nil {
			session.cancel()
			session.cancel = nil
		}
		session.windowValid = false
		session.generation++
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

// schedule launches a read-ahead window covering up to windowBytes ahead of
// offset, clamped to the remaining file length and articleLimit. Notifications
// inside the first half of an existing window are coalesced; once playback
// crosses that midpoint, the old window is cancelled and replaced. This keeps
// coverage ahead of playback without copying the span table or rebuilding a
// nearly identical 512 MiB plan after every foreground read.
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
	if session.windowValid && offset >= session.windowStart && offset < session.rescheduleAt {
		m.mu.Unlock()
		return
	}
	if session.cancel != nil {
		session.cancel()
		session.cancel = nil
	}
	session.generation++
	generation := session.generation
	session.windowValid = false

	spans := session.spans
	if len(spans) == 0 || offset >= spans[len(spans)-1].End {
		m.mu.Unlock()
		return
	}
	window := m.windowBytes
	if remaining := spans[len(spans)-1].End - offset; remaining < window {
		window = remaining
	}
	articleLimit := adaptiveArticleLimit(spans, offset, window, m.articleLimit, session.meta.FileSizeBytes >= highBitrateFileSizeThreshold)
	ranges, err := resolveRange(spans, offset, window, articleLimit)
	if err != nil || len(ranges) == 0 {
		m.mu.Unlock()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	session.cancel = cancel
	windowEnd := ranges[len(ranges)-1].RangeEnd
	rescheduleAt := offset + (windowEnd-offset)/2
	if rescheduleAt <= offset {
		rescheduleAt = windowEnd
	}
	session.windowStart = offset
	session.rescheduleAt = rescheduleAt
	session.windowValid = true
	fetcher := session.fetcher
	maxParallelism := m.maxParallelism
	activeStreams := len(m.sessions)
	scheduledSession := session
	m.mu.Unlock()

	go func() {
		// Divide read-ahead parallelism across active streams so every player
		// gets a fair share of NNTP connections (reference: 80% streaming
		// priority split per-stream). Keep at least one worker per stream.
		if activeStreams < 1 {
			activeStreams = 1
		}
		parallelism := maxParallelism / activeStreams
		if parallelism < minReadAheadParallelism {
			parallelism = minReadAheadParallelism
		}
		sem := make(chan struct{}, parallelism)
		var wg sync.WaitGroup
		var fetchFailed atomic.Bool
		var stagger *time.Ticker
		if parallelism > readAheadRampFloor {
			stagger = time.NewTicker(readAheadRampStaggerInterval)
			defer stagger.Stop()
		}
	readAhead:
		for i, segment := range ranges {
			// Every window ramps up gradually instead of bursting straight to
			// its full parallelism share in one instant -- see
			// readAheadRampFloor's doc comment. Only paces the initial climb
			// from floor to the window's full parallelism share (i <
			// parallelism); once that many concurrent fetches have been
			// launched, later replacement launches are gated purely by sem
			// (a slot freeing up) same as before, so sustained steady-state
			// throughput for windows with more segments than parallelism is
			// unaffected. A no-op entirely whenever the full share is already
			// at or below the floor (stagger is nil in that case).
			if stagger != nil && i >= readAheadRampFloor && i < parallelism {
				select {
				case <-ctx.Done():
					break readAhead
				case <-stagger.C:
				}
			}
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
				if _, err := fetcher.FetchRangePriority(ctx, seg, PriorityReadAhead); err != nil && ctx.Err() == nil {
					fetchFailed.Store(true)
				}
			}(segment)
		}
		wg.Wait()
		wasCancelled := ctx.Err() != nil
		cancel()

		m.mu.Lock()
		current := m.sessions[sessionID]
		if current == scheduledSession && current.generation == generation {
			current.cancel = nil
			if fetchFailed.Load() && !wasCancelled {
				current.windowValid = false
			}
		}
		m.mu.Unlock()
	}()
}

func adaptiveArticleLimit(spans []SegmentSpan, offset, window int64, configuredLimit int, highBitrate bool) int {
	if configuredLimit <= 0 {
		configuredLimit = defaultArticleBufferSize
	}
	if !highBitrate || window <= 0 || configuredLimit >= highBitrateMaxArticleBufferSize {
		return configuredLimit
	}
	target := highBitrateMinReadAheadBytes
	if window < target {
		target = window
	}
	requestEnd := offset + target
	if requestEnd < offset {
		return configuredLimit
	}
	count := 0
	cursor := offset
	for _, span := range spans {
		if span.End <= cursor {
			continue
		}
		if span.Start > cursor {
			break
		}
		count++
		if span.End >= requestEnd || count >= highBitrateMaxArticleBufferSize {
			break
		}
		cursor = span.End
	}
	if count < configuredLimit {
		return configuredLimit
	}
	if count > highBitrateMaxArticleBufferSize {
		return highBitrateMaxArticleBufferSize
	}
	return count
}

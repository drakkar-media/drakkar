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
// content: e.g. a ~47 Mbps 4K remux needs roughly 8-9 concurrent ~700KB
// segment fetches in flight just to break even, leaving little margin
// against any per-fetch latency before the read-ahead buffer runs dry and
// playback stalls -- repeatedly, since it refills and drains on every cycle.
const (
	defaultMaxReadAheadParallelism  = 4
	absoluteMaxReadAheadParallelism = 30
	minReadAheadParallelism         = 1
	defaultArticleBufferSize        = 40
)

type FetchPriority int

const (
	PriorityBackground  FetchPriority = 10
	PriorityReadAhead   FetchPriority = 80
	PriorityInteractive FetchPriority = 100
)

type PrioritySegmentFetcher interface {
	SegmentFetcher
	FetchRangePriority(ctx context.Context, segment SegmentRange, priority FetchPriority) ([]byte, error)
}

type SessionVirtualMediaFile interface {
	VirtualMediaFile
	StartSession(sessionID string)
	NotifyRead(sessionID string, offset int64)
	Seek(sessionID string, offset int64)
	StopSession(sessionID string)
	RegisterMeta(sessionID string, meta SessionMeta)
}

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
}

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
		limit = streamingBudget / 2 // half the streaming budget for prefetch depth
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

func (m *ReadAheadManager) Seek(sessionID string, offset int64) {
	metrics.M.ReadAheadCancellations.Add(1)
	// Cancel the current read-ahead window without immediately scheduling a
	// new one. The interactive ReadAt that follows a seek goes to priority 100
	// (vs read-ahead 80). If we started a new window here those goroutines
	// would compete with the player's first fetch at the new position. Instead,
	// NotifyRead — called by the FUSE handle right after the interactive read
	// returns — will schedule the next window from the correct offset.
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
		sem := make(chan struct{}, parallelism)
		var wg sync.WaitGroup
		for _, segment := range ranges {
			select {
			case <-ctx.Done():
				break
			case sem <- struct{}{}:
			}
			if ctx.Err() != nil {
				break
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

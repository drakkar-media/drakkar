package stream

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type priorityFetchCall struct {
	segment  SegmentRange
	priority FetchPriority
	ctx      context.Context
}

type priorityFetcherStub struct {
	calls chan priorityFetchCall
}

func (s *priorityFetcherStub) FetchRange(ctx context.Context, segment SegmentRange) ([]byte, error) {
	return s.FetchRangePriority(ctx, segment, PriorityInteractive)
}

func (s *priorityFetcherStub) FetchRangePriority(ctx context.Context, segment SegmentRange, priority FetchPriority) ([]byte, error) {
	if s.calls != nil {
		s.calls <- priorityFetchCall{segment: segment, priority: priority, ctx: ctx}
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestReadAheadManagerNotifyReadUsesReadAheadPriority(t *testing.T) {
	manager := NewReadAheadManager(32)
	fetcher := &priorityFetcherStub{calls: make(chan priorityFetchCall, 1)}
	manager.Register("stream-1", []SegmentSpan{{SegmentID: 1, MessageID: "<msg1>", Start: 0, End: 64}}, fetcher)

	manager.NotifyRead("stream-1", 16)

	select {
	case call := <-fetcher.calls:
		if call.priority != PriorityReadAhead {
			t.Fatalf("expected read-ahead priority, got %d", call.priority)
		}
		if call.segment.RangeStart != 16 || call.segment.RangeEnd != 48 {
			t.Fatalf("unexpected range %#v", call.segment)
		}
		manager.Stop("stream-1")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for read-ahead fetch")
	}
}

func TestReadAheadManagerSeekCancelsPreviousRequest(t *testing.T) {
	manager := NewReadAheadManager(32)
	fetcher := &priorityFetcherStub{calls: make(chan priorityFetchCall, 2)}
	manager.Register("stream-2", []SegmentSpan{{SegmentID: 1, MessageID: "<msg1>", Start: 0, End: 128}}, fetcher)

	manager.NotifyRead("stream-2", 0)
	first := <-fetcher.calls
	manager.Seek("stream-2", 64)

	select {
	case <-first.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("expected first request context to cancel on seek")
	}

	// Seek only cancels — it does not schedule a new window immediately.
	// The FUSE handle calls NotifyRead after the interactive read completes.
	manager.NotifyRead("stream-2", 64)

	select {
	case second := <-fetcher.calls:
		if second.segment.RangeStart != 64 || second.segment.RangeEnd != 96 {
			t.Fatalf("unexpected second range %#v", second.segment)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for read-ahead after seek+NotifyRead")
	}
	manager.Stop("stream-2")
}

func TestActiveSessionsTracking(t *testing.T) {
	manager := NewReadAheadManager(32)
	fetcher := &priorityFetcherStub{}

	if n := manager.ActiveCount(); n != 0 {
		t.Fatalf("expected 0 sessions initially, got %d", n)
	}
	if ss := manager.ActiveSessions(); len(ss) != 0 {
		t.Fatalf("expected empty sessions, got %v", ss)
	}

	meta := SessionMeta{
		VirtualFileID: 42,
		FileName:      "Dune (2021).mkv",
		FileSizeBytes: 8_000_000_000,
		OpenedAt:      time.Now().UTC(),
	}
	manager.Register("s1", []SegmentSpan{{SegmentID: 1, MessageID: "<m1>", Start: 0, End: 100}}, fetcher, meta)

	if n := manager.ActiveCount(); n != 1 {
		t.Fatalf("expected 1 session, got %d", n)
	}
	ss := manager.ActiveSessions()
	if len(ss) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(ss))
	}
	snap := ss[0]
	if snap.SessionID != "s1" {
		t.Fatalf("unexpected session ID %q", snap.SessionID)
	}
	if snap.VirtualFileID != 42 {
		t.Fatalf("unexpected virtualFileID %d", snap.VirtualFileID)
	}
	if snap.FileName != "Dune (2021).mkv" {
		t.Fatalf("unexpected fileName %q", snap.FileName)
	}
	if snap.FileSizeBytes != 8_000_000_000 {
		t.Fatalf("unexpected size %d", snap.FileSizeBytes)
	}

	// Simulate a read notification updating the current offset.
	manager.NotifyRead("s1", 4096)
	ss2 := manager.ActiveSessions()
	if ss2[0].CurrentOffset != 4096 {
		t.Fatalf("expected offset 4096, got %d", ss2[0].CurrentOffset)
	}

	// RegisterMeta after the fact also updates the snapshot.
	manager.RegisterMeta("s1", SessionMeta{VirtualFileID: 99, FileName: "updated.mkv"})
	ss3 := manager.ActiveSessions()
	if ss3[0].FileName != "updated.mkv" {
		t.Fatalf("expected updated fileName, got %q", ss3[0].FileName)
	}

	// Stop removes the session.
	manager.Stop("s1")
	if n := manager.ActiveCount(); n != 0 {
		t.Fatalf("expected 0 sessions after stop, got %d", n)
	}
}

func TestReadAheadManagerStopCancelsRequest(t *testing.T) {
	manager := NewReadAheadManager(32)
	fetcher := &priorityFetcherStub{calls: make(chan priorityFetchCall, 1)}
	manager.Register("stream-3", []SegmentSpan{{SegmentID: 1, MessageID: "<msg1>", Start: 0, End: 128}}, fetcher)

	manager.NotifyRead("stream-3", 0)
	call := <-fetcher.calls
	manager.Stop("stream-3")

	select {
	case <-call.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("expected stop to cancel request context")
	}
}

// TestSetConnectionBudgetScalesWithStreamingBudget guards the read-ahead
// throughput fix for high-bitrate content: sustaining something like an
// ~84.5 Mbps 4K remux needs many concurrent segment fetches in flight, and
// a low ceiling left too little margin against per-fetch latency before the
// buffer ran dry mid-playback. Read-ahead fetches always run at
// PriorityReadAhead, strictly below the player's PriorityInteractive, so
// this budget can use the full streaming budget without risking starved
// live reads.
func TestSetConnectionBudgetScalesWithStreamingBudget(t *testing.T) {
	tests := []struct {
		name             string
		totalConnections int
		streamingPct     int
		wantParallelism  int
	}{
		{"generous budget caps at absolute max", 90, 80, 60},            // streamingBudget=72, capped to 60
		{"modest budget scales with full streaming budget", 20, 80, 16}, // streamingBudget=16
		{"zero total falls back to default", 0, 80, defaultMaxReadAheadParallelism},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewReadAheadManager(1024)
			m.SetConnectionBudget(tt.totalConnections, tt.streamingPct)
			m.mu.Lock()
			got := m.maxParallelism
			m.mu.Unlock()
			if got != tt.wantParallelism {
				t.Fatalf("maxParallelism = %d, want %d", got, tt.wantParallelism)
			}
		})
	}
}

// TestReadAheadManagerRampsUpParallelismOverFirstWindows guards the fix for
// a live A/V-sync-delay report (2026-08-09, Venom: Let There Be Carnage):
// visible block/stutter artifacts right at stream start, then a persistent
// desync only a seek (not pause/resume) fixed. A live repro caught every
// priority tier -- interactive, read-ahead, and the health-check's own
// background probe -- going slow simultaneously, alongside unrelated titles'
// health-check probes failing in the same few seconds, matching this
// codebase's own documented signature for provider-side rate limiting
// triggered by aggregate connection/request load. A single stream jumping
// straight from 0 to its full parallelism share (routinely ~20 connections)
// in one instant, right when read-ahead first fills its window, is exactly
// the kind of spike that can trip it. This proves the first few windows ramp
// up gradually (readAheadRampFloor, then increasing) instead of bursting
// straight to the full share, while still reaching the full share within
// readAheadRampWindows.
func TestReadAheadManagerRampsUpParallelismOverFirstWindows(t *testing.T) {
	manager := NewReadAheadManager(1 << 20)
	manager.SetConnectionBudget(25, 80) // full per-stream share = 20
	manager.SetArticleBufferSize(30)

	spans := make([]SegmentSpan, 30)
	for i := range spans {
		spans[i] = SegmentSpan{SegmentID: int64(i + 1), MessageID: fmt.Sprintf("<msg%d>", i+1), Start: int64(i) * 64, End: int64(i+1) * 64}
	}

	fetcher := &priorityFetcherStub{calls: make(chan priorityFetchCall, 64)}
	manager.Register("stream-ramp", spans, fetcher)
	defer manager.Stop("stream-ramp")

	// priorityFetcherStub sends to calls then blocks on ctx.Done() forever,
	// so once a window's semaphore fills, no further calls arrive until the
	// next NotifyRead cancels it -- draining until a short quiet gap gives an
	// exact count of that window's concurrent fetches.
	drainConcurrentCalls := func() int {
		count := 0
		for {
			select {
			case <-fetcher.calls:
				count++
			case <-time.After(150 * time.Millisecond):
				return count
			}
		}
	}

	wantByWindow := []int{11, 14, 17, 20} // floor 8 + (20-8)*windowIndex/4, then full from window 4
	for i, want := range wantByWindow {
		manager.NotifyRead("stream-ramp", 0)
		if got := drainConcurrentCalls(); got != want {
			t.Fatalf("window %d: expected %d concurrent fetches, got %d", i+1, want, got)
		}
	}
}

// TestReadAheadManagerSeekResetsRamp guards a gap in the ramp-up fix above,
// confirmed live (2026-08-10) independently on two unrelated titles: play
// from the start always worked, but seeking to a specific timestamp
// afterward consistently hung. The ramp only reset on a brand-new session
// (Register), so a session that had already played sequentially long enough
// to reach its full parallelism share, then seeked to a distant, entirely
// unfetched part of the file, burst straight to that full share for the new
// region anyway -- the exact instant spike the ramp exists to prevent, just
// triggered by a seek instead of a fresh open. A seek must restart the ramp,
// since it lands on unfetched territory just as surely as a new session
// does.
func TestReadAheadManagerSeekResetsRamp(t *testing.T) {
	manager := NewReadAheadManager(1 << 20)
	manager.SetConnectionBudget(25, 80) // full per-stream share = 20
	manager.SetArticleBufferSize(30)

	spans := make([]SegmentSpan, 30)
	for i := range spans {
		spans[i] = SegmentSpan{SegmentID: int64(i + 1), MessageID: fmt.Sprintf("<msg%d>", i+1), Start: int64(i) * 64, End: int64(i+1) * 64}
	}

	fetcher := &priorityFetcherStub{calls: make(chan priorityFetchCall, 64)}
	manager.Register("stream-seek-ramp", spans, fetcher)
	defer manager.Stop("stream-seek-ramp")

	drainConcurrentCalls := func() int {
		count := 0
		for {
			select {
			case <-fetcher.calls:
				count++
			case <-time.After(150 * time.Millisecond):
				return count
			}
		}
	}

	// Play sequentially through all 4 ramp windows to reach full parallelism.
	for range 4 {
		manager.NotifyRead("stream-seek-ramp", 0)
		drainConcurrentCalls()
	}
	if got := drainConcurrentCalls(); got != 0 {
		t.Fatalf("setup: expected the ramp-up loop to have drained everything, got %d extra", got)
	}

	// Now seek to a distant, never-fetched part of the file. Without the
	// reset, this next window would burst straight to the full share (20)
	// instead of restarting the ramp near the floor (8 + (20-8)*1/4 = 11 for
	// this window, the first of the restarted ramp).
	manager.Seek("stream-seek-ramp", 0)
	manager.NotifyRead("stream-seek-ramp", 0)
	if got := drainConcurrentCalls(); got != 11 {
		t.Fatalf("expected the window right after a seek to ramp back down to 11, got %d", got)
	}
}

func TestReadAheadManagerCapsArticleBuffer(t *testing.T) {
	manager := NewReadAheadManager(1024)
	manager.SetArticleBufferSize(2)
	fetcher := &priorityFetcherStub{calls: make(chan priorityFetchCall, 8)}
	manager.Register("stream-4", []SegmentSpan{
		{SegmentID: 1, MessageID: "<msg1>", Start: 0, End: 64},
		{SegmentID: 2, MessageID: "<msg2>", Start: 64, End: 128},
		{SegmentID: 3, MessageID: "<msg3>", Start: 128, End: 192},
		{SegmentID: 4, MessageID: "<msg4>", Start: 192, End: 256},
	}, fetcher)

	manager.NotifyRead("stream-4", 0)
	for i := 0; i < 2; i++ {
		select {
		case <-fetcher.calls:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for buffered article fetch")
		}
	}
	manager.Stop("stream-4")
	select {
	case call := <-fetcher.calls:
		t.Fatalf("expected only 2 buffered article fetches, got extra %#v", call.segment)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestRegisterMetaStopsStaleSessionForSameVirtualFile guards a real
// production incident (2026-08-10): a seek opens a brand-new WebDAV
// GET/Range request (and thus a brand-new session) while the old one is
// still lingering, rather than reusing it. Before this fix, the old
// session's read-ahead kept running -- and kept consuming NNTP connection
// budget -- until it independently noticed the client was gone, competing
// with the new session's interactive fetch for the same pool at exactly the
// moment it needs a connection fastest. RegisterMeta is where a new
// session's VirtualFileID first becomes known, so that's where the old
// session for the same file must be recognized and cut loose.
func TestRegisterMetaStopsStaleSessionForSameVirtualFile(t *testing.T) {
	manager := NewReadAheadManager(32)
	fetcher := &priorityFetcherStub{calls: make(chan priorityFetchCall, 2)}
	manager.Register("old-session", []SegmentSpan{{SegmentID: 1, MessageID: "<msg1>", Start: 0, End: 128}}, fetcher)
	manager.RegisterMeta("old-session", SessionMeta{VirtualFileID: 42})

	manager.NotifyRead("old-session", 0)
	oldCall := <-fetcher.calls

	manager.Register("new-session", []SegmentSpan{{SegmentID: 1, MessageID: "<msg1>", Start: 0, End: 128}}, fetcher)
	manager.RegisterMeta("new-session", SessionMeta{VirtualFileID: 42})

	select {
	case <-oldCall.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("expected the old session's in-flight read-ahead to be cancelled when a new session opened for the same VirtualFileID")
	}

	active := manager.ActiveSessions()
	for _, s := range active {
		if s.SessionID == "old-session" {
			t.Fatalf("expected the old session to be removed from tracking, still present: %#v", s)
		}
	}

	manager.Stop("new-session")
}

// TestRegisterMetaLeavesUnrelatedSessionsAlone guards against an
// overcorrection: sessions for a *different* VirtualFileID must never be
// touched just because some other session's metadata was registered.
func TestRegisterMetaLeavesUnrelatedSessionsAlone(t *testing.T) {
	manager := NewReadAheadManager(32)
	fetcher := &priorityFetcherStub{calls: make(chan priorityFetchCall, 2)}
	manager.Register("other-file", []SegmentSpan{{SegmentID: 1, MessageID: "<msg1>", Start: 0, End: 128}}, fetcher)
	manager.RegisterMeta("other-file", SessionMeta{VirtualFileID: 7})

	manager.NotifyRead("other-file", 0)
	otherCall := <-fetcher.calls

	manager.Register("new-session", []SegmentSpan{{SegmentID: 1, MessageID: "<msg1>", Start: 0, End: 128}}, fetcher)
	manager.RegisterMeta("new-session", SessionMeta{VirtualFileID: 42})

	select {
	case <-otherCall.ctx.Done():
		t.Fatal("expected the unrelated session's in-flight read-ahead to survive")
	case <-time.After(100 * time.Millisecond):
	}

	found := false
	for _, s := range manager.ActiveSessions() {
		if s.SessionID == "other-file" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected the unrelated session to remain tracked")
	}

	manager.Stop("other-file")
	manager.Stop("new-session")
}

// TestRegisterMetaCancelsStaleSessionsForegroundServing guards a real
// production incident (2026-08-10): stopping only the stale session's
// read-ahead wasn't enough -- its foreground Read loop (the dav.virtualFile
// serving the old, now-abandoned HTTP connection) kept running for as long
// as the client kept draining it, measured live at up to ~163s of real data
// transfer nobody needed anymore. meta.Cancel is the hook a session's owner
// supplies to tear that foreground loop down directly; RegisterMeta must
// call it for any stale session it finds, not just cancel its read-ahead.
func TestRegisterMetaCancelsStaleSessionsForegroundServing(t *testing.T) {
	manager := NewReadAheadManager(32)
	fetcher := &priorityFetcherStub{calls: make(chan priorityFetchCall, 2)}

	foregroundCancelled := false
	cancel := func() { foregroundCancelled = true }

	manager.Register("old-session", []SegmentSpan{{SegmentID: 1, MessageID: "<msg1>", Start: 0, End: 128}}, fetcher)
	manager.RegisterMeta("old-session", SessionMeta{VirtualFileID: 99, Cancel: cancel})

	manager.Register("new-session", []SegmentSpan{{SegmentID: 1, MessageID: "<msg1>", Start: 0, End: 128}}, fetcher)
	manager.RegisterMeta("new-session", SessionMeta{VirtualFileID: 99})

	if !foregroundCancelled {
		t.Fatal("expected the stale session's foreground serving Cancel to be called when a new session opened for the same VirtualFileID")
	}

	manager.Stop("new-session")
}

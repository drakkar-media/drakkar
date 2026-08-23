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

func TestReadAheadManagerCoalescesNotificationsWithinWindow(t *testing.T) {
	manager := NewReadAheadManager(128)
	manager.SetArticleBufferSize(2)
	fetcher := &priorityFetcherStub{calls: make(chan priorityFetchCall, 8)}
	manager.Register("stream-coalesce", []SegmentSpan{
		{SegmentID: 1, MessageID: "<msg1>", Start: 0, End: 64},
		{SegmentID: 2, MessageID: "<msg2>", Start: 64, End: 128},
		{SegmentID: 3, MessageID: "<msg3>", Start: 128, End: 192},
	}, fetcher)
	defer manager.Stop("stream-coalesce")

	manager.NotifyRead("stream-coalesce", 0)
	first := <-fetcher.calls
	<-fetcher.calls

	manager.NotifyRead("stream-coalesce", 32)
	select {
	case <-first.ctx.Done():
		t.Fatal("notification inside first half of window cancelled active prefetch")
	case call := <-fetcher.calls:
		t.Fatalf("notification inside first half of window scheduled duplicate fetch %#v", call.segment)
	case <-time.After(100 * time.Millisecond):
	}

	manager.NotifyRead("stream-coalesce", 64)
	select {
	case <-first.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("crossing window midpoint did not cancel previous prefetch")
	}
	for range 2 {
		select {
		case <-fetcher.calls:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for replacement prefetch")
		}
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

// TestReadAheadManagerRampsUpParallelismEveryWindow guards the fix for a
// live streaming stall (2026-08-20, a 2160p stream, confirmed ~1h45m
// into an uninterrupted playback session): 20 concurrent read-ahead
// fetches -- exactly that session's full parallelism share -- all timed out
// simultaneously against the sole configured provider, immediately followed
// by a genuine connection reset and a stall on the interactive lane itself
// (the felt playback freeze). The ramp originally added for a similar 2026
// -08-09 incident (another high-bitrate stream) only applied to a session's
// first few windows (tracked via a per-session counter, reset on Seek);
// every window after that burst straight to full parallelism in one
// instant, retriggering the exact provider-throttle signature the ramp was
// built to avoid, on a roughly 2-minute cycle for the rest of any
// sufficiently long stream. This proves EVERY window ramps gradually now --
// checked once early in the session and repeatedly many windows deep, well
// past the old fix's session-lifetime cutoff of 4.
func TestReadAheadManagerRampsUpParallelismEveryWindow(t *testing.T) {
	manager := NewReadAheadManager(1 << 20)
	manager.SetConnectionBudget(25, 80) // full per-stream share = 20
	manager.SetArticleBufferSize(30)

	spans := make([]SegmentSpan, 800)
	for i := range spans {
		spans[i] = SegmentSpan{SegmentID: int64(i + 1), MessageID: fmt.Sprintf("<msg%d>", i+1), Start: int64(i) * 64, End: int64(i+1) * 64}
	}

	fetcher := &priorityFetcherStub{calls: make(chan priorityFetchCall, 64)}
	manager.Register("stream-ramp", spans, fetcher)
	defer manager.Stop("stream-ramp")

	// priorityFetcherStub sends to calls then blocks on ctx.Done() forever,
	// so once a window's semaphore (sized to the full parallelism share)
	// fills, no further calls arrive until the next NotifyRead cancels it --
	// draining until a quiet gap well above the 50ms stagger interval but
	// well below the reschedule cadence gives every call's arrival time.
	drainWithTimestamps := func() []time.Time {
		var arrivals []time.Time
		for {
			select {
			case <-fetcher.calls:
				arrivals = append(arrivals, time.Now())
			case <-time.After(150 * time.Millisecond):
				return arrivals
			}
		}
	}

	checkWindowRamps := func(label string) {
		t.Helper()
		arrivals := drainWithTimestamps()
		if len(arrivals) != 20 {
			t.Fatalf("%s: expected 20 concurrent fetches (full parallelism share), got %d", label, len(arrivals))
		}
		// The initial floor batch (readAheadRampFloor=8) launches
		// unstaggered -- essentially at once.
		floorSpan := arrivals[readAheadRampFloor-1].Sub(arrivals[0])
		if floorSpan > 50*time.Millisecond {
			t.Fatalf("%s: floor batch (first %d) took %s to arrive, expected near-instant", label, readAheadRampFloor, floorSpan)
		}
		// The climb from the floor to the full 20 must be staggered, not
		// instant -- this is the actual fix. 12 more launches at
		// readAheadRampStaggerInterval (50ms) apart should take ~600ms;
		// require at least a bit over half that so a slow CI box doesn't
		// flake, while still clearly distinguishing from the old
		// near-instant-burst behavior this test guards against.
		rampSpan := arrivals[len(arrivals)-1].Sub(arrivals[readAheadRampFloor-1])
		if rampSpan < 350*time.Millisecond {
			t.Fatalf("%s: ramp from floor to full parallelism took only %s, expected a staggered climb of several hundred ms (bug: bursting to full share in one instant)", label, rampSpan)
		}
	}

	// A 30-article window covers 1,920 bytes, so each 960-byte advance
	// crosses its midpoint and starts one genuinely new window.
	for i := range 8 {
		manager.NotifyRead("stream-ramp", int64(i)*960)
		checkWindowRamps(fmt.Sprintf("window %d", i+1))
	}
}

// TestReadAheadManagerSeekStartsRampedWindow guards the seek path
// specifically: a seek cancels the in-flight window without immediately
// scheduling a replacement (NotifyRead does that once the interactive read
// following the seek reports its landing offset), and that replacement
// window must ramp up gradually like any other window -- it lands on
// unfetched territory just as surely as a new session or a routine
// mid-playback reschedule does.
func TestReadAheadManagerSeekStartsRampedWindow(t *testing.T) {
	manager := NewReadAheadManager(1 << 20)
	manager.SetConnectionBudget(25, 80) // full per-stream share = 20
	manager.SetArticleBufferSize(30)

	spans := make([]SegmentSpan, 800)
	for i := range spans {
		spans[i] = SegmentSpan{SegmentID: int64(i + 1), MessageID: fmt.Sprintf("<msg%d>", i+1), Start: int64(i) * 64, End: int64(i+1) * 64}
	}

	fetcher := &priorityFetcherStub{calls: make(chan priorityFetchCall, 64)}
	manager.Register("stream-seek-ramp", spans, fetcher)
	defer manager.Stop("stream-seek-ramp")

	drainWithTimestamps := func() []time.Time {
		var arrivals []time.Time
		for {
			select {
			case <-fetcher.calls:
				arrivals = append(arrivals, time.Now())
			case <-time.After(150 * time.Millisecond):
				return arrivals
			}
		}
	}

	// Play sequentially through several windows first.
	for i := range 4 {
		manager.NotifyRead("stream-seek-ramp", int64(i)*960)
		drainWithTimestamps()
	}

	// Seek to a distant, never-fetched part of the file (still within the
	// 800*64=51,200-byte span coverage), then land there (mirroring the
	// FUSE handle's real NotifyRead-after-seek sequence).
	manager.Seek("stream-seek-ramp", 40_000)
	manager.NotifyRead("stream-seek-ramp", 40_000)
	arrivals := drainWithTimestamps()
	if len(arrivals) != 20 {
		t.Fatalf("expected the window right after a seek to reach the full 20-fetch parallelism share, got %d", len(arrivals))
	}
	rampSpan := arrivals[len(arrivals)-1].Sub(arrivals[readAheadRampFloor-1])
	if rampSpan < 350*time.Millisecond {
		t.Fatalf("expected the window right after a seek to ramp gradually, got a %s climb from floor to full share (bug: bursting straight to full share)", rampSpan)
	}
}

type immediatePriorityFetcher struct{}

func (immediatePriorityFetcher) FetchRange(context.Context, SegmentRange) ([]byte, error) {
	return nil, nil
}

func (immediatePriorityFetcher) FetchRangePriority(context.Context, SegmentRange, FetchPriority) ([]byte, error) {
	return nil, nil
}

func BenchmarkReadAheadManagerCoalescedNotify(b *testing.B) {
	const spanSize = int64(750 << 10)
	spans := make([]SegmentSpan, 2_800)
	for i := range spans {
		spans[i] = SegmentSpan{
			SegmentID: int64(i + 1),
			Start:     int64(i) * spanSize,
			End:       int64(i+1) * spanSize,
		}
	}
	manager := NewReadAheadManager(512 << 20)
	manager.Register("benchmark-coalesce", spans, immediatePriorityFetcher{})
	manager.NotifyRead("benchmark-coalesce", 0)
	b.Cleanup(func() { manager.Stop("benchmark-coalesce") })

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		manager.NotifyRead("benchmark-coalesce", 64<<10)
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

func TestAdaptiveArticleLimitExpandsLargeStreams(t *testing.T) {
	const spanSize = int64(398336)
	spans := make([]SegmentSpan, 500)
	for i := range spans {
		spans[i] = SegmentSpan{
			SegmentID: int64(i + 1),
			MessageID: fmt.Sprintf("<msg%d>", i+1),
			Start:     int64(i) * spanSize,
			End:       int64(i+1) * spanSize,
		}
	}
	got := adaptiveArticleLimit(spans, 0, 512<<20, 40, true)
	if got <= 40 {
		t.Fatalf("expected large stream article limit above configured 40, got %d", got)
	}
	if got > highBitrateMaxArticleBufferSize {
		t.Fatalf("expected article limit capped at %d, got %d", highBitrateMaxArticleBufferSize, got)
	}
	if got := adaptiveArticleLimit(spans, 0, 512<<20, 40, false); got != 40 {
		t.Fatalf("expected normal stream to keep configured 40 article limit, got %d", got)
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

// TestRegisterMetaLeavesOtherSessionsForegroundServingAlone guards the fix
// for a real production incident (2026-08-23): RegisterMeta used to also
// tear down another session's foreground serving loop the instant a new one
// opened for the same VirtualFileID, on the assumption that this always
// meant the same client seeking. That assumption is false whenever two
// genuinely independent reads of the same file overlap (confirmed live: a
// second, unrelated request cut an unrelated 500MB transfer off after 322
// bytes) -- and since a client retries a cut-off read by reopening the
// file, which re-enters this same path, two such reads could keep killing
// each other's retries indefinitely. RegisterMeta must still stop the other
// session's read-ahead (safe: it only affects not-yet-requested speculative
// prefetch), but must never have any way to reach into and cancel its
// actual foreground transfer -- confirmed structurally here, not just
// behaviorally: SessionMeta no longer has a Cancel field at all, so there is
// no hook left for this function to call even if it wanted to.
func TestRegisterMetaLeavesOtherSessionsForegroundServingAlone(t *testing.T) {
	manager := NewReadAheadManager(32)
	fetcher := &priorityFetcherStub{calls: make(chan priorityFetchCall, 2)}

	manager.Register("old-session", []SegmentSpan{{SegmentID: 1, MessageID: "<msg1>", Start: 0, End: 128}}, fetcher)
	manager.RegisterMeta("old-session", SessionMeta{VirtualFileID: 99})
	manager.NotifyRead("old-session", 0)
	oldCall := <-fetcher.calls

	manager.Register("new-session", []SegmentSpan{{SegmentID: 1, MessageID: "<msg1>", Start: 0, End: 128}}, fetcher)
	manager.RegisterMeta("new-session", SessionMeta{VirtualFileID: 99})

	select {
	case <-oldCall.ctx.Done():
		// Expected: read-ahead for the superseded session is stopped.
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected the superseded session's read-ahead to be cancelled")
	}

	for _, s := range manager.ActiveSessions() {
		if s.SessionID == "old-session" {
			t.Fatal("expected the superseded session to no longer be tracked for read-ahead")
		}
	}

	manager.Stop("new-session")
}

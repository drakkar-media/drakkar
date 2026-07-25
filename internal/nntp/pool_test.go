package nntp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeSession struct {
	calls *atomic.Int32
}

func (s *fakeSession) Body(ctx context.Context, messageID string) ([]byte, error) {
	s.calls.Add(1)
	time.Sleep(10 * time.Millisecond)
	return []byte(messageID), nil
}

func (s *fakeSession) Stat(ctx context.Context, messageID string) error { return nil }
func (s *fakeSession) Close() error                                     { return nil }

func TestPooledSourceReusesSessions(t *testing.T) {
	var created atomic.Int32
	var calls atomic.Int32
	source := NewPooledSource(context.Background(), func(ctx context.Context) (BodySession, error) {
		created.Add(1)
		return &fakeSession{calls: &calls}, nil
	}, 2)

	// keepWarmLoop proactively dials up to minWarmConns connections in the
	// background as soon as the pool is created (independent of any real
	// call) -- wait for that one-time warm-up to settle before measuring
	// reuse, otherwise this test races keepWarm's own dial.
	waitForOpenSessions(t, source, 2)
	warmedCount := created.Load()

	for i := 0; i < 3; i++ {
		body, err := source.Body(context.Background(), "<msg>")
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "<msg>" {
			t.Fatalf("got %q", string(body))
		}
	}
	// The real calls above must reuse the already-warmed connections, not
	// trigger fresh ones.
	if created.Load() != warmedCount {
		t.Fatalf("expected no new sessions created beyond the %d warmed at startup, got %d total", warmedCount, created.Load())
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 body calls, got %d", calls.Load())
	}
}

// waitForOpenSessions polls Stats() until at least `want` connections are
// open (active+idle) or fails the test after a short timeout -- used to
// deterministically wait out keepWarmLoop's async startup dial instead of a
// fixed sleep.
func waitForOpenSessions(t *testing.T, source *PooledSource, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		active, idle := source.Stats()
		if active+idle >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d open sessions", want)
}

type controllableSession struct {
	block   chan struct{} // if non-nil, Body waits on this before returning
	failErr error         // if non-nil, Body returns this error instead of a body
}

func (s *controllableSession) Body(ctx context.Context, messageID string) ([]byte, error) {
	if s.block != nil {
		<-s.block
	}
	if s.failErr != nil {
		return nil, s.failErr
	}
	return []byte(messageID), nil
}

func (s *controllableSession) Stat(ctx context.Context, messageID string) error { return nil }
func (s *controllableSession) Close() error                                     { return nil }

// TestPooledSourceWakesWaiterAfterDiscard reproduces the Brothers Under Fire
// streaming stall: with the pool full, a waiter parked in acquire()'s
// wait-select must be woken when the in-flight session errors out (discard,
// not release) -- not just when a session is released back to idle. Before
// notifyFreed existed, this waiter would block forever on a request context
// with no deadline (matching a real FUSE/WebDAV read), since nothing is ever
// pushed to p.idle and ctx.Done() never fires.
func TestPooledSourceWakesWaiterAfterDiscard(t *testing.T) {
	var created atomic.Int32
	unblockA := make(chan struct{})
	failErr := errors.New("boom")

	source := NewPooledSource(context.Background(), func(ctx context.Context) (BodySession, error) {
		if created.Add(1) == 1 {
			return &controllableSession{block: unblockA, failErr: failErr}, nil
		}
		return &controllableSession{}, nil
	}, 1)

	doneA := make(chan struct{})
	go func() {
		_, _ = source.Body(context.Background(), "<a>")
		close(doneA)
	}()
	time.Sleep(20 * time.Millisecond) // let A acquire the only slot and block inside Body

	resultB := make(chan error, 1)
	go func() {
		_, err := source.Body(context.Background(), "<b>")
		resultB <- err
	}()
	time.Sleep(20 * time.Millisecond) // let B park in acquire()'s wait-select (pool is full)

	close(unblockA) // A's Body() now returns failErr -> discard(), not release()
	<-doneA

	select {
	case err := <-resultB:
		if err != nil {
			t.Fatalf("goroutine B: unexpected error %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine parked in acquire() was never woken after discard freed a slot (lost wakeup)")
	}

	if created.Load() != 2 {
		t.Fatalf("expected 2 sessions created (A discarded, B opened fresh), got %d", created.Load())
	}
}

func TestPooledSourceCapsOpenSessions(t *testing.T) {
	var created atomic.Int32
	source := NewPooledSource(context.Background(), func(ctx context.Context) (BodySession, error) {
		created.Add(1)
		return &fakeSession{calls: &atomic.Int32{}}, nil
	}, 2)

	done := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		go func() {
			_, _ = source.Body(context.Background(), "<msg>")
			done <- struct{}{}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	if created.Load() > 2 {
		t.Fatalf("expected <=2 sessions created, got %d", created.Load())
	}
}

// TestPooledSourceProactivelyWarmsConnections guards the fix for a real
// production complaint (2026-07-25): resuming playback after a pause felt
// like a full cold start every time, because idleTimeout's sweep closes
// every connection after just 30s idle and nothing redialed them until a
// real read paid for the handshake itself. keepWarmLoop must proactively
// establish minWarmConns connections in the background, with zero calls to
// Body/Stat ever made -- so they're already sitting idle and ready before
// the first real request arrives.
func TestPooledSourceProactivelyWarmsConnections(t *testing.T) {
	var created atomic.Int32
	source := NewPooledSource(context.Background(), func(ctx context.Context) (BodySession, error) {
		created.Add(1)
		return &fakeSession{calls: &atomic.Int32{}}, nil
	}, 5)

	waitForOpenSessions(t, source, minWarmConns)

	active, idle := source.Stats()
	if idle < minWarmConns {
		t.Fatalf("expected at least %d idle warmed connections with zero requests made, got active=%d idle=%d", minWarmConns, active, idle)
	}
	if created.Load() < int32(minWarmConns) {
		t.Fatalf("expected keepWarmLoop to have dialed at least %d connections, got %d", minWarmConns, created.Load())
	}
}

// TestPooledSourceKeepWarmRespectsMaxOpen guards against keepWarmLoop
// over-warming a pool whose maxOpen is smaller than minWarmConns -- it must
// clamp to maxOpen, not exceed it.
func TestPooledSourceKeepWarmRespectsMaxOpen(t *testing.T) {
	var created atomic.Int32
	source := NewPooledSource(context.Background(), func(ctx context.Context) (BodySession, error) {
		created.Add(1)
		return &fakeSession{calls: &atomic.Int32{}}, nil
	}, 1)

	waitForOpenSessions(t, source, 1)
	time.Sleep(20 * time.Millisecond) // let any over-eager extra dial happen if the clamp were missing

	if created.Load() > 1 {
		t.Fatalf("expected keepWarmLoop to clamp to maxOpen=1, got %d sessions created", created.Load())
	}
}

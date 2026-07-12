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

	for i := 0; i < 3; i++ {
		body, err := source.Body(context.Background(), "<msg>")
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "<msg>" {
			t.Fatalf("got %q", string(body))
		}
	}
	if created.Load() != 1 {
		t.Fatalf("expected 1 session created, got %d", created.Load())
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 body calls, got %d", calls.Load())
	}
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

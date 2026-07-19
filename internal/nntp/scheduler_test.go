package nntp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/stream"
)

type orderedSource struct {
	mu    sync.Mutex
	order []string
	wait  time.Duration
}

func (s *orderedSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	s.mu.Lock()
	s.order = append(s.order, messageID)
	s.mu.Unlock()
	time.Sleep(s.wait)
	return []byte(messageID), nil
}

func TestScheduledSourcePrioritizesHighQueue(t *testing.T) {
	src := &orderedSource{wait: 20 * time.Millisecond}
	scheduler := NewScheduledSource(context.Background(), src, 1, 8)

	done := make(chan struct{}, 3)
	go func() {
		_, _ = scheduler.BodyPriority(context.Background(), "low-1", stream.PriorityBackground)
		done <- struct{}{}
	}()
	time.Sleep(5 * time.Millisecond)
	go func() {
		_, _ = scheduler.BodyPriority(context.Background(), "low-2", stream.PriorityBackground)
		done <- struct{}{}
	}()
	time.Sleep(5 * time.Millisecond)
	go func() {
		_, _ = scheduler.BodyPriority(context.Background(), "high-1", stream.PriorityInteractive)
		done <- struct{}{}
	}()

	for i := 0; i < 3; i++ {
		<-done
	}

	if len(src.order) != 3 {
		t.Fatalf("unexpected order %#v", src.order)
	}
	if src.order[0] != "low-1" || src.order[1] != "high-1" || src.order[2] != "low-2" {
		t.Fatalf("unexpected order %#v", src.order)
	}
}

// startTrackingSource records when Body is first CALLED (not when it
// returns) for each messageID, so a test can prove a request started
// promptly even if the fake source itself is artificially slow to complete.
type startTrackingSource struct {
	mu      sync.Mutex
	started map[string]time.Time
	wait    time.Duration
}

func (s *startTrackingSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	s.mu.Lock()
	if s.started == nil {
		s.started = make(map[string]time.Time)
	}
	s.started[messageID] = time.Now()
	s.mu.Unlock()
	time.Sleep(s.wait)
	return []byte(messageID), nil
}

func (s *startTrackingSource) startedAt(messageID string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.started[messageID]
	return t, ok
}

// TestScheduledSourceBackgroundLaneNotBlockedByForeground guards the
// nzbdav-parity fix: calibration/health-check (low priority) must get its own
// dedicated worker lane, never blocked behind a busy foreground (high/medium)
// lane -- matching nzbdav's health check bypassing its download semaphore
// entirely. With a single shared worker (the pre-fix behaviour), a
// long-running high-priority fetch would starve a concurrently-issued
// low-priority one until the worker freed up; with a dedicated background
// lane, the low-priority fetch starts immediately regardless.
func TestScheduledSourceBackgroundLaneNotBlockedByForeground(t *testing.T) {
	src := &startTrackingSource{wait: 200 * time.Millisecond}
	// One foreground worker (kept busy for 200ms by "high-1"), one dedicated
	// background worker.
	scheduler := NewScheduledSourceLanes(context.Background(), src, 1, 1, 8)

	testStart := time.Now()
	go func() {
		_, _ = scheduler.BodyPriority(context.Background(), "high-1", stream.PriorityInteractive)
	}()
	time.Sleep(10 * time.Millisecond) // let high-1 claim the sole foreground worker

	done := make(chan struct{})
	go func() {
		_, _ = scheduler.BodyPriority(context.Background(), "low-1", stream.PriorityBackground)
		close(done)
	}()
	<-done

	startedAt, ok := src.startedAt("low-1")
	if !ok {
		t.Fatal("low-1 never started")
	}
	if d := startedAt.Sub(testStart); d > 100*time.Millisecond {
		t.Fatalf("expected background fetch to START promptly via its own lane, started %v after test began (foreground worker was busy for 200ms)", d)
	}
}

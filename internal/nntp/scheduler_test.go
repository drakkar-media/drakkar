package nntp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hjongedijk/drakkar/internal/stream"
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
	scheduler := NewScheduledSource(src, 1, 8)

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

func TestScheduledSourceLimitsBackgroundWhenStreamingActive(t *testing.T) {
	src := &orderedSource{wait: 40 * time.Millisecond}
	scheduler := NewScheduledSource(src, 3, 8)
	scheduler.SetBackgroundBudget(1, func() int { return 1 })

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
	if src.order[2] != "low-2" {
		t.Fatalf("expected second background request to be delayed, got %#v", src.order)
	}
}

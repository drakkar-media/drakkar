package nntp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type blockingSource struct {
	active atomic.Int32
	max    atomic.Int32
	wait   time.Duration
}

func (s *blockingSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	active := s.active.Add(1)
	for {
		prev := s.max.Load()
		if active <= prev || s.max.CompareAndSwap(prev, active) {
			break
		}
	}
	time.Sleep(s.wait)
	s.active.Add(-1)
	return []byte("ok"), nil
}

func TestLimitedSourceBoundsConcurrency(t *testing.T) {
	src := &blockingSource{wait: 20 * time.Millisecond}
	limited := NewLimitedSource(src, 2)
	done := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		go func() {
			_, _ = limited.Body(context.Background(), "<msg>")
			done <- struct{}{}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	if src.max.Load() > 2 {
		t.Fatalf("expected max concurrency <=2, got %d", src.max.Load())
	}
}

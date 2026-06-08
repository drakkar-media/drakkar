package nntp

import (
	"context"
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
	source := NewPooledSource(func(ctx context.Context) (BodySession, error) {
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

func TestPooledSourceCapsOpenSessions(t *testing.T) {
	var created atomic.Int32
	source := NewPooledSource(func(ctx context.Context) (BodySession, error) {
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

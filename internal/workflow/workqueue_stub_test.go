package workflow

import (
	"context"
	"sync"
)

// workQueueStub is an in-memory WorkQueuer for unit tests (no Redis needed).
type workQueueStub struct {
	mu    sync.Mutex
	items map[int64]int // libraryItemID → priority
}

func newWorkQueueStub() *workQueueStub {
	return &workQueueStub{items: make(map[int64]int)}
}

func (s *workQueueStub) Push(_ context.Context, libraryItemID int64, priority int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.items[libraryItemID]; !ok || priority > existing {
		s.items[libraryItemID] = priority
	}
}

func (s *workQueueStub) Depth(_ context.Context) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.items))
}

func (s *workQueueStub) Start(_ context.Context, _ func(context.Context, int64)) error {
	return nil
}

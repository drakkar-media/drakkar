package cache

import (
	"context"
	"sync"
)

type FetchFunc func(context.Context) ([]byte, error)

type SingleFlight struct {
	mu      sync.Mutex
	flights map[string]*flight
}

type flight struct {
	done chan struct{}
	data []byte
	err  error
}

func NewSingleFlight() *SingleFlight {
	return &SingleFlight{flights: make(map[string]*flight)}
}

// Do deduplicates concurrent fetches for the same key. If a fetch is already
// in progress, the caller waits and shares the result.
//
// The underlying fetch runs with a detached (non-cancellable) context so that
// a prefetch context being cancelled by a seek does not propagate failure to
// interactive readers waiting on the same key. Callers can still cancel their
// own wait by cancelling ctx — they will receive ctx.Err() but the fetch
// continues so the result is available to any remaining waiters.
func (s *SingleFlight) Do(ctx context.Context, key string, fn FetchFunc) ([]byte, error) {
	s.mu.Lock()
	if active, ok := s.flights[key]; ok {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-active.done:
			return active.data, active.err
		}
	}
	active := &flight{done: make(chan struct{})}
	s.flights[key] = active
	s.mu.Unlock()

	// Use a detached context so that cancelling the owner (e.g. a prefetch
	// whose window was invalidated by a seek) does not abort the fetch and
	// poison all waiting interactive readers.
	active.data, active.err = fn(context.WithoutCancel(ctx))
	close(active.done)

	s.mu.Lock()
	delete(s.flights, key)
	s.mu.Unlock()

	return active.data, active.err
}

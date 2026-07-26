package nntp

import (
	"context"
	"errors"
)

// LimitedSource wraps an ArticleSource with a semaphore bounding how many
// Body/Stat calls run against it concurrently, independent of how many
// callers are waiting.
//
// LimitedSource is safe for concurrent use.
type LimitedSource struct {
	source ArticleSource
	sem    chan struct{}
}

// NewLimitedSource creates a LimitedSource wrapping source with at most
// maxConcurrent in-flight calls. A non-positive maxConcurrent is treated as 1
// rather than as unlimited.
func NewLimitedSource(source ArticleSource, maxConcurrent int) *LimitedSource {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &LimitedSource{
		source: source,
		sem:    make(chan struct{}, maxConcurrent),
	}
}

// Body fetches messageID's raw article body, blocking until a concurrency
// slot is available or ctx is cancelled.
func (s *LimitedSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	if s == nil || s.source == nil {
		return nil, errors.New("limited source unavailable")
	}
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.sem }()
	return s.source.Body(ctx, messageID)
}

// Stat checks messageID's existence under the same concurrency limit as
// Body, preferring the wrapped source's own Stat when it implements
// StatSource and falling back to a full body fetch otherwise.
func (s *LimitedSource) Stat(ctx context.Context, messageID string) error {
	if s == nil || s.source == nil {
		return errors.New("limited source unavailable")
	}
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.sem }()
	if statSource, ok := s.source.(StatSource); ok {
		return statSource.Stat(ctx, messageID)
	}
	_, err := s.source.Body(ctx, messageID)
	return err
}

package nntp

import (
	"context"
	"errors"
)

type LimitedSource struct {
	source ArticleSource
	sem    chan struct{}
}

func NewLimitedSource(source ArticleSource, maxConcurrent int) *LimitedSource {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &LimitedSource{
		source: source,
		sem:    make(chan struct{}, maxConcurrent),
	}
}

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

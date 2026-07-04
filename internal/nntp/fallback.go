package nntp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hjongedijk/drakkar/internal/stream"
)

// retryBackoff is applied between fallback attempt rounds so a retry against
// a source that just failed/throttled doesn't immediately hammer it again.
// Kept short since this sits in the interactive streaming read path.
const retryBackoff = 200 * time.Millisecond

// waitBackoff pauses briefly before the next retry round, returning ctx.Err()
// if the context is cancelled first.
func waitBackoff(ctx context.Context) error {
	timer := time.NewTimer(retryBackoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type NamedArticleSource struct {
	Name   string
	Source ArticleSource
}

type StatSource interface {
	Stat(ctx context.Context, messageID string) error
}

type FallbackSource struct {
	sources []NamedArticleSource
	retries int
}

func NewFallbackSource(sources []NamedArticleSource, retries int) *FallbackSource {
	filtered := make([]NamedArticleSource, 0, len(sources))
	for _, source := range sources {
		if source.Source == nil {
			continue
		}
		filtered = append(filtered, source)
	}
	if retries < 0 {
		retries = 0
	}
	// With a single provider, retrying the same missing article immediately just
	// burns another full round-trip and halves throughput under heavy backlog.
	// Keep multi-provider fallback retries, but avoid duplicate misses on solo setups.
	if len(filtered) <= 1 {
		retries = 0
	}
	return &FallbackSource{
		sources: filtered,
		retries: retries,
	}
}

func (s *FallbackSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	return s.BodyPriority(ctx, messageID, stream.PriorityInteractive)
}

func (s *FallbackSource) BodyPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, error) {
	if s == nil || len(s.sources) == 0 {
		return nil, errors.New("fallback source unavailable")
	}
	var failures []error
	for attempt := 0; attempt <= s.retries; attempt++ {
		if attempt > 0 {
			if err := waitBackoff(ctx); err != nil {
				return nil, err
			}
		}
		for _, source := range s.sources {
			body, err := fetchArticleBody(ctx, source.Source, messageID, priority)
			if err == nil {
				return body, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			failures = append(failures, fmt.Errorf("%s attempt %d: %w", sourceName(source), attempt+1, err))
		}
	}
	return nil, errors.Join(failures...)
}

func (s *FallbackSource) Stat(ctx context.Context, messageID string) error {
	if s == nil || len(s.sources) == 0 {
		return errors.New("fallback source unavailable")
	}
	var failures []error
	for attempt := 0; attempt <= s.retries; attempt++ {
		if attempt > 0 {
			if err := waitBackoff(ctx); err != nil {
				return err
			}
		}
		for _, source := range s.sources {
			err := fetchArticleStat(ctx, source.Source, messageID)
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failures = append(failures, fmt.Errorf("%s attempt %d: %w", sourceName(source), attempt+1, err))
		}
	}
	return errors.Join(failures...)
}

func fetchArticleBody(ctx context.Context, source ArticleSource, messageID string, priority stream.FetchPriority) ([]byte, error) {
	if prioritySource, ok := source.(PriorityArticleSource); ok {
		return prioritySource.BodyPriority(ctx, messageID, priority)
	}
	return source.Body(ctx, messageID)
}

func fetchArticleStat(ctx context.Context, source ArticleSource, messageID string) error {
	if statSource, ok := source.(StatSource); ok {
		return statSource.Stat(ctx, messageID)
	}
	_, err := source.Body(ctx, messageID)
	return err
}

func sourceName(source NamedArticleSource) string {
	name := strings.TrimSpace(source.Name)
	if name == "" {
		return "provider"
	}
	return name
}

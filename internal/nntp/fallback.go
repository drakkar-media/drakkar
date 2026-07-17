package nntp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/drakkar-media/drakkar/internal/stream"
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
	breaker *providerCircuitBreaker
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
		breaker: newProviderCircuitBreaker(),
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
	// conclusivelyMissing remembers, within this single call, which sources
	// already gave a definitive "no such article" answer for this exact
	// messageID (per classifyCacheableError's missingArticleTTL
	// classification -- not a throttle/circuit-open failure, which is
	// already handled per-round by s.breaker.Allow). Without this, a
	// multi-provider config with retries>0 would re-query the same source
	// for the same known-dead article on every retry round. Currently
	// dormant in production: NewFallbackSource forces retries=0 for a
	// single-provider setup, so the outer loop only ever runs once -- this
	// only matters the moment a second provider is configured.
	conclusivelyMissing := make(map[string]bool)
	for attempt := 0; attempt <= s.retries; attempt++ {
		if attempt > 0 {
			if err := waitBackoff(ctx); err != nil {
				return nil, err
			}
		}
		for _, source := range s.sources {
			name := sourceName(source)
			if conclusivelyMissing[name] {
				continue
			}
			if !s.breaker.Allow(name) {
				failures = append(failures, fmt.Errorf("%s attempt %d: %w", name, attempt+1, ErrProviderCircuitOpen))
				continue
			}
			body, err := fetchArticleBody(ctx, source.Source, messageID, priority)
			slog.Debug("fallback: article fetch attempt", "provider", name, "messageID", messageID, "attempt", attempt+1, "ok", err == nil)
			if err == nil {
				s.breaker.RecordSuccess(name)
				return body, nil
			}
			s.breaker.RecordFailure(name, err)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if ttl, cacheable := classifyCacheableError(err); cacheable && ttl == missingArticleTTL {
				conclusivelyMissing[name] = true
			}
			failures = append(failures, fmt.Errorf("%s attempt %d: %w", name, attempt+1, err))
		}
	}
	return nil, errors.Join(failures...)
}

func (s *FallbackSource) Stat(ctx context.Context, messageID string) error {
	if s == nil || len(s.sources) == 0 {
		return errors.New("fallback source unavailable")
	}
	var failures []error
	// See the matching comment in BodyPriority.
	conclusivelyMissing := make(map[string]bool)
	for attempt := 0; attempt <= s.retries; attempt++ {
		if attempt > 0 {
			if err := waitBackoff(ctx); err != nil {
				return err
			}
		}
		for _, source := range s.sources {
			name := sourceName(source)
			if conclusivelyMissing[name] {
				continue
			}
			if !s.breaker.Allow(name) {
				failures = append(failures, fmt.Errorf("%s attempt %d: %w", name, attempt+1, ErrProviderCircuitOpen))
				continue
			}
			err := fetchArticleStat(ctx, source.Source, messageID)
			if err == nil {
				s.breaker.RecordSuccess(name)
				return nil
			}
			s.breaker.RecordFailure(name, err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if ttl, cacheable := classifyCacheableError(err); cacheable && ttl == missingArticleTTL {
				conclusivelyMissing[name] = true
			}
			failures = append(failures, fmt.Errorf("%s attempt %d: %w", name, attempt+1, err))
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

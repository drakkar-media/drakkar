package nntp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hjongedijk/drakkar/internal/stream"
)

type NamedArticleSource struct {
	Name   string
	Source ArticleSource
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

func fetchArticleBody(ctx context.Context, source ArticleSource, messageID string, priority stream.FetchPriority) ([]byte, error) {
	if prioritySource, ok := source.(PriorityArticleSource); ok {
		return prioritySource.BodyPriority(ctx, messageID, priority)
	}
	return source.Body(ctx, messageID)
}

func sourceName(source NamedArticleSource) string {
	name := strings.TrimSpace(source.Name)
	if name == "" {
		return "provider"
	}
	return name
}

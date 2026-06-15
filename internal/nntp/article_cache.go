package nntp

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/hjongedijk/drakkar/internal/stream"
)

// missingArticleTTL matches the reference implementation's 24-hour cache for
// articles that return 430 "No Such Article" from the Usenet provider.
const missingArticleTTL = 24 * time.Hour

// CachedFallbackSource wraps FallbackSource and caches message IDs that
// returned a "not found" (430) status. Repeated fetches for the same dead
// article are short-circuited for 24 hours without hitting NNTP at all.
type CachedFallbackSource struct {
	inner *FallbackSource

	mu      sync.Mutex
	missing map[string]time.Time // messageID → expiry
}

func NewCachedFallbackSource(inner *FallbackSource) *CachedFallbackSource {
	return &CachedFallbackSource{
		inner:   inner,
		missing: make(map[string]time.Time),
	}
}

func (s *CachedFallbackSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	return s.BodyPriority(ctx, messageID, stream.PriorityInteractive)
}

func (s *CachedFallbackSource) BodyPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, error) {
	if s.isMissing(messageID) {
		return nil, errArticleNotFound(messageID)
	}
	body, err := s.inner.BodyPriority(ctx, messageID, priority)
	if err != nil && isNotFoundError(err) {
		s.markMissing(messageID)
	}
	return body, err
}

func (s *CachedFallbackSource) Stat(ctx context.Context, messageID string) error {
	if s.isMissing(messageID) {
		return errArticleNotFound(messageID)
	}
	err := s.inner.Stat(ctx, messageID)
	if err != nil && isNotFoundError(err) {
		s.markMissing(messageID)
	}
	return err
}

func (s *CachedFallbackSource) isMissing(messageID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.missing[messageID]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(s.missing, messageID)
		return false
	}
	return true
}

func (s *CachedFallbackSource) markMissing(messageID string) {
	s.mu.Lock()
	s.missing[messageID] = time.Now().Add(missingArticleTTL)
	s.mu.Unlock()
}

// Evict removes expired entries. Call periodically to avoid unbounded growth.
func (s *CachedFallbackSource) Evict() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, expiry := range s.missing {
		if now.After(expiry) {
			delete(s.missing, id)
		}
	}
}

// MissingCount returns the number of currently cached missing article IDs.
func (s *CachedFallbackSource) MissingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.missing)
}

func errArticleNotFound(messageID string) error {
	return articleNotFoundError(messageID)
}

type articleNotFoundError string

func (e articleNotFoundError) Error() string {
	return "article not found (cached): " + string(e)
}

// isNotFoundError detects a 430-class error from the NNTP client.
// The client formats it as "unexpected BODY status 430".
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status 430") ||
		strings.Contains(msg, "status 423") || // no such group
		strings.Contains(msg, "article not found")
}

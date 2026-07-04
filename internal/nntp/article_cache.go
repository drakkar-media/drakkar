package nntp

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/hjongedijk/drakkar/internal/stream"
)

// missingArticleTTL matches the reference implementation's 24-hour cache for
// genuinely absent articles.
const missingArticleTTL = 24 * time.Hour

// throttleTTL is used for NNTP 430 responses. This provider (and others)
// returns status 430 both for "no such article" and for a transient
// connection/transfer-limit throttle — this project has hit that ambiguity
// and fixed it multiple times on the queue/download path (see policy.go's
// KeyNNTPThrottled). A short TTL means a throttled article is retried again
// soon instead of being blacklisted from streaming for a full day.
const throttleTTL = 30 * time.Second

// CachedFallbackSource wraps FallbackSource and caches message IDs that
// recently failed, so repeated fetches for the same dead/throttled article
// are short-circuited without hitting NNTP every time.
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
	if err != nil {
		if ttl, ok := classifyCacheableError(err); ok {
			s.markMissing(messageID, ttl)
		}
	}
	return body, err
}

func (s *CachedFallbackSource) Stat(ctx context.Context, messageID string) error {
	if s.isMissing(messageID) {
		return errArticleNotFound(messageID)
	}
	err := s.inner.Stat(ctx, messageID)
	if err != nil {
		if ttl, ok := classifyCacheableError(err); ok {
			s.markMissing(messageID, ttl)
		}
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

func (s *CachedFallbackSource) markMissing(messageID string, ttl time.Duration) {
	s.mu.Lock()
	s.missing[messageID] = time.Now().Add(ttl)
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

// classifyCacheableError decides whether an error is worth short-circuiting
// on repeat fetches, and for how long. Status 430 is NOT treated as a
// definitive "article missing" signal — this provider (like others) also
// returns 430 for a transient connection/transfer-limit throttle, and
// conflating the two caused releases to be permanently blacklisted for a
// throttle blip (fixed repeatedly on the queue/download path already).
func classifyCacheableError(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "status 430"):
		return throttleTTL, true
	case strings.Contains(msg, "status 423"), // no such group
		strings.Contains(msg, "article not found"):
		return missingArticleTTL, true
	default:
		return 0, false
	}
}

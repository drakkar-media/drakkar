package nntp

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/drakkar-media/drakkar/internal/cache"
	"github.com/drakkar-media/drakkar/internal/stream"
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

	// statFlight/bodyFlight coalesce concurrent Stat/BodyPriority calls for
	// the same messageID that all miss the missing-article cache at once.
	// Without this, isMissing()+markMissing() being two separate critical
	// sections left a window where two concurrent callers (e.g. earlyChecker
	// calls from several download-worker-pool goroutines importing the same
	// selected release) could both see isMissing()==false and both issue a
	// real duplicate fetch/STAT against the NNTP provider for a
	// never-before-cached, currently-dead/throttled article. Kept as two
	// separate SingleFlight instances (rather than one shared by key) so a
	// concurrent Stat and BodyPriority for the same messageID never share a
	// flight — they call different inner methods and BodyPriority callers
	// need the actual bytes back, not a Stat-shaped (nil, err) result.
	statFlight *cache.SingleFlight
	bodyFlight *cache.SingleFlight
}

func NewCachedFallbackSource(inner *FallbackSource) *CachedFallbackSource {
	return &CachedFallbackSource{
		inner:      inner,
		missing:    make(map[string]time.Time),
		statFlight: cache.NewSingleFlight(),
		bodyFlight: cache.NewSingleFlight(),
	}
}

func (s *CachedFallbackSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	return s.BodyPriority(ctx, messageID, stream.PriorityInteractive)
}

func (s *CachedFallbackSource) BodyPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, error) {
	if s.isMissing(messageID) {
		return nil, errArticleNotFound(messageID)
	}
	return s.bodyFlight.Do(ctx, messageID, func(ctx context.Context) ([]byte, error) {
		body, err := s.inner.BodyPriority(ctx, messageID, priority)
		if err != nil {
			if ttl, ok := classifyCacheableError(err); ok {
				s.markMissing(messageID, ttl)
			}
		}
		return body, err
	})
}

func (s *CachedFallbackSource) Stat(ctx context.Context, messageID string) error {
	if s.isMissing(messageID) {
		return errArticleNotFound(messageID)
	}
	_, err := s.statFlight.Do(ctx, messageID, func(ctx context.Context) ([]byte, error) {
		err := s.inner.Stat(ctx, messageID)
		if err != nil {
			if ttl, ok := classifyCacheableError(err); ok {
				s.markMissing(messageID, ttl)
			}
		}
		return nil, err
	})
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
	slog.Debug("article cache: marking missing", "messageID", messageID, "ttl", ttl)
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

// Is reports this as ErrArticleMissing to errors.Is, so a cache HIT for an
// already-confirmed-missing article is indistinguishable from a fresh 430
// STAT response to any caller that classifies errors this way (e.g.
// database.isArticlePermanentlyMissing). Without this, calibrate.go's
// permanent-vs-transient check called Exists() again, got this cached
// error back instead of the original ErrArticleMissing, and misclassified
// an already-confirmed-permanently-missing article as "transient, retry
// later" -- causing the exact same segments to be retried forever, every
// health-check pass, instead of being marked calibrated_at once and never
// touched again.
func (e articleNotFoundError) Is(target error) bool {
	return target == ErrArticleMissing
}

// classifyCacheableError decides whether an error is worth short-circuiting
// on repeat fetches, and for how long. Status 430 (like 423) IS treated as a
// definitive "article missing" signal: per RFC 3977 and Newshosting's own
// support docs, both codes mean the specific article is gone (past
// retention or removed) — a property of that article, not the provider.
// An earlier version of this code treated 430 as ambiguous/transient based
// on a misread pattern (see isThrottleLikeErr in circuit_breaker.go for the
// full explanation); that caused genuinely-dead articles to be retried
// instead of cached as missing.
func classifyCacheableError(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	if errors.Is(err, ErrProviderCircuitOpen) {
		return throttleTTL, true
	}
	if errors.Is(err, ErrArticleMissing) {
		return missingArticleTTL, true
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "status 430"), // no such article
		strings.Contains(msg, "status 423"), // no such article/group
		strings.Contains(msg, "article not found"),
		strings.Contains(msg, "article missing"): // ErrArticleMissing's message, e.g. from client.go's Stat() on a 430 STAT response
		return missingArticleTTL, true
	default:
		return 0, false
	}
}

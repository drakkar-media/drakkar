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

// confirmMissingDelay spaces the single extra confirmation check
// confirmMissing performs before a 430/423 response is trusted enough to
// cache for missingArticleTTL (24h) and, via classifyCacheableError's
// callers (preflight, calibration, health checks), before it's treated as
// grounds to blocklist a release forever. Mirrors calibrate.go's
// confirmPermanentCRCMismatch, which needed the identical treatment after
// two confirmed live false positives for CRC on this same provider — this
// file's own classifyCacheableError doc comment records that 430's meaning
// has already been reversed once (ambiguous -> always-definitive) based on
// a misread pattern, so a second, independent, delayed sample is cheap
// insurance against the same class of mistake recurring here.
var confirmMissingDelay = 10 * time.Second

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

// NewCachedFallbackSource wraps inner with an empty missing-article cache and
// its own statFlight/bodyFlight SingleFlight pairs.
func NewCachedFallbackSource(inner *FallbackSource) *CachedFallbackSource {
	return &CachedFallbackSource{
		inner:      inner,
		missing:    make(map[string]time.Time),
		statFlight: cache.NewSingleFlight(),
		bodyFlight: cache.NewSingleFlight(),
	}
}

// Body fetches messageID at interactive priority; see BodyPriority.
func (s *CachedFallbackSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	return s.BodyPriority(ctx, messageID, stream.PriorityInteractive)
}

// BodyPriority returns messageID's body, short-circuiting with a cached
// not-found error (without contacting inner) if it was recently classified
// as missing/throttled. Otherwise it fetches through bodyFlight so
// concurrent callers for the same messageID share one inner fetch and one
// missing-cache update.
func (s *CachedFallbackSource) BodyPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, error) {
	if s.isMissing(messageID) {
		return nil, errArticleNotFound(messageID)
	}
	return s.bodyFlight.Do(ctx, messageID, func(ctx context.Context) ([]byte, error) {
		body, err := s.inner.BodyPriority(ctx, messageID, priority)
		if err != nil {
			if ttl, ok := classifyCacheableError(err); ok {
				s.markMissing(messageID, s.confirmedTTL(ctx, messageID, priority, true, ttl))
			}
		}
		return body, err
	})
}

// Stat mirrors Stat at the default interactive priority. Equivalent to
// StatPriority with stream.PriorityInteractive.
func (s *CachedFallbackSource) Stat(ctx context.Context, messageID string) error {
	return s.StatPriority(ctx, messageID, stream.PriorityInteractive)
}

// StatPriority mirrors BodyPriority's missing-cache short-circuit and
// coalescing, but for an existence check instead of a body fetch.
func (s *CachedFallbackSource) StatPriority(ctx context.Context, messageID string, priority stream.FetchPriority) error {
	if s.isMissing(messageID) {
		return errArticleNotFound(messageID)
	}
	_, err := s.statFlight.Do(ctx, messageID, func(ctx context.Context) ([]byte, error) {
		err := s.inner.StatPriority(ctx, messageID, priority)
		if err != nil {
			if ttl, ok := classifyCacheableError(err); ok {
				s.markMissing(messageID, s.confirmedTTL(ctx, messageID, priority, false, ttl))
			}
		}
		return nil, err
	})
	return err
}

// confirmedTTL returns ttl unchanged, EXCEPT for the missingArticleTTL
// (24h, "confirmed gone") case at background priority: there, a single
// 430/423 sample is not trusted enough on its own to cache for a full day
// (and, via every caller downstream, to justify blocklisting a release
// forever) -- see confirmMissingDelay's doc comment. One extra, independent,
// delayed re-check via s.inner directly (bypassing this cache) must agree
// before the long TTL is used; if it doesn't, a short throttleTTL is used
// instead so the next real request gets a fresh look soon rather than being
// stuck on an unconfirmed verdict for a day.
//
// Deliberately NOT applied at interactive/read-ahead priority: a real-time
// playback read that hits a genuinely missing segment must fail fast, not
// wait an extra confirmMissingDelay before the error even surfaces --
// confirmation only gates how long the NEGATIVE result is cached, not
// whether this one caller's own failure is reported, so skipping it here
// costs nothing but latency for the (much rarer) interactive path.
func (s *CachedFallbackSource) confirmedTTL(ctx context.Context, messageID string, priority stream.FetchPriority, isBody bool, ttl time.Duration) time.Duration {
	if ttl != missingArticleTTL || priority >= stream.PriorityInteractive {
		return ttl
	}
	timer := time.NewTimer(confirmMissingDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return throttleTTL // Couldn't confirm -- don't commit to a day-long verdict.
	case <-timer.C:
	}
	var confirmErr error
	if isBody {
		_, confirmErr = s.inner.BodyPriority(ctx, messageID, priority)
	} else {
		confirmErr = s.inner.StatPriority(ctx, messageID, priority)
	}
	if confirmTTL, ok := classifyCacheableError(confirmErr); ok && confirmTTL == missingArticleTTL {
		return missingArticleTTL // Independently confirmed -- trust it for the full day.
	}
	return throttleTTL
}

// isMissing reports whether messageID is currently cached as missing,
// lazily evicting the entry first if its TTL has already expired.
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

// markMissing records messageID as missing until ttl elapses.
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

// articleNotFoundError is the sentinel error type for a missing-cache hit;
// see its Is method for why it masquerades as ErrArticleMissing.
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

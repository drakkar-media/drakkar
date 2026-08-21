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

// confirmMissingBudget bounds confirmMissing's own detached re-check fetch.
// Generous relative to clientSession's own 30s internal timeout (client.go)
// so it never truncates a legitimate in-flight confirmation, while still
// giving this fire-and-forget goroutine its own explicit ceiling rather
// than running unbounded on context.Background().
const confirmMissingBudget = 45 * time.Second

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
				s.markAndMaybeConfirm(messageID, priority, true, ttl)
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
				s.markAndMaybeConfirm(messageID, priority, false, ttl)
			}
		}
		return nil, err
	})
	return err
}

// markAndMaybeConfirm caches messageID as missing for ttl, immediately and
// synchronously, EXCEPT for the missingArticleTTL (24h, "confirmed gone")
// classification at below-interactive priority: there, a single 430/423
// sample is not trusted enough on its own to cache for a full day (and, via
// every caller downstream, to justify blocklisting a release forever) --
// see confirmMissingDelay's doc comment. This caches a short, provisional
// throttleTTL right away and hands off to confirmMissing, running in a
// detached background goroutine, to independently re-check and upgrade the
// cache entry to the full 24h TTL if confirmed.
//
// Previously the delayed re-check ran synchronously, inside the
// bodyFlight/statFlight singleflight critical section, before this
// function's caller (BodyPriority/StatPriority) could return at all.
// bodyFlight/statFlight coalesce every concurrent caller for the same
// messageID onto ONE shared call regardless of priority, so a low-priority
// caller (a background health check or read-ahead prefetch) that happened
// to be first through the door held every OTHER caller for that same
// messageID -- including a concurrent INTERACTIVE playback read -- for the
// full confirmMissingDelay plus a live re-fetch (10-40s). That defeated the
// "interactive reads fail fast" guarantee this function's own doc comment
// already promised, but only for whichever caller happened to start the
// flight; every rider on the same flight paid the delay regardless of its
// own priority. Confirmed live 2026-08-20: this is the same mechanism
// documented as a possible cause of unexplained multi-second playback
// stalls with no error anywhere in the chain.
//
// The confirmation step only ever refines how long the cached NEGATIVE
// result is trusted for -- it never changes what's returned to the caller
// that triggered it, since that value was already read from inner before
// this function is even called. Moving it out of the critical section
// therefore changes nothing about the eventual cached outcome, only
// removes the blocking.
func (s *CachedFallbackSource) markAndMaybeConfirm(messageID string, priority stream.FetchPriority, isBody bool, ttl time.Duration) {
	if ttl != missingArticleTTL || priority >= stream.PriorityInteractive {
		s.markMissing(messageID, ttl)
		return
	}
	s.markMissing(messageID, throttleTTL) // provisional, until confirmed
	go s.confirmMissing(messageID, priority, isBody)
}

// confirmMissing waits confirmMissingDelay, then independently re-checks
// messageID via s.inner directly (bypassing this cache) and upgrades its
// cached TTL to the full missingArticleTTL if the recheck agrees it's
// genuinely gone -- otherwise leaves the provisional throttleTTL mark
// already in place. Runs detached from any caller's context (the caller
// that triggered this has already returned by the time this executes) with
// its own bounded budget, matching the pattern every other fire-and-forget
// goroutine in this codebase uses.
func (s *CachedFallbackSource) confirmMissing(messageID string, priority stream.FetchPriority, isBody bool) {
	if confirmMissingDone != nil {
		defer confirmMissingDone()
	}
	timer := time.NewTimer(confirmMissingDelay)
	defer timer.Stop()
	<-timer.C
	ctx, cancel := context.WithTimeout(context.Background(), confirmMissingBudget)
	defer cancel()
	var confirmErr error
	if isBody {
		_, confirmErr = s.inner.BodyPriority(ctx, messageID, priority)
	} else {
		confirmErr = s.inner.StatPriority(ctx, messageID, priority)
	}
	if confirmTTL, ok := classifyCacheableError(confirmErr); ok && confirmTTL == missingArticleTTL {
		s.markMissing(messageID, missingArticleTTL) // Independently confirmed -- trust it for the full day.
	}
}

// confirmMissingDone, when non-nil, is called synchronously at the very end
// of every confirmMissing invocation. Test-only hook: since confirmMissing
// runs detached in its own goroutine by design (see markAndMaybeConfirm),
// tests need a deterministic way to wait for it to fully finish before
// asserting on its effects or restoring shared package state like
// confirmMissingDelay -- polling a side effect (e.g. a stub's call count)
// does not establish a happens-before edge for OTHER memory the goroutine
// touches, and the race detector correctly flags that as unsynchronized.
var confirmMissingDone func()

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

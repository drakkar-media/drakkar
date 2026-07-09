package nntp

import (
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// ErrProviderCircuitOpen is returned when a provider's circuit breaker is
// currently tripped — callers should treat this the same as any other
// transient/throttle error (retry later), never as proof the requested
// article/content is actually missing or corrupt.
var ErrProviderCircuitOpen = errors.New("provider circuit open — recent throttling, cooling down")

// breakerTripThreshold/breakerBaseCooldown/breakerMaxCooldown started out as
// a direct port of nzbdav's ProviderCircuitBreaker (trip after 3, cool down
// 60s doubling to a 5-minute cap). Live traffic showed that model doesn't
// fit Drakkar: with many concurrent calibration/verification goroutines
// sharing one provider, a single sub-second hiccup produces dozens of
// "failures" almost simultaneously (observed: 87 in one second), blowing
// past a threshold of 3 instantly and then locking every caller out for a
// full minute+ even though the same logs showed the provider serving 40-80
// successful requests within the following few seconds. The provider's real
// failure pattern is brief multi-second blips, not sustained outages, so the
// threshold must be high enough to absorb a concurrency burst and the
// cooldown short enough to match how fast it actually recovers.
// Raised again 15→40: the periodic backlog-search batch (many BullMQ workers
// each running a multi-file preflight check, itself fanning out up to 8
// concurrent per-file NNTP checks) produces a legitimately larger worst-case
// concurrent burst than the calibration-goroutine pattern this was last
// tuned for (observed: 374 requests piling up against an already-tripped
// breaker within about a second of the batch firing) — at 15, that burst
// tripped the breaker on every single batch cycle, blocking that whole
// cycle's candidates together and making every item in the batch look like
// it had no viable release.
// failureWindow bounds what counts as "consecutive" — a failure more than
// this long after the previous one starts a fresh streak instead of
// extending an old one, so failures that trickle in slowly over minutes
// don't get misread as one continuous outage.
const (
	breakerTripThreshold = 40
	breakerBaseCooldown  = 3 * time.Second
	breakerMaxCooldown   = 30 * time.Second
	failureWindow        = 2 * time.Second
)

type breakerState struct {
	consecutiveFailures int
	lastFailureAt       time.Time
	disabledUntil       time.Time
	cooldown            time.Duration
}

// providerCircuitBreaker tracks per-provider health across all callers
// sharing a FallbackSource. Safe for concurrent use.
type providerCircuitBreaker struct {
	mu    sync.Mutex
	state map[string]*breakerState
}

func newProviderCircuitBreaker() *providerCircuitBreaker {
	return &providerCircuitBreaker{state: make(map[string]*breakerState)}
}

// Allow reports whether a request to this provider should be attempted right
// now. A provider with no recorded failures is always allowed.
func (b *providerCircuitBreaker) Allow(name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.state[name]
	if st == nil {
		return true
	}
	allowed := !time.Now().Before(st.disabledUntil)
	if !allowed {
		slog.Debug("circuit breaker: request blocked", "provider", name, "disabledUntil", st.disabledUntil)
	}
	return allowed
}

// RecordSuccess fully resets the breaker for this provider.
func (b *providerCircuitBreaker) RecordSuccess(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, had := b.state[name]; had {
		slog.Debug("circuit breaker: reset on success", "provider", name)
	}
	delete(b.state, name)
}

// RecordFailure only counts throttle-like errors against the breaker — a
// genuine "article not found" is a property of that specific article, not
// evidence the provider itself is unhealthy, and must not trip the breaker.
func (b *providerCircuitBreaker) RecordFailure(name string, err error) {
	if !isThrottleLikeErr(err) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	st := b.state[name]
	if st == nil {
		st = &breakerState{cooldown: breakerBaseCooldown}
		b.state[name] = st
	}
	if !st.lastFailureAt.IsZero() && now.Sub(st.lastFailureAt) > failureWindow {
		// Gap since the last failure was long enough that this isn't a
		// continuation of the same burst — start counting fresh.
		st.consecutiveFailures = 0
	}
	st.lastFailureAt = now
	st.consecutiveFailures++
	slog.Debug("circuit breaker: failure recorded", "provider", name,
		"consecutiveFailures", st.consecutiveFailures, "threshold", breakerTripThreshold)
	if st.consecutiveFailures < breakerTripThreshold {
		return
	}
	st.disabledUntil = now.Add(st.cooldown)
	slog.Debug("circuit breaker: tripped", "provider", name, "cooldown", st.cooldown, "disabledUntil", st.disabledUntil)
	if st.cooldown < breakerMaxCooldown {
		st.cooldown *= 2
		if st.cooldown > breakerMaxCooldown {
			st.cooldown = breakerMaxCooldown
		}
	}
}

// isThrottleLikeErr reports whether err looks like transient provider
// throttling/rate-limiting/connection trouble rather than a permanent,
// article-specific failure. Mirrors the "status 430" bucket in
// classifyCacheableError (article_cache.go) — deliberately narrow, since
// over-matching here would let genuinely-missing content quietly disable a
// perfectly healthy provider.
func isThrottleLikeErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrProviderCircuitOpen) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "status 430") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "provider circuit open")
}

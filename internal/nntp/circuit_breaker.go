package nntp

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrProviderCircuitOpen is returned when a provider's circuit breaker is
// currently tripped — callers should treat this the same as any other
// transient/throttle error (retry later), never as proof the requested
// article/content is actually missing or corrupt.
var ErrProviderCircuitOpen = errors.New("provider circuit open — recent throttling, cooling down")

// breakerTripThreshold/breakerBaseCooldown/breakerMaxCooldown mirror nzbdav's
// ProviderCircuitBreaker (Clients/Usenet/Connections/ProviderCircuitBreaker.cs):
// trip after 3 consecutive throttle-like failures, cool down starting at 60s
// and doubling on each further trip up to a 5-minute cap, full reset on the
// next success. This exists specifically so that when a provider starts
// returning "status 430" under load, Drakkar backs off that provider instead
// of continuing to hammer it at full concurrency — which is what turns a
// brief throttle blip into a sustained storm that makes every verification
// read inconclusive.
const (
	breakerTripThreshold = 3
	breakerBaseCooldown  = 60 * time.Second
	breakerMaxCooldown   = 5 * time.Minute
)

type breakerState struct {
	consecutiveFailures int
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
	return !time.Now().Before(st.disabledUntil)
}

// RecordSuccess fully resets the breaker for this provider.
func (b *providerCircuitBreaker) RecordSuccess(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
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
	st := b.state[name]
	if st == nil {
		st = &breakerState{cooldown: breakerBaseCooldown}
		b.state[name] = st
	}
	st.consecutiveFailures++
	if st.consecutiveFailures < breakerTripThreshold {
		return
	}
	st.disabledUntil = time.Now().Add(st.cooldown)
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

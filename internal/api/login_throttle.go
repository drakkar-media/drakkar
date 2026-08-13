package api

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const (
	defaultLoginFailureThreshold = 3
	defaultLoginMaxEntries       = 4096
	defaultLoginMaxConcurrent    = 4
	defaultLoginMaxPerIdentity   = 2
)

type loginThrottleConfig struct {
	now              func() time.Time
	failureThreshold int
	baseBackoff      time.Duration
	maxBackoff       time.Duration
	entryTTL         time.Duration
	busyRetry        time.Duration
	maxEntries       int
	maxConcurrent    int
	maxPerIdentity   int
}

type loginThrottleEntry struct {
	failures    int
	inFlight    int
	nextAllowed time.Time
	lastSeen    time.Time
}

// loginThrottle bounds bcrypt concurrency and applies exponential backoff to
// both client-IP and normalized-account identities. Entries contain no raw
// usernames and are expired/evicted under a fixed memory cap.
type loginThrottle struct {
	mu             sync.Mutex
	config         loginThrottleConfig
	entries        map[string]*loginThrottleEntry
	globalInFlight int
	operations     uint64
}

type loginAttempt struct {
	throttle *loginThrottle
	keys     []string
	done     bool
}

func newLoginThrottle(config loginThrottleConfig) *loginThrottle {
	defaults := loginThrottleConfig{
		now:              time.Now,
		failureThreshold: defaultLoginFailureThreshold,
		baseBackoff:      time.Second,
		maxBackoff:       30 * time.Second,
		entryTTL:         15 * time.Minute,
		busyRetry:        time.Second,
		maxEntries:       defaultLoginMaxEntries,
		maxConcurrent:    defaultLoginMaxConcurrent,
		maxPerIdentity:   defaultLoginMaxPerIdentity,
	}
	if config.now != nil {
		defaults.now = config.now
	}
	if config.failureThreshold > 0 {
		defaults.failureThreshold = config.failureThreshold
	}
	if config.baseBackoff > 0 {
		defaults.baseBackoff = config.baseBackoff
	}
	if config.maxBackoff > 0 {
		defaults.maxBackoff = config.maxBackoff
	}
	if config.entryTTL > 0 {
		defaults.entryTTL = config.entryTTL
	}
	if config.busyRetry > 0 {
		defaults.busyRetry = config.busyRetry
	}
	if config.maxEntries > 0 {
		defaults.maxEntries = config.maxEntries
	}
	if config.maxConcurrent > 0 {
		defaults.maxConcurrent = config.maxConcurrent
	}
	if config.maxPerIdentity > 0 {
		defaults.maxPerIdentity = config.maxPerIdentity
	}
	return &loginThrottle{config: defaults, entries: make(map[string]*loginThrottleEntry)}
}

// begin reserves one bounded bcrypt slot. A nil attempt means the caller must
// reject without doing password work and retry after the returned duration.
func (t *loginThrottle) begin(clientIP, username string) (*loginAttempt, time.Duration) {
	now := t.config.now()
	keys := loginThrottleKeys(clientIP, username)

	t.mu.Lock()
	defer t.mu.Unlock()
	t.operations++
	if t.operations%64 == 0 || len(t.entries) >= t.config.maxEntries {
		t.pruneExpiredLocked(now)
	}
	if t.globalInFlight >= t.config.maxConcurrent {
		return nil, t.config.busyRetry
	}

	var retryAfter time.Duration
	for _, key := range keys {
		entry := t.entries[key]
		if entry == nil {
			continue
		}
		if wait := entry.nextAllowed.Sub(now); wait > retryAfter {
			retryAfter = wait
		}
		if entry.inFlight >= t.config.maxPerIdentity && t.config.busyRetry > retryAfter {
			retryAfter = t.config.busyRetry
		}
	}
	if retryAfter > 0 {
		return nil, retryAfter
	}

	missing := 0
	for _, key := range keys {
		if t.entries[key] == nil {
			missing++
		}
	}
	if !t.ensureCapacityLocked(missing, keys, now) {
		return nil, t.config.busyRetry
	}
	for _, key := range keys {
		entry := t.entries[key]
		if entry == nil {
			entry = &loginThrottleEntry{}
			t.entries[key] = entry
		}
		entry.inFlight++
		entry.lastSeen = now
	}
	t.globalInFlight++
	return &loginAttempt{throttle: t, keys: keys}, 0
}

// finish releases a bcrypt slot and publishes either success or a failed
// credential attempt. It is idempotent so deferred cleanup cannot underflow
// counters if a caller also finishes explicitly.
func (a *loginAttempt) finish(success bool) {
	if a == nil || a.throttle == nil {
		return
	}
	t := a.throttle
	now := t.config.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if a.done {
		return
	}
	a.done = true
	if t.globalInFlight > 0 {
		t.globalInFlight--
	}
	for _, key := range a.keys {
		entry := t.entries[key]
		if entry == nil {
			continue
		}
		if entry.inFlight > 0 {
			entry.inFlight--
		}
		entry.lastSeen = now
		if success {
			entry.failures = 0
			entry.nextAllowed = time.Time{}
			continue
		}
		entry.failures++
		if entry.failures >= t.config.failureThreshold {
			entry.nextAllowed = now.Add(t.backoff(entry.failures))
		}
	}
}

func (t *loginThrottle) backoff(failures int) time.Duration {
	delay := t.config.baseBackoff
	for step := t.config.failureThreshold; step < failures && delay < t.config.maxBackoff; step++ {
		if delay > t.config.maxBackoff/2 {
			return t.config.maxBackoff
		}
		delay *= 2
	}
	if delay > t.config.maxBackoff {
		return t.config.maxBackoff
	}
	return delay
}

func (t *loginThrottle) pruneExpiredLocked(now time.Time) {
	for key, entry := range t.entries {
		if entry.inFlight == 0 && now.Sub(entry.lastSeen) >= t.config.entryTTL {
			delete(t.entries, key)
		}
	}
}

func (t *loginThrottle) ensureCapacityLocked(missing int, protectedKeys []string, now time.Time) bool {
	for len(t.entries)+missing > t.config.maxEntries {
		var (
			oldestKey     string
			oldest        time.Time
			oldestBlocked bool
		)
		for key, entry := range t.entries {
			if loginKeyProtected(protectedKeys, key) || entry.inFlight > 0 {
				continue
			}
			blocked := entry.nextAllowed.After(now)
			if oldestKey != "" {
				if oldestBlocked != blocked {
					if !oldestBlocked {
						continue
					}
				} else if !entry.lastSeen.Before(oldest) {
					continue
				}
			}
			oldestKey = key
			oldest = entry.lastSeen
			oldestBlocked = blocked
		}
		if oldestKey == "" {
			return false
		}
		delete(t.entries, oldestKey)
	}
	return true
}

func loginKeyProtected(keys []string, candidate string) bool {
	for _, key := range keys {
		if key == candidate {
			return true
		}
	}
	return false
}

func loginThrottleKeys(clientIP, username string) []string {
	if strings.TrimSpace(clientIP) == "" {
		clientIP = "unknown"
	}
	normalizedAccount := strings.ToLower(strings.TrimSpace(username))
	accountHash := sha256.Sum256([]byte(normalizedAccount))
	return []string{
		"ip:" + clientIP,
		"account:" + hex.EncodeToString(accountHash[:]),
	}
}

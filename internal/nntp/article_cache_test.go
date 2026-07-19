package nntp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// statOnlySource implements StatSource (and the bare ArticleSource interface,
// since NamedArticleSource requires it) so fetchArticleStat's type assertion
// picks the cheap Stat path instead of falling back to a full Body fetch --
// matching the real client.go code path that produces the bare ErrArticleMissing
// sentinel this test guards against.
type statOnlySource struct {
	statErr   error
	statCalls int
}

func (s *statOnlySource) Body(ctx context.Context, messageID string) ([]byte, error) {
	return nil, errors.New("statOnlySource: Body should not be called in this test")
}

func (s *statOnlySource) Stat(ctx context.Context, messageID string) error {
	s.statCalls++
	return s.statErr
}

// TestArticleNotFoundErrorIsErrArticleMissing guards a gap found in the
// 2026-07-19 production incident investigation: a cache HIT for an
// already-confirmed-missing article (served by CachedFallbackSource.Stat/
// BodyPriority via errArticleNotFound, once the miss is cached) must still
// be recognized by errors.Is(err, ErrArticleMissing) exactly like a fresh
// 430 STAT response would be. Without this, callers that re-classify an
// error to decide "is this permanent" (e.g.
// database.isArticlePermanentlyMissing, called from the periodic segment
// calibration health check) saw a cache hit as a different error entirely
// and treated an already-confirmed-permanently-missing article as
// transient -- causing the exact same segments to be retried forever,
// every 15-minute health-check pass, instead of being marked done once.
func TestArticleNotFoundErrorIsErrArticleMissing(t *testing.T) {
	err := errArticleNotFound("<msg1>")
	if !errors.Is(err, ErrArticleMissing) {
		t.Fatal("expected errArticleNotFound's cached-miss error to satisfy errors.Is(err, ErrArticleMissing)")
	}
	if errors.Is(err, ErrProviderCircuitOpen) {
		t.Fatal("expected errArticleNotFound not to falsely match an unrelated sentinel")
	}
}

// TestClassifyCacheableErrorMatchesStatArticleMissing guards against a real
// production gap: client.go's Stat() returns the bare ErrArticleMissing
// sentinel (message "article missing") on a 430 STAT response, not a
// "status 430"/"article not found" formatted string. classifyCacheableError
// used to only match those two substrings, so the 24h missing-article cache
// never activated for the cheap STAT path -- exercised on every selected-
// release fetch/retry via earlyChecker -- causing a confirmed-dead article
// to be re-verified live against the NNTP provider forever instead of being
// served from cache.
func TestClassifyCacheableErrorMatchesStatArticleMissing(t *testing.T) {
	// Bare sentinel, as returned directly by a StatSource implementation.
	if ttl, ok := classifyCacheableError(ErrArticleMissing); !ok || ttl != missingArticleTTL {
		t.Fatalf("bare ErrArticleMissing: got (%v, %v), want (%v, true)", ttl, ok, missingArticleTTL)
	}

	// The exact wrapped/joined shape FallbackSource.Stat produces around a
	// single source's failure.
	wrapped := errors.Join(errors.New("provider1 attempt 1: article missing"))
	if ttl, ok := classifyCacheableError(wrapped); !ok || ttl != missingArticleTTL {
		t.Fatalf("string-wrapped article missing: got (%v, %v), want (%v, true)", ttl, ok, missingArticleTTL)
	}
}

// TestCachedFallbackSourceStatCachesArticleMissingFromStatPath is an
// end-to-end regression test for the same gap: a Stat() failure classified
// as ErrArticleMissing must populate the 24h missing-article cache so a
// second Stat() call for the same messageID is served from cache instead of
// re-hitting the (here, single) NNTP provider.
func TestCachedFallbackSourceStatCachesArticleMissingFromStatPath(t *testing.T) {
	stub := &statOnlySource{statErr: ErrArticleMissing}
	fallback := NewFallbackSource([]NamedArticleSource{{Name: "provider1", Source: stub}}, 1)
	cached := NewCachedFallbackSource(fallback)

	ctx := context.Background()
	if err := cached.Stat(ctx, "msg1"); err == nil {
		t.Fatal("expected Stat to return an error for a missing article")
	}
	if got := stub.statCalls; got != 1 {
		t.Fatalf("expected exactly 1 live Stat call, got %d", got)
	}
	if got := cached.MissingCount(); got != 1 {
		t.Fatalf("expected the missing-article cache to hold 1 entry after a classified failure, got %d", got)
	}

	// A second Stat() for the same messageID must be served from cache, not
	// re-issue a live NNTP STAT.
	if err := cached.Stat(ctx, "msg1"); err == nil {
		t.Fatal("expected cached Stat to still report the article as missing")
	}
	if got := stub.statCalls; got != 1 {
		t.Fatalf("expected the cache to short-circuit the second Stat call (still 1 live call), got %d", got)
	}
}

// blockingStatSource is a StatSource whose Stat call blocks on a channel
// until the test releases it, so concurrent callers can be forced to
// genuinely overlap inside the "live" fetch instead of happening to run
// sequentially (which would hide a check-then-act race).
type blockingStatSource struct {
	statErr   error
	start     chan struct{}
	statCalls atomic.Int32
}

func (s *blockingStatSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	return nil, errors.New("blockingStatSource: Body should not be called in this test")
}

func (s *blockingStatSource) Stat(ctx context.Context, messageID string) error {
	s.statCalls.Add(1)
	<-s.start
	return s.statErr
}

// TestCachedFallbackSourceStatCoalescesConcurrentMisses guards against the
// check-then-act race between isMissing() and markMissing(): the real
// network call used to sit unprotected between them, so two concurrent
// Stat() calls for the same never-before-cached, currently-dead/throttled
// messageID could both observe isMissing()==false and both issue a real
// duplicate fetch against the NNTP provider. This is reachable in production
// via earlyChecker's SegmentFetcher.Exists calls, which run concurrently
// across the download-worker pool for the same selected-release import
// attempt. CachedFallbackSource.Stat now funnels concurrent callers for the
// same messageID through statFlight (a SingleFlight), so only the first
// caller performs the live Stat and the rest share its result.
//
// Because blockingStatSource's Stat call blocks until the test explicitly
// releases it, every goroutine launched below that reaches statFlight.Do
// before the release either becomes the one leader that calls Stat, or
// observes the leader's in-flight call and waits -- deterministically
// proving at most one live call happens, regardless of goroutine scheduling.
func TestCachedFallbackSourceStatCoalescesConcurrentMisses(t *testing.T) {
	stub := &blockingStatSource{statErr: ErrArticleMissing, start: make(chan struct{})}
	fallback := NewFallbackSource([]NamedArticleSource{{Name: "provider1", Source: stub}}, 1)
	cached := NewCachedFallbackSource(fallback)

	ctx := context.Background()
	const goroutines = 8
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = cached.Stat(ctx, "msg-concurrent")
		}(i)
	}
	// Give every goroutine a chance to reach (and, pre-fix, race past)
	// statFlight.Do before releasing the single blocked leader -- without
	// this, a fast leader could run to completion before the others are even
	// scheduled, which would look the same as the fix working whether or not
	// it actually does.
	time.Sleep(50 * time.Millisecond)
	close(stub.start)
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Fatalf("call %d: expected an error for a missing article", i)
		}
	}
	if got := stub.statCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 live Stat call across %d concurrent callers, got %d", goroutines, got)
	}
	if got := cached.MissingCount(); got != 1 {
		t.Fatalf("expected the missing-article cache to hold 1 entry, got %d", got)
	}
}

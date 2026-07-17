package nntp

import (
	"context"
	"errors"
	"testing"
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

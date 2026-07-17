package nntp

import (
	"context"
	"errors"
	"testing"

	"github.com/drakkar-media/drakkar/internal/stream"
)

type failThenOKSource struct {
	errs  []error
	body  []byte
	calls int
}

func (s *failThenOKSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	if s.calls < len(s.errs) && s.errs[s.calls] != nil {
		err := s.errs[s.calls]
		s.calls++
		return nil, err
	}
	s.calls++
	return s.body, nil
}

type priorityAwareSource struct {
	body     []byte
	priority stream.FetchPriority
}

func (s *priorityAwareSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	return s.body, nil
}

func (s *priorityAwareSource) BodyPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, error) {
	s.priority = priority
	return s.body, nil
}

func TestFallbackSourceFallsThroughProviders(t *testing.T) {
	first := &failThenOKSource{errs: []error{errors.New("dial failed")}}
	second := &failThenOKSource{body: []byte("ok")}
	source := NewFallbackSource([]NamedArticleSource{
		{Name: "primary", Source: first},
		{Name: "backup", Source: second},
	}, 0)

	body, err := source.Body(context.Background(), "<msg1>")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("got %q", string(body))
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("unexpected calls primary=%d backup=%d", first.calls, second.calls)
	}
}

func TestFallbackSourceDoesNotRetrySingleProvider(t *testing.T) {
	only := &failThenOKSource{
		errs: []error{errors.New("timeout")},
		body: []byte("retry-ok"),
	}
	source := NewFallbackSource([]NamedArticleSource{{Name: "solo", Source: only}}, 1)

	_, err := source.Body(context.Background(), "<msg1>")
	if err == nil {
		t.Fatal("expected timeout")
	}
	if only.calls != 1 {
		t.Fatalf("expected 1 call, got %d", only.calls)
	}
}

// alwaysFailSource always returns the same error, counting how many times
// Body was called.
type alwaysFailSource struct {
	err   error
	calls int
}

func (s *alwaysFailSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	s.calls++
	return nil, s.err
}

// TestFallbackSourceSkipsConclusivelyMissingSourceOnRetryRounds guards
// against a real gap found in the 2026-07-17 exhaustive audit: with 2+
// providers configured (so NewFallbackSource keeps retries>0), a source
// that gave a definitive "no such article" answer (classified as
// missingArticleTTL by classifyCacheableError, e.g. ErrArticleMissing) was
// re-queried again on every subsequent retry round for the exact same
// messageID -- a wasted NNTP round-trip against a known-dead article that
// isThrottleLikeErr/the circuit breaker deliberately does not catch, since
// 430/423 are article-specific, not provider-health signals. This gap is
// currently dormant in production (a single-provider config forces
// retries=0), but reactivates the moment a second provider is configured.
func TestFallbackSourceSkipsConclusivelyMissingSourceOnRetryRounds(t *testing.T) {
	missing := &alwaysFailSource{err: ErrArticleMissing}
	other := &alwaysFailSource{err: errors.New("some other benign failure")}
	source := NewFallbackSource([]NamedArticleSource{
		{Name: "providerA", Source: missing},
		{Name: "providerB", Source: other},
	}, 1)

	_, err := source.Body(context.Background(), "<msg1>")
	if err == nil {
		t.Fatal("expected an error since neither source succeeds")
	}
	if missing.calls != 1 {
		t.Fatalf("expected the conclusively-missing source to be queried exactly once across both retry rounds, got %d", missing.calls)
	}
	if other.calls != 2 {
		t.Fatalf("expected the other source to still be queried on both retry rounds, got %d", other.calls)
	}
}

func TestFallbackSourcePreservesPriority(t *testing.T) {
	only := &priorityAwareSource{body: []byte("ok")}
	source := NewFallbackSource([]NamedArticleSource{{Name: "solo", Source: only}}, 0)

	body, err := source.BodyPriority(context.Background(), "<msg1>", stream.PriorityReadAhead)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("got %q", string(body))
	}
	if only.priority != stream.PriorityReadAhead {
		t.Fatalf("expected priority %d, got %d", stream.PriorityReadAhead, only.priority)
	}
}

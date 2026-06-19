package nntp

import (
	"context"
	"errors"
	"testing"

	"github.com/hjongedijk/drakkar/internal/stream"
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

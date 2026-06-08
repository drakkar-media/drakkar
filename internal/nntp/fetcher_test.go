package nntp

import (
	"context"
	"testing"

	"github.com/hjongedijk/drakkar/internal/stream"
	"github.com/hjongedijk/drakkar/internal/yenc"
)

type sourceStub struct {
	body []byte
}

func (s sourceStub) DecodedBody(ctx context.Context, messageID string) ([]byte, error) {
	return s.body, nil
}

type prioritySourceStub struct {
	body     []byte
	priority stream.FetchPriority
}

func (s *prioritySourceStub) DecodedBody(ctx context.Context, messageID string) ([]byte, error) {
	return s.body, nil
}

func (s *prioritySourceStub) DecodedBodyPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, error) {
	s.priority = priority
	return s.body, nil
}

type infoSourceStub struct {
	body []byte
	info yenc.PartInfo
}

func (s infoSourceStub) DecodedBody(ctx context.Context, messageID string) ([]byte, error) {
	return s.body, nil
}

func (s infoSourceStub) DecodedBodyInfo(ctx context.Context, messageID string) ([]byte, yenc.PartInfo, error) {
	return s.body, s.info, nil
}

func TestSegmentFetcherFetchRange(t *testing.T) {
	fetcher := NewSegmentFetcher(sourceStub{body: []byte("hello world")})
	got, err := fetcher.FetchRange(context.Background(), stream.SegmentRange{
		MessageID:    "<msg1>",
		RangeStart:   6,
		RangeEnd:     11,
		SegmentStart: 0,
		SegmentEnd:   11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "world" {
		t.Fatalf("got %q", string(got))
	}
}

func TestSegmentFetcherFetchRangePriority(t *testing.T) {
	source := &prioritySourceStub{body: []byte("hello world")}
	fetcher := NewSegmentFetcher(source)

	got, err := fetcher.FetchRangePriority(context.Background(), stream.SegmentRange{
		MessageID:    "<msg1>",
		RangeStart:   0,
		RangeEnd:     5,
		SegmentStart: 0,
		SegmentEnd:   11,
	}, stream.PriorityReadAhead)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q", string(got))
	}
	if source.priority != stream.PriorityReadAhead {
		t.Fatalf("expected priority %d, got %d", stream.PriorityReadAhead, source.priority)
	}
}

func TestSegmentFetcherFetchRangeInfoUsesActualPartOffsets(t *testing.T) {
	fetcher := NewSegmentFetcher(infoSourceStub{
		body: []byte("hello world"),
		info: yenc.PartInfo{Begin: 11, End: 21},
	})
	got, actual, err := fetcher.FetchRangeInfo(context.Background(), stream.SegmentRange{
		MessageID:    "<msg1>",
		RangeStart:   12,
		RangeEnd:     17,
		SegmentStart: 0,
		SegmentEnd:   11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "llo w" {
		t.Fatalf("got %q", string(got))
	}
	if actual.Start != 10 || actual.End != 21 {
		t.Fatalf("unexpected actual span %+v", actual)
	}
}

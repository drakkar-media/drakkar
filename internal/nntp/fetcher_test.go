package nntp

import (
	"context"
	"testing"

	"github.com/drakkar-media/drakkar/internal/stream"
	"github.com/drakkar-media/drakkar/internal/yenc"
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

// infoSourceWithInvalidateStub implements both DecodedArticleInfoSource and
// cacheInvalidator, matching the shape of the real production wrap chain
// (CachedDecodedSource / DiskCachedDecodedSource both implement
// InvalidateCached) so FetchRangeInfoPriority's type assertion succeeds.
type infoSourceWithInvalidateStub struct {
	body        []byte
	info        yenc.PartInfo
	invalidated []string
}

func (s *infoSourceWithInvalidateStub) DecodedBody(ctx context.Context, messageID string) ([]byte, error) {
	return s.body, nil
}

func (s *infoSourceWithInvalidateStub) DecodedBodyInfo(ctx context.Context, messageID string) ([]byte, yenc.PartInfo, error) {
	return s.body, s.info, nil
}

func (s *infoSourceWithInvalidateStub) InvalidateCached(messageID string) {
	s.invalidated = append(s.invalidated, messageID)
}

// TestSegmentFetcherFetchRangeInfoRejectsWildlyWrongPosition reproduces the
// live 2026-08-23 incident: a disk-cache entry silently held another
// segment's decoded bytes under the correct messageID key (no transport-level
// error anywhere), with a yEnc header declaring a real position ~90 segments
// away from where this segment is actually expected to be. The old behavior
// blindly trusted info.DecodedStart() and handed back a "corrected"
// SegmentSpan, which realignSpans then used to shift every later span in the
// file by the same huge delta -- permanently corrupting the whole file's
// layout from a single bad segment. A mismatch this large must be rejected as
// a fetch error instead, and the bad cached entry must be forgotten so a retry
// has a chance to get a genuinely correct copy.
func TestSegmentFetcherFetchRangeInfoRejectsWildlyWrongPosition(t *testing.T) {
	body := make([]byte, 1000)
	source := &infoSourceWithInvalidateStub{
		body: body,
		// Begin is 1-based; declares this article starts ~64.6MB into the
		// file when the caller expected it to start at 0 -- the exact shape
		// of the live incident (segment 0 of a 2482-segment file coming back
		// tagged with segment ~90's real position).
		info: yenc.PartInfo{Begin: 64601089, End: 64601089 + int64(len(body)) - 1},
	}
	fetcher := NewSegmentFetcher(source)
	_, _, err := fetcher.FetchRangeInfo(context.Background(), stream.SegmentRange{
		MessageID:    "<msg1>",
		RangeStart:   0,
		RangeEnd:     100,
		SegmentStart: 0,
		SegmentEnd:   716800,
	})
	if err == nil {
		t.Fatal("expected an error for a wildly-inconsistent declared position, got nil")
	}
	if len(source.invalidated) != 1 || source.invalidated[0] != "<msg1>" {
		t.Fatalf("expected InvalidateCached(<msg1>) to be called exactly once, got %v", source.invalidated)
	}
}

// TestSegmentFetcherFetchRangeInfoToleratesNormalYencDriftAtRealisticScale
// guards against the sanity bound above being too tight: a realistic ~700KB
// segment with a small (~1KB, well under 1% -- the documented normal yEnc
// decode-ratio variance) drift must still self-correct via the existing
// realignSpans path, not get rejected as a wild mismatch.
func TestSegmentFetcherFetchRangeInfoToleratesNormalYencDriftAtRealisticScale(t *testing.T) {
	const segmentWidth = 716800
	body := make([]byte, segmentWidth+1024)
	source := &infoSourceWithInvalidateStub{
		body: body,
		info: yenc.PartInfo{Begin: 1025, End: 1024 + int64(len(body))},
	}
	fetcher := NewSegmentFetcher(source)
	_, actual, err := fetcher.FetchRangeInfo(context.Background(), stream.SegmentRange{
		MessageID:    "<msg1>",
		RangeStart:   0,
		RangeEnd:     100,
		SegmentStart: 0,
		SegmentEnd:   segmentWidth,
	})
	if err != nil {
		t.Fatalf("expected normal small drift to be tolerated, got error: %v", err)
	}
	if actual.Start != 1024 {
		t.Fatalf("expected self-corrected start 1024, got %d", actual.Start)
	}
	if len(source.invalidated) != 0 {
		t.Fatalf("expected no invalidation for normal drift, got %v", source.invalidated)
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

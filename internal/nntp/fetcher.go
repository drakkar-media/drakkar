package nntp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/drakkar-media/drakkar/internal/metrics"
	"github.com/drakkar-media/drakkar/internal/stream"
	"github.com/drakkar-media/drakkar/internal/yenc"
)

// ArticleSource defines the operations required to fetch a raw (still
// yEnc-encoded) NNTP article body.
//
// Implementations must treat messageID as an opaque NNTP message identifier.
type ArticleSource interface {
	Body(ctx context.Context, messageID string) ([]byte, error)
}

// PriorityArticleSource extends ArticleSource with a priority-aware fetch,
// letting callers signal whether a request is on the interactive playback
// path or a lower-priority background one.
type PriorityArticleSource interface {
	ArticleSource
	BodyPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, error)
}

// PriorityStatSource extends StatSource with a priority-aware existence
// check, mirroring PriorityArticleSource -- needed so a background-priority
// Stat (e.g. the deep health check) can be gated the same as a
// background-priority Body fetch instead of silently reverting to
// unrestricted access at the pool.
type PriorityStatSource interface {
	StatSource
	StatPriority(ctx context.Context, messageID string, priority stream.FetchPriority) error
}

// minRealignSanityBound floors the tolerance FetchRangeInfoPriority allows
// between a segment's estimated and actually-declared decoded position,
// for the (rare) tiny final segment of a file where the segment-width-based
// tolerance alone would otherwise be too tight to absorb normal variance.
const minRealignSanityBound = 64 * 1024

// cacheInvalidator is implemented by decoded-article sources that cache
// results and can be told to forget one, so FetchRangeInfoPriority can evict
// a cached body that turned out not to match the article it's supposed to
// be, rather than leaving it to keep poisoning every future read of the same
// messageID. Both CachedDecodedSource and DiskCachedDecodedSource implement
// this, cascading through the wrap chain.
type cacheInvalidator interface {
	InvalidateCached(messageID string)
}

// SegmentFetcher resolves stream.SegmentRange requests against a
// DecodedArticleSource, mapping each requested byte range onto the article's
// actual decoded content and returning only the bytes that fall within it.
type SegmentFetcher struct {
	source DecodedArticleSource
}

// NewSegmentFetcher creates a SegmentFetcher that resolves segment fetches
// through source.
func NewSegmentFetcher(source DecodedArticleSource) *SegmentFetcher {
	return &SegmentFetcher{source: source}
}

// DecodedArticle fetches and returns the full decoded article body.
func (f *SegmentFetcher) DecodedArticle(ctx context.Context, messageID string) ([]byte, error) {
	if f == nil || f.source == nil {
		return nil, errors.New("nntp source unavailable")
	}
	if ps, ok := f.source.(PriorityDecodedArticleSource); ok {
		return ps.DecodedBodyPriority(ctx, messageID, stream.PriorityBackground)
	}
	return f.source.DecodedBody(ctx, messageID)
}

// DecodedSize fetches the full decoded article and returns its byte length.
// This is used during calibration to determine the actual decoded size of a
// segment rather than relying on estimates from the NZB bytes attribute.
func (f *SegmentFetcher) DecodedSize(ctx context.Context, messageID string) (int64, error) {
	decoded, err := f.DecodedArticle(ctx, messageID)
	if err != nil {
		return 0, err
	}
	return int64(len(decoded)), nil
}

// DecodedStart fetches messageID's decoded article and returns the yEnc
// header's declared absolute decoded-start offset (info.DecodedStart())
// alongside the decoded size. valid is false when the source can't supply
// PartInfo at all, or the header it returned doesn't pass yenc.PartInfo.Valid()
// -- callers must treat that as "position unknown", not "starts at 0".
func (f *SegmentFetcher) DecodedStart(ctx context.Context, messageID string) (start, size int64, valid bool, err error) {
	if f == nil || f.source == nil {
		return 0, 0, false, errors.New("nntp source unavailable")
	}
	var (
		decoded []byte
		info    yenc.PartInfo
	)
	switch src := f.source.(type) {
	case PriorityDecodedArticleInfoSource:
		decoded, info, err = src.DecodedBodyInfoPriority(ctx, messageID, stream.PriorityBackground)
	case DecodedArticleInfoSource:
		decoded, info, err = src.DecodedBodyInfo(ctx, messageID)
	default:
		size, err = f.DecodedSize(ctx, messageID)
		return 0, size, false, err
	}
	if err != nil {
		return 0, 0, false, err
	}
	if !info.Valid() {
		return 0, int64(len(decoded)), false, nil
	}
	return info.DecodedStart(), int64(len(decoded)), true, nil
}

// Exists verifies that the article exists without forcing a full decoded-body
// download when the underlying source supports NNTP STAT.
func (f *SegmentFetcher) Exists(ctx context.Context, messageID string) error {
	if f == nil || f.source == nil {
		return errors.New("nntp source unavailable")
	}
	if statSource, ok := f.source.(interface {
		Stat(context.Context, string) error
	}); ok {
		return statSource.Stat(ctx, messageID)
	}
	_, err := f.DecodedSize(ctx, messageID)
	return err
}

// FetchRange fetches segment's requested byte range at interactive priority;
// see FetchRangeInfoPriority.
func (f *SegmentFetcher) FetchRange(ctx context.Context, segment stream.SegmentRange) ([]byte, error) {
	return f.FetchRangePriority(ctx, segment, stream.PriorityInteractive)
}

// FetchRangePriority fetches segment's requested byte range at the given
// priority, discarding the SegmentSpan that FetchRangeInfoPriority also
// computes.
func (f *SegmentFetcher) FetchRangePriority(ctx context.Context, segment stream.SegmentRange, priority stream.FetchPriority) ([]byte, error) {
	block, _, err := f.FetchRangeInfoPriority(ctx, segment, priority)
	return block, err
}

// FetchRangeInfo fetches segment's requested byte range at interactive
// priority; see FetchRangeInfoPriority.
func (f *SegmentFetcher) FetchRangeInfo(ctx context.Context, segment stream.SegmentRange) ([]byte, stream.SegmentSpan, error) {
	return f.FetchRangeInfoPriority(ctx, segment, stream.PriorityInteractive)
}

// FetchRangeInfoPriority fetches and decodes segment.MessageID's article,
// then clips the decoded body to the caller-requested [RangeStart, RangeEnd)
// byte range.
//
// The NZB-declared segment offsets (segment.SegmentStart/SegmentEnd) are only
// estimates; when the article carries a valid yEnc PartInfo header, the
// actual decoded start/end (info.DecodedStart() and its length) are used
// instead to compute the returned span and range. Two estimate-vs-actual
// mismatches are handled without erroring:
//
//   - If the true decoded content starts after the estimated RangeStart, an
//     empty slice is returned along with the actual SegmentSpan so the caller
//     (DirectNzbReader) can realign its span table and retry.
//   - If the estimated RangeEnd overruns the true decoded length (yEnc decode
//     ratio varies slightly per article), the returned range is clamped to
//     what actually exists rather than failing.
//
// Returns:
//   - []byte: the requested slice of the decoded article, which may be
//     shorter than requested or empty per the cases above.
//   - stream.SegmentSpan: the segment's actual decoded start/end, for the
//     caller to reconcile against its estimated span table.
//
// Errors:
//   - returned when RangeEnd is before RangeStart, or when the underlying
//     fetch/decode fails.
func (f *SegmentFetcher) FetchRangeInfoPriority(ctx context.Context, segment stream.SegmentRange, priority stream.FetchPriority) ([]byte, stream.SegmentSpan, error) {
	if f == nil || f.source == nil {
		return nil, stream.SegmentSpan{}, errors.New("nntp source unavailable")
	}
	decodedSource, hasPriorityDecoded := f.source.(PriorityDecodedArticleSource)
	infoSource, infoOK := f.source.(PriorityDecodedArticleInfoSource)
	var (
		decoded []byte
		info    yenc.PartInfo
		err     error
	)
	if infoOK {
		decoded, info, err = infoSource.DecodedBodyInfoPriority(ctx, segment.MessageID, priority)
	} else if basicInfo, ok := f.source.(DecodedArticleInfoSource); ok {
		decoded, info, err = basicInfo.DecodedBodyInfo(ctx, segment.MessageID)
	} else if hasPriorityDecoded {
		decoded, err = decodedSource.DecodedBodyPriority(ctx, segment.MessageID, priority)
	} else {
		decoded, err = f.source.DecodedBody(ctx, segment.MessageID)
	}
	if err != nil {
		metrics.M.NNTPFetchFailures.Add(1)
		// context.Canceled is normal: parallel connections race to fetch the
		// same segment; the losers are cancelled after the winner succeeds.
		// Log those at DEBUG to avoid flooding the console.
		if errors.Is(err, context.Canceled) {
			slog.Debug("nntp fetch canceled", "messageID", segment.MessageID)
		} else {
			slog.Debug("nntp fetch failed", "messageID", segment.MessageID, "err", err)
		}
		return nil, stream.SegmentSpan{}, fmt.Errorf("fetch decoded article %s: %w", segment.MessageID, err)
	}
	metrics.M.NNTPArticlesFetched.Add(1)
	metrics.M.NNTPBytesFetched.Add(int64(len(decoded)))
	actualStart := segment.SegmentStart
	actualEnd := segment.SegmentEnd
	if info.Valid() {
		actualStart = info.DecodedStart()
		actualEnd = actualStart + int64(len(decoded))
	}
	// A legitimate yEnc decode-ratio estimate is off by a small fraction of one
	// segment (the ~0.15% drift the comment below refers to) -- never anywhere
	// close to a whole segment's width. A mismatch this large means the article
	// actually fetched under this messageID does not belong at this position at
	// all: confirmed live (2026-08-23) via a disk-cache entry that had been
	// silently serving another segment's decoded bytes (~90 segments off) under
	// the correct messageID key, with no transport-level error at any layer, for
	// hours -- realignSpans blindly trusted it and shifted every later span in
	// the file by the same huge delta, permanently corrupting the whole file's
	// layout from one bad segment. Treat this as a fetch failure instead of a
	// self-correction, and forget whatever got cached under this messageID so
	// the next attempt has a chance to fetch a genuinely correct copy instead of
	// replaying the same wrong bytes forever.
	if info.Valid() {
		threshold := segment.SegmentEnd - segment.SegmentStart
		if threshold < minRealignSanityBound {
			threshold = minRealignSanityBound
		}
		if delta := actualStart - segment.SegmentStart; delta > threshold || delta < -threshold {
			if inv, ok := f.source.(cacheInvalidator); ok {
				inv.InvalidateCached(segment.MessageID)
			}
			slog.Warn("nntp: fetched article's declared position is wildly inconsistent with expected position, discarding",
				"messageID", segment.MessageID, "estimatedStart", segment.SegmentStart, "estimatedEnd", segment.SegmentEnd,
				"actualStart", actualStart, "actualEnd", actualEnd, "delta", delta)
			return nil, stream.SegmentSpan{}, fmt.Errorf("article %s: declared decoded position %d is wildly inconsistent with expected %d (off by %d) -- likely wrong cached/fetched article data", segment.MessageID, actualStart, segment.SegmentStart, delta)
		}
	}
	start := int(segment.RangeStart - actualStart)
	end := int(segment.RangeEnd - actualStart)
	if end < start {
		return nil, stream.SegmentSpan{SegmentID: segment.SegmentID, MessageID: segment.MessageID, Start: actualStart, End: actualEnd}, errors.New("invalid segment range")
	}
	// start < 0 means the actual decoded content of this segment begins AFTER
	// our estimated RangeStart. The span table is stale — return empty so
	// realignSpans + retry in DirectNzbReader can find the correct segment.
	if start < 0 {
		return []byte{}, stream.SegmentSpan{SegmentID: segment.SegmentID, MessageID: segment.MessageID, Start: actualStart, End: actualEnd}, nil
	}
	// Clamp end to actual decoded size. Estimated offsets may be ~0.15% too large
	// (yEnc decode ratio varies per article). We return what exists rather than
	// failing so callers can gracefully handle the boundary.
	if end > len(decoded) {
		end = len(decoded)
	}
	if start >= end {
		return []byte{}, stream.SegmentSpan{SegmentID: segment.SegmentID, MessageID: segment.MessageID, Start: actualStart, End: actualEnd}, nil
	}
	out := make([]byte, end-start)
	copy(out, decoded[start:end])
	return out, stream.SegmentSpan{SegmentID: segment.SegmentID, MessageID: segment.MessageID, Start: actualStart, End: actualEnd}, nil
}

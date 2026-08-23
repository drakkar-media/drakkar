package nntp

import (
	"context"
	"encoding/binary"
	"errors"

	"github.com/drakkar-media/drakkar/internal/cache"
	"github.com/drakkar-media/drakkar/internal/stream"
	"github.com/drakkar-media/drakkar/internal/yenc"
)

// DecodedArticleSource defines the operations required to fetch a fully
// yEnc-decoded article body.
//
// Implementations must treat messageID as an opaque NNTP message identifier
// and return the decoded (not raw/encoded) article bytes.
type DecodedArticleSource interface {
	DecodedBody(ctx context.Context, messageID string) ([]byte, error)
}

// PriorityDecodedArticleSource extends DecodedArticleSource with a
// priority-aware fetch, letting callers signal whether a request is on the
// interactive playback path or a lower-priority background one (e.g.
// read-ahead or calibration).
type PriorityDecodedArticleSource interface {
	DecodedArticleSource
	DecodedBodyPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, error)
}

// DecodedArticleInfoSource extends DecodedArticleSource with access to the
// article's yEnc PartInfo header (total/begin/end offsets) alongside its
// decoded body, needed by callers that must map a segment onto its true
// decoded byte range.
type DecodedArticleInfoSource interface {
	DecodedArticleSource
	DecodedBodyInfo(ctx context.Context, messageID string) ([]byte, yenc.PartInfo, error)
}

// PriorityDecodedArticleInfoSource combines priority-aware fetching with
// PartInfo retrieval; every real production source implements this full
// interface.
type PriorityDecodedArticleInfoSource interface {
	DecodedArticleInfoSource
	DecodedBodyInfoPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, yenc.PartInfo, error)
}

// CachedDecodedSource wraps a DecodedArticleSource with an in-memory LRU
// cache of decoded article bodies (and a small companion cache of their
// PartInfo headers), plus singleflight coalescing so concurrent requests for
// the same messageID share one underlying fetch.
//
// CachedDecodedSource is safe for concurrent use.
type CachedDecodedSource struct {
	source       DecodedArticleSource
	cache        *cache.ByteLRU
	infoCache    *cache.ByteLRU // tiny: one encodedPartInfoLen entry per messageID
	singleflight *cache.SingleFlight
}

// infoCacheMaxBytes bounds the PartInfo companion cache. Entries are
// encodedPartInfoLen bytes each, so this comfortably covers the same key
// count as any realistic MemoryHotCacheMaxBytes without needing to share its
// budget — PartInfo (24 bytes) is negligible next to a decoded segment body.
const infoCacheMaxBytes = 4 << 20 // ~175k entries

// NewCachedDecodedSource creates a CachedDecodedSource wrapping source with a
// decoded-body LRU bounded at maxBytes and its own singleflight group.
func NewCachedDecodedSource(source DecodedArticleSource, maxBytes int64) *CachedDecodedSource {
	return &CachedDecodedSource{
		source:       source,
		cache:        cache.NewByteLRU(maxBytes),
		infoCache:    cache.NewByteLRU(infoCacheMaxBytes),
		singleflight: cache.NewSingleFlight(),
	}
}

const encodedPartInfoLen = 24 // 3 × int64: TotalSize, Begin, End

// encodePartInfo serializes info to a fixed-width byte slice so it can be
// stored in a ByteLRU (which only holds []byte values) or transported through
// a singleflight call keyed on []byte results.
func encodePartInfo(info yenc.PartInfo) []byte {
	buf := make([]byte, encodedPartInfoLen)
	binary.BigEndian.PutUint64(buf[0:8], uint64(info.TotalSize))
	binary.BigEndian.PutUint64(buf[8:16], uint64(info.Begin))
	binary.BigEndian.PutUint64(buf[16:24], uint64(info.End))
	return buf
}

// decodePartInfo is the inverse of encodePartInfo. It returns false if buf is
// not exactly encodedPartInfoLen bytes, which callers treat as a cache miss
// rather than a fatal error.
func decodePartInfo(buf []byte) (yenc.PartInfo, bool) {
	if len(buf) != encodedPartInfoLen {
		return yenc.PartInfo{}, false
	}
	return yenc.PartInfo{
		TotalSize: int64(binary.BigEndian.Uint64(buf[0:8])),
		Begin:     int64(binary.BigEndian.Uint64(buf[8:16])),
		End:       int64(binary.BigEndian.Uint64(buf[16:24])),
	}, true
}

// DecodedBody fetches messageID's decoded body at interactive priority; see
// DecodedBodyPriority.
func (s *CachedDecodedSource) DecodedBody(ctx context.Context, messageID string) ([]byte, error) {
	return s.DecodedBodyPriority(ctx, messageID, stream.PriorityInteractive)
}

// DecodedBodyPriority returns messageID's decoded body, serving it from the
// in-memory LRU when present. On a miss it fetches through singleflight so
// concurrent callers for the same messageID share one underlying decode/fetch
// and one cache population.
func (s *CachedDecodedSource) DecodedBodyPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, error) {
	if s == nil || s.source == nil {
		return nil, errors.New("decoded source unavailable")
	}
	if body, ok := s.cache.Get(messageID); ok {
		return body, nil
	}
	body, err := s.singleflight.Do(ctx, messageID, func(ctx context.Context) ([]byte, error) {
		var (
			decoded []byte
			err     error
		)
		if prioritySource, ok := s.source.(PriorityDecodedArticleSource); ok {
			decoded, err = prioritySource.DecodedBodyPriority(ctx, messageID, priority)
		} else {
			decoded, err = s.source.DecodedBody(ctx, messageID)
		}
		if err != nil {
			return nil, err
		}
		s.cache.Put(messageID, decoded)
		return decoded, nil
	})
	if err != nil {
		return nil, err
	}
	return body, nil
}

// DecodedBodyInfo fetches messageID's decoded body and PartInfo at
// interactive priority; see DecodedBodyInfoPriority.
func (s *CachedDecodedSource) DecodedBodyInfo(ctx context.Context, messageID string) ([]byte, yenc.PartInfo, error) {
	return s.DecodedBodyInfoPriority(ctx, messageID, stream.PriorityInteractive)
}

// DecodedBodyInfoPriority previously type-asserted s.source and, whenever
// the wrapped source implemented the info interface (which every real
// production source does — see DiskCachedDecodedSource), called straight
// through to it — never touching s.cache. That silently made the RAM hot
// cache configured via MemoryHotCacheMaxBytes dead code on the actual
// streaming read path: every segment read went to disk cache or NNTP even
// moments after being fetched. This now checks/populates s.cache (and its
// small PartInfo companion, infoCache) the same way the byte-only path does.
func (s *CachedDecodedSource) DecodedBodyInfoPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, yenc.PartInfo, error) {
	if s == nil || s.source == nil {
		return nil, yenc.PartInfo{}, errors.New("decoded source unavailable")
	}
	if body, ok := s.cache.Get(messageID); ok {
		if raw, ok := s.infoCache.Get(messageID); ok {
			if info, ok := decodePartInfo(raw); ok {
				return body, info, nil
			}
		}
		// Body cached but its (tiny, independently-evicted) info entry
		// wasn't — fall through and re-fetch both together below.
	}

	// singleflight.Do is fixed to []byte, so transport the PartInfo by
	// prefixing it onto the body — this also lets concurrent "follower"
	// callers (who never run this closure themselves) get the same info a
	// leader fetched, which a captured-variable side effect couldn't do.
	combined, err := s.singleflight.Do(ctx, "info:"+messageID, func(ctx context.Context) ([]byte, error) {
		var (
			decoded []byte
			info    yenc.PartInfo
			err     error
		)
		switch src := s.source.(type) {
		case PriorityDecodedArticleInfoSource:
			decoded, info, err = src.DecodedBodyInfoPriority(ctx, messageID, priority)
		case DecodedArticleInfoSource:
			decoded, info, err = src.DecodedBodyInfo(ctx, messageID)
		default:
			if prioritySource, ok := s.source.(PriorityDecodedArticleSource); ok {
				decoded, err = prioritySource.DecodedBodyPriority(ctx, messageID, priority)
			} else {
				decoded, err = s.source.DecodedBody(ctx, messageID)
			}
		}
		if err != nil {
			return nil, err
		}
		s.cache.Put(messageID, decoded)
		s.infoCache.Put(messageID, encodePartInfo(info))
		return append(encodePartInfo(info), decoded...), nil
	})
	if err != nil {
		return nil, yenc.PartInfo{}, err
	}
	info, _ := decodePartInfo(combined[:encodedPartInfoLen])
	return combined[encodedPartInfoLen:], info, nil
}

// InvalidateCached forgets messageID's cached decoded body and PartInfo in
// this in-memory layer, then delegates to the wrapped source so a
// further-wrapped layer (in production, DiskCachedDecodedSource's on-disk
// cache) forgets it too -- otherwise clearing only this RAM layer would just
// have the very next fetch repopulate it straight from the still-poisoned
// disk entry. See DiskCachedDecodedSource.InvalidateCached for why this
// exists at all.
func (s *CachedDecodedSource) InvalidateCached(messageID string) {
	if s == nil {
		return
	}
	s.cache.Remove(messageID)
	s.infoCache.Remove(messageID)
	if inv, ok := s.source.(interface{ InvalidateCached(string) }); ok {
		inv.InvalidateCached(messageID)
	}
}

// Stat reports whether messageID exists without requiring the caller to
// consume a decoded body: a cache hit is itself proof of existence, and
// otherwise this defers to the wrapped source's Stat when available, falling
// back to a full decode only as a last resort.
func (s *CachedDecodedSource) Stat(ctx context.Context, messageID string) error {
	if s == nil || s.source == nil {
		return errors.New("decoded source unavailable")
	}
	if _, ok := s.cache.Get(messageID); ok {
		return nil
	}
	if statSource, ok := s.source.(StatSource); ok {
		return statSource.Stat(ctx, messageID)
	}
	_, err := s.DecodedBodyPriority(ctx, messageID, stream.PriorityBackground)
	return err
}

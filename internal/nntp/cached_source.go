package nntp

import (
	"context"
	"encoding/binary"
	"errors"

	"github.com/hjongedijk/drakkar/internal/cache"
	"github.com/hjongedijk/drakkar/internal/stream"
	"github.com/hjongedijk/drakkar/internal/yenc"
)

type DecodedArticleSource interface {
	DecodedBody(ctx context.Context, messageID string) ([]byte, error)
}

type PriorityDecodedArticleSource interface {
	DecodedArticleSource
	DecodedBodyPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, error)
}

type DecodedArticleInfoSource interface {
	DecodedArticleSource
	DecodedBodyInfo(ctx context.Context, messageID string) ([]byte, yenc.PartInfo, error)
}

type PriorityDecodedArticleInfoSource interface {
	DecodedArticleInfoSource
	DecodedBodyInfoPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, yenc.PartInfo, error)
}

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

func NewCachedDecodedSource(source DecodedArticleSource, maxBytes int64) *CachedDecodedSource {
	return &CachedDecodedSource{
		source:       source,
		cache:        cache.NewByteLRU(maxBytes),
		infoCache:    cache.NewByteLRU(infoCacheMaxBytes),
		singleflight: cache.NewSingleFlight(),
	}
}

const encodedPartInfoLen = 24 // 3 × int64: TotalSize, Begin, End

func encodePartInfo(info yenc.PartInfo) []byte {
	buf := make([]byte, encodedPartInfoLen)
	binary.BigEndian.PutUint64(buf[0:8], uint64(info.TotalSize))
	binary.BigEndian.PutUint64(buf[8:16], uint64(info.Begin))
	binary.BigEndian.PutUint64(buf[16:24], uint64(info.End))
	return buf
}

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

func (s *CachedDecodedSource) DecodedBody(ctx context.Context, messageID string) ([]byte, error) {
	return s.DecodedBodyPriority(ctx, messageID, stream.PriorityInteractive)
}

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

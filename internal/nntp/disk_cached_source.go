package nntp

import (
	"context"
	"errors"
	"log/slog"

	"github.com/hjongedijk/drakkar/internal/cache"
	"github.com/hjongedijk/drakkar/internal/metrics"
	"github.com/hjongedijk/drakkar/internal/stream"
	"github.com/hjongedijk/drakkar/internal/yenc"
)

type DiskCachedDecodedSource struct {
	source       ArticleSource
	cache        *cache.FileCache
	singleflight *cache.SingleFlight
	// partInfo companions the on-disk decoded body cache, which has no size
	// cap of its own (bounded by maxBytes on disk, not entry count) — an
	// unbounded map here would otherwise grow forever across a long-running
	// process. Bounded the same way as CachedDecodedSource's infoCache.
	partInfo *cache.ByteLRU
}

func NewDiskCachedDecodedSource(source ArticleSource, root string, maxBytes int64) *DiskCachedDecodedSource {
	return &DiskCachedDecodedSource{
		source:       source,
		cache:        cache.NewFileCache(root, maxBytes),
		singleflight: cache.NewSingleFlight(),
		partInfo:     cache.NewByteLRU(infoCacheMaxBytes),
	}
}

func (s *DiskCachedDecodedSource) DecodedBody(ctx context.Context, messageID string) ([]byte, error) {
	return s.DecodedBodyPriority(ctx, messageID, stream.PriorityInteractive)
}

func (s *DiskCachedDecodedSource) DecodedBodyPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, error) {
	body, _, err := s.DecodedBodyInfoPriority(ctx, messageID, priority)
	return body, err
}

func (s *DiskCachedDecodedSource) DecodedBodyInfo(ctx context.Context, messageID string) ([]byte, yenc.PartInfo, error) {
	return s.DecodedBodyInfoPriority(ctx, messageID, stream.PriorityInteractive)
}

func (s *DiskCachedDecodedSource) DecodedBodyInfoPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, yenc.PartInfo, error) {
	if s == nil || s.source == nil {
		return nil, yenc.PartInfo{}, errors.New("disk cached source unavailable")
	}
	if body, ok, err := s.cache.Get(messageID); err == nil && ok {
		metrics.M.CacheHits.Add(1)
		if info, ok := s.lookupPartInfo(messageID); ok {
			return body, info, nil
		}
		return s.fillPartInfoFromRaw(ctx, messageID, priority, body)
	} else if err != nil {
		return nil, yenc.PartInfo{}, err
	}
	metrics.M.CacheMisses.Add(1)
	value, err := s.singleflight.Do(ctx, messageID, func(ctx context.Context) ([]byte, error) {
		if body, ok, err := s.cache.Get(messageID); err == nil && ok {
			metrics.M.CacheHits.Add(1)
			return body, nil
		} else if err != nil {
			return nil, err
		}
		var (
			raw []byte
			err error
		)
		if prioritySource, ok := s.source.(PriorityArticleSource); ok {
			raw, err = prioritySource.BodyPriority(ctx, messageID, priority)
		} else {
			raw, err = s.source.Body(ctx, messageID)
		}
		if err != nil {
			return nil, err
		}
		decoded, info, err := yenc.DecodeArticleWithInfo(raw)
		if err != nil {
			return nil, err
		}
		_ = s.cache.Put(messageID, decoded) // no-op when disk cache root is empty
		s.storePartInfo(messageID, info)
		return decoded, nil
	})
	if err != nil {
		return nil, yenc.PartInfo{}, err
	}
	if info, ok := s.lookupPartInfo(messageID); ok {
		return value, info, nil
	}
	return s.fillPartInfoFromRaw(ctx, messageID, priority, value)
}

func (s *DiskCachedDecodedSource) fillPartInfoFromRaw(ctx context.Context, messageID string, priority stream.FetchPriority, decoded []byte) ([]byte, yenc.PartInfo, error) {
	raw, err := s.fetchRaw(ctx, messageID, priority)
	if err != nil {
		// The decoded body already came from disk cache and is still
		// returned — this refetch is only to recover yEnc header info lost
		// across a process restart (partInfo is in-memory only). Log so a
		// persistently failing refetch (e.g. dead provider) is visible
		// instead of silently degrading every disk-cache hit to estimated
		// offsets with no signal.
		slog.Warn("nntp: could not refetch article for part-info enrichment; using estimated offsets", "messageID", messageID, "err", err)
		return decoded, yenc.PartInfo{}, nil
	}
	info, _ := yenc.ParsePartInfo(raw)
	s.storePartInfo(messageID, info)
	return decoded, info, nil
}

func (s *DiskCachedDecodedSource) fetchRaw(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, error) {
	if prioritySource, ok := s.source.(PriorityArticleSource); ok {
		return prioritySource.BodyPriority(ctx, messageID, priority)
	}
	return s.source.Body(ctx, messageID)
}

func (s *DiskCachedDecodedSource) lookupPartInfo(messageID string) (yenc.PartInfo, bool) {
	raw, ok := s.partInfo.Get(messageID)
	if !ok {
		return yenc.PartInfo{}, false
	}
	return decodePartInfo(raw)
}

func (s *DiskCachedDecodedSource) storePartInfo(messageID string, info yenc.PartInfo) {
	s.partInfo.Put(messageID, encodePartInfo(info))
}

func (s *DiskCachedDecodedSource) Stat(ctx context.Context, messageID string) error {
	if s == nil || s.source == nil {
		return errors.New("disk cached source unavailable")
	}
	if body, ok, err := s.cache.Get(messageID); err == nil && ok {
		metrics.M.CacheHits.Add(1)
		if _, hasInfo := s.lookupPartInfo(messageID); !hasInfo {
			_, _, _ = s.fillPartInfoFromRaw(ctx, messageID, stream.PriorityBackground, body)
		}
		return nil
	} else if err != nil {
		return err
	}
	if statSource, ok := s.source.(StatSource); ok {
		return statSource.Stat(ctx, messageID)
	}
	_, _, err := s.DecodedBodyInfoPriority(ctx, messageID, stream.PriorityBackground)
	return err
}

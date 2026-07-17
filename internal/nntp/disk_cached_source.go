package nntp

import (
	"context"
	"errors"
	"log/slog"
	"runtime"

	"github.com/drakkar-media/drakkar/internal/cache"
	"github.com/drakkar-media/drakkar/internal/metrics"
	"github.com/drakkar-media/drakkar/internal/stream"
	"github.com/drakkar-media/drakkar/internal/yenc"
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
	// decodeSem bounds how many yEnc decodes run at once, independent of how
	// many segment fetches are in flight (which can be much higher -- e.g.
	// 30 for a single high-bitrate read-ahead stream). The rapidyenc/CGO
	// decoder isn't preemptible mid-call the way pure-Go code is: a goroutine
	// in a CGO call ties up its OS thread for the duration, so many
	// concurrent decodes can starve the Go scheduler of threads for
	// unrelated work (including the HTTP health-check handler) even though
	// each individual decode is fast. Confirmed in production: streaming a
	// ~47 Mbps 4K remux at 30x fetch parallelism drove sustained 400-600%
	// CPU and made the app fail its own health check under load. Decoding
	// itself is CPU-bound and fast, so bounding it to NumCPU lets fetches
	// stay highly concurrent (I/O-bound, cheap to leave in flight) while
	// capping how many CPU-bound CGO calls compete for OS threads at once.
	decodeSem chan struct{}
}

func NewDiskCachedDecodedSource(source ArticleSource, root string, maxBytes int64) *DiskCachedDecodedSource {
	return &DiskCachedDecodedSource{
		source:       source,
		cache:        cache.NewFileCache(root, maxBytes),
		singleflight: cache.NewSingleFlight(),
		partInfo:     cache.NewByteLRU(infoCacheMaxBytes),
		decodeSem:    make(chan struct{}, runtime.NumCPU()),
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
		decoded, info, err := s.decodeArticle(ctx, raw)
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

// decodeArticle runs the yEnc decode under decodeSem -- see the field comment
// on DiskCachedDecodedSource for why this needs its own, smaller concurrency
// bound separate from fetch parallelism.
func (s *DiskCachedDecodedSource) decodeArticle(ctx context.Context, raw []byte) ([]byte, yenc.PartInfo, error) {
	select {
	case s.decodeSem <- struct{}{}:
	case <-ctx.Done():
		return nil, yenc.PartInfo{}, ctx.Err()
	}
	defer func() { <-s.decodeSem }()
	return yenc.DecodeArticleWithInfo(raw)
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
	if _, ok, err := s.cache.Get(messageID); err == nil && ok {
		// The decoded body already being on disk is itself proof the
		// article exists -- that's all a Stat caller needs. Don't call
		// fillPartInfoFromRaw here: its only purpose is recovering the
		// yEnc PartInfo lost across a restart (partInfo is in-memory
		// only), which no Stat caller ever observes, and doing so degrades
		// this supposedly-cheap existence check into a full live article
		// body fetch. earlyChecker (used as a preflight gate before every
		// selected-release fetch/retry) calls exactly this path, so that
		// degradation defeated the entire point of using a cheap check
		// there. A missing partInfo entry is instead filled in lazily by
		// DecodedBodyInfoPriority the next time this article is actually
		// read for playback, which already needs to hold the body in hand.
		metrics.M.CacheHits.Add(1)
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

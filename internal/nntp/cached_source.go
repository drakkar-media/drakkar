package nntp

import (
	"context"
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
	singleflight *cache.SingleFlight
}

func NewCachedDecodedSource(source DecodedArticleSource, maxBytes int64) *CachedDecodedSource {
	return &CachedDecodedSource{
		source:       source,
		cache:        cache.NewByteLRU(maxBytes),
		singleflight: cache.NewSingleFlight(),
	}
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

func (s *CachedDecodedSource) DecodedBodyInfoPriority(ctx context.Context, messageID string, priority stream.FetchPriority) ([]byte, yenc.PartInfo, error) {
	if infoSource, ok := s.source.(PriorityDecodedArticleInfoSource); ok {
		return infoSource.DecodedBodyInfoPriority(ctx, messageID, priority)
	}
	if infoSource, ok := s.source.(DecodedArticleInfoSource); ok {
		return infoSource.DecodedBodyInfo(ctx, messageID)
	}
	body, err := s.DecodedBodyPriority(ctx, messageID, priority)
	if err != nil {
		return nil, yenc.PartInfo{}, err
	}
	return body, yenc.PartInfo{}, nil
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

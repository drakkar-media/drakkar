package cache

import "context"

// Service exposes cache maintenance operations to the API layer.
type Service struct {
	blockCache *FileCache
}

// NewService creates a Service wrapping the given block cache. blockCache
// may be nil when disk block caching is disabled, in which case Prune is a
// no-op.
func NewService(blockCache *FileCache) *Service {
	return &Service{blockCache: blockCache}
}

// Prune forces an eviction pass on the underlying block cache and reports
// the result. Safe to call on a nil Service or with a nil block cache
// (e.g. when disk caching is disabled), returning a zero-value result.
func (s *Service) Prune(ctx context.Context) (PruneResult, error) {
	_ = ctx
	if s == nil || s.blockCache == nil {
		return PruneResult{}, nil
	}
	return s.blockCache.Prune()
}

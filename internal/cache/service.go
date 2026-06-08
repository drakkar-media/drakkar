package cache

import "context"

type Service struct {
	blockCache *FileCache
}

func NewService(blockCache *FileCache) *Service {
	return &Service{blockCache: blockCache}
}

func (s *Service) Prune(ctx context.Context) (PruneResult, error) {
	_ = ctx
	if s == nil || s.blockCache == nil {
		return PruneResult{}, nil
	}
	return s.blockCache.Prune()
}

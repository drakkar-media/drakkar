package blocklist

import (
	"context"

	"github.com/hjongedijk/drakkar/internal/database"
)

type Repository interface {
	ListBlocklistItems(ctx context.Context) ([]database.BlocklistItemSummary, error)
	DeleteBlocklistItem(ctx context.Context, id int64) error
	DeleteAllBlocklistItems(ctx context.Context) (int, error)
	DeleteBlocklistItemsByReason(ctx context.Context, reason string) (int, error)
}

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) List(ctx context.Context) ([]database.BlocklistItemSummary, error) {
	return s.repo.ListBlocklistItems(ctx)
}

func (s *Service) Clear(ctx context.Context, id int64) error {
	return s.repo.DeleteBlocklistItem(ctx, id)
}

func (s *Service) ClearAll(ctx context.Context) (database.BlocklistClearResult, error) {
	cleared, err := s.repo.DeleteAllBlocklistItems(ctx)
	if err != nil {
		return database.BlocklistClearResult{}, err
	}
	return database.BlocklistClearResult{Cleared: cleared}, nil
}

func (s *Service) ClearByReason(ctx context.Context, reason string) (database.BlocklistClearResult, error) {
	cleared, err := s.repo.DeleteBlocklistItemsByReason(ctx, reason)
	if err != nil {
		return database.BlocklistClearResult{}, err
	}
	return database.BlocklistClearResult{Cleared: cleared}, nil
}

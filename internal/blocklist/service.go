package blocklist

import (
	"context"

	"github.com/drakkar-media/drakkar/internal/database"
)

// Repository defines the persistence operations required to manage the
// release blocklist (releases excluded from future selection, e.g. after
// exhausting their retry budget).
type Repository interface {
	ListBlocklistItems(ctx context.Context) ([]database.BlocklistItemSummary, error)
	ListBlocklistItemsPaged(ctx context.Context, f database.BlocklistFilter) (database.BlocklistPage, error)
	BlocklistStats(ctx context.Context) (database.BlocklistStats, error)
	CreateBlocklistItem(ctx context.Context, item database.BlocklistMutation) (database.BlocklistItemSummary, error)
	UpdateBlocklistItem(ctx context.Context, id int64, item database.BlocklistMutation) (database.BlocklistItemSummary, error)
	DeleteBlocklistItem(ctx context.Context, id int64) error
	DeleteAllBlocklistItems(ctx context.Context) (int, error)
	DeleteBlocklistItemsByReason(ctx context.Context, reason string) (int, error)
}

// Service exposes blocklist management operations to the API layer, backed
// by a Repository.
type Service struct {
	repo Repository
}

// NewService creates a Service backed by the given Repository.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// List returns all blocklist items.
func (s *Service) List(ctx context.Context) ([]database.BlocklistItemSummary, error) {
	return s.repo.ListBlocklistItems(ctx)
}

// ListPaged returns a filtered, paginated view of the blocklist.
func (s *Service) ListPaged(ctx context.Context, f database.BlocklistFilter) (database.BlocklistPage, error) {
	return s.repo.ListBlocklistItemsPaged(ctx, f)
}

// Stats returns aggregate counts describing the current blocklist.
func (s *Service) Stats(ctx context.Context) (database.BlocklistStats, error) {
	return s.repo.BlocklistStats(ctx)
}

// Create adds a new blocklist item.
func (s *Service) Create(ctx context.Context, item database.BlocklistMutation) (database.BlocklistItemSummary, error) {
	return s.repo.CreateBlocklistItem(ctx, item)
}

// Update modifies an existing blocklist item identified by id.
func (s *Service) Update(ctx context.Context, id int64, item database.BlocklistMutation) (database.BlocklistItemSummary, error) {
	return s.repo.UpdateBlocklistItem(ctx, id, item)
}

// Clear removes a single blocklist item, making the underlying release
// eligible for selection again.
func (s *Service) Clear(ctx context.Context, id int64) error {
	return s.repo.DeleteBlocklistItem(ctx, id)
}

// ClearAll removes every blocklist item and reports how many were deleted.
func (s *Service) ClearAll(ctx context.Context) (database.BlocklistClearResult, error) {
	cleared, err := s.repo.DeleteAllBlocklistItems(ctx)
	if err != nil {
		return database.BlocklistClearResult{}, err
	}
	return database.BlocklistClearResult{Cleared: cleared}, nil
}

// ClearByReason removes all blocklist items matching the given reason and
// reports how many were deleted.
func (s *Service) ClearByReason(ctx context.Context, reason string) (database.BlocklistClearResult, error) {
	cleared, err := s.repo.DeleteBlocklistItemsByReason(ctx, reason)
	if err != nil {
		return database.BlocklistClearResult{}, err
	}
	return database.BlocklistClearResult{Cleared: cleared}, nil
}

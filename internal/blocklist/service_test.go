package blocklist

import (
	"context"
	"testing"

	"github.com/hjongedijk/drakkar/internal/database"
)

type repoStub struct {
	items   []database.BlocklistItemSummary
	deleted int64
	all     int
}

func (r *repoStub) ListBlocklistItems(ctx context.Context) ([]database.BlocklistItemSummary, error) {
	return r.items, nil
}

func (r *repoStub) DeleteBlocklistItem(ctx context.Context, id int64) error {
	r.deleted = id
	return nil
}

func (r *repoStub) DeleteAllBlocklistItems(ctx context.Context) (int, error) {
	return r.all, nil
}

func (r *repoStub) ListBlocklistItemsPaged(ctx context.Context, f database.BlocklistFilter) (database.BlocklistPage, error) {
	return database.BlocklistPage{Items: r.items}, nil
}

func (r *repoStub) BlocklistStats(ctx context.Context) (database.BlocklistStats, error) {
	return database.BlocklistStats{ByReason: map[string]int{}}, nil
}

func (r *repoStub) CreateBlocklistItem(ctx context.Context, item database.BlocklistMutation) (database.BlocklistItemSummary, error) {
	return database.BlocklistItemSummary{ID: 2, Key: item.Key, Reason: item.Reason}, nil
}

func (r *repoStub) UpdateBlocklistItem(ctx context.Context, id int64, item database.BlocklistMutation) (database.BlocklistItemSummary, error) {
	return database.BlocklistItemSummary{ID: id, Key: item.Key, Reason: item.Reason}, nil
}

func (r *repoStub) DeleteBlocklistItemsByReason(ctx context.Context, reason string) (int, error) {
	return 0, nil
}

func TestList(t *testing.T) {
	service := NewService(&repoStub{items: []database.BlocklistItemSummary{{ID: 1, Key: "external_url:http://example", Reason: "manual_reject"}}})
	items, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Reason != "manual_reject" {
		t.Fatalf("unexpected items %+v", items)
	}
}

func TestClear(t *testing.T) {
	repo := &repoStub{}
	service := NewService(repo)
	if err := service.Clear(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if repo.deleted != 7 {
		t.Fatalf("unexpected deleted id %d", repo.deleted)
	}
}

func TestClearAll(t *testing.T) {
	service := NewService(&repoStub{all: 3})
	result, err := service.ClearAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Cleared != 3 {
		t.Fatalf("unexpected result %+v", result)
	}
}

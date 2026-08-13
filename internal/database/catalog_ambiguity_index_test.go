package database

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestFilterAmbiguousAlternateTitlesCachesAndInvalidates(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)
	suffix := time.Now().UnixNano()
	alias := fmt.Sprintf("CodexAlias%d", suffix)
	targetTitle := fmt.Sprintf("CodexTarget%d", suffix)
	otherTitle := fmt.Sprintf("CodexOther%d", suffix)

	var targetMovieID, otherMovieID, otherLibraryItemID int64
	if err := sqlDB.QueryRowContext(ctx, `
		insert into movies (title, alternative_titles)
		values ($1, array[$2]::text[])
		returning id`, targetTitle, alias,
	).Scan(&targetMovieID); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into movies (title)
		values ($1)
		returning id`, otherTitle,
	).Scan(&otherMovieID); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		insert into library_items (media_type, movie_id, title)
		values ('movie', $1, $2)
		returning id`, otherMovieID, otherTitle,
	).Scan(&otherLibraryItemID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = sqlDB.ExecContext(context.Background(), `delete from movies where id in ($1, $2)`, targetMovieID, otherMovieID)
	})

	titles := []string{alias, "Keep This Alias"}
	filtered, err := db.filterAmbiguousAlternateTitles(ctx, titles, 0, targetMovieID)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Fatalf("target's own alias was treated as ambiguous: %v", filtered)
	}
	firstIndex := db.catalogAmbiguity.Load()
	if firstIndex == nil {
		t.Fatal("expected catalog ambiguity index to be cached")
	}
	if _, err := db.filterAmbiguousAlternateTitles(ctx, titles, 0, targetMovieID); err != nil {
		t.Fatal(err)
	}
	if db.catalogAmbiguity.Load() != firstIndex {
		t.Fatal("unchanged catalog rebuilt its ambiguity index")
	}

	if err := db.EnrichMovieFull(ctx, otherLibraryItemID, MovieEnrichment{Title: alias + " Returns"}); err != nil {
		t.Fatal(err)
	}
	if db.catalogAmbiguity.Load() != nil {
		t.Fatal("movie title update did not invalidate catalog ambiguity index")
	}
	filtered, err = db.filterAmbiguousAlternateTitles(ctx, titles, 0, targetMovieID)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0] != "Keep This Alias" {
		t.Fatalf("rebuilt index did not remove newly ambiguous alias: %v", filtered)
	}
}

var benchmarkFilteredAlternateTitles []string

func BenchmarkFilterAmbiguousAlternateTitlesCached(b *testing.B) {
	index := &catalogAmbiguityIndex{
		firstWordEntityCounts: make(map[string]int, 10_000),
		entityFirstWords:      make(map[catalogEntity][]string),
	}
	for i := range 10_000 {
		index.firstWordEntityCounts[fmt.Sprintf("alias%d", i)] = 1
	}
	excluded := catalogEntity{kind: catalogEntityMovie, id: 42}
	index.entityFirstWords[excluded] = []string{"alias0"}
	db := &DB{}
	db.catalogAmbiguity.Store(index)
	titles := []string{
		"Alias0", "Alias1", "Alias2", "Alias3", "Alias4", "Alias5",
		"A Useful Multi Word Alias", "Another Multi Word Alias",
	}
	benchmarkFilteredAlternateTitles, _ = db.filterAmbiguousAlternateTitles(context.Background(), titles, 0, excluded.id)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		filtered, err := db.filterAmbiguousAlternateTitles(context.Background(), titles, 0, excluded.id)
		if err != nil {
			b.Fatal(err)
		}
		if len(filtered) != 3 {
			b.Fatalf("filtered %d titles, want 3", len(filtered))
		}
		benchmarkFilteredAlternateTitles = filtered
	}
}

package workflow

import (
	"fmt"
	"testing"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/hydra"
	"github.com/drakkar-media/drakkar/internal/ranking"
)

var benchmarkSearchCandidates []database.SearchCandidateRecord

func BenchmarkBuildCandidateHeavySearchResults(b *testing.B) {
	results := make([]hydra.SearchResult, 2_000)
	for i := range results {
		group := i % 500
		results[i] = hydra.SearchResult{
			Title:        fmt.Sprintf("Benchmark.Show.S01E%02d.1080p.WEB-DL-GROUP-%03d", group%20+1, group),
			Link:         fmt.Sprintf("https://indexer.example/get/%d", i),
			Indexer:      fmt.Sprintf("Indexer-%d", i%8),
			SizeBytes:    int64(2_000_000_000 + group*1_000_000),
			IndexerScore: i % 100,
			Grabs:        i,
		}
	}
	benchmarkSearchCandidates = buildSearchCandidates(
		results,
		ranking.Requirements{Title: "Benchmark Show", MediaType: "movie"},
		nil,
		ranking.Preferences{},
		IndexerLimits{},
		nil,
	)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkSearchCandidates = buildSearchCandidates(
			results,
			ranking.Requirements{Title: "Benchmark Show", MediaType: "movie"},
			nil,
			ranking.Preferences{},
			IndexerLimits{},
			nil,
		)
	}
}

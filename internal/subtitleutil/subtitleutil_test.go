package subtitleutil

import (
	"testing"

	"github.com/drakkar-media/drakkar/internal/database"
)

func TestSearchTitle(t *testing.T) {
	episode := database.SubtitleSearchInput{MediaType: "episode", Title: "S01E01", ShowTitle: "The Bear"}
	if got := SearchTitle(episode); got != "The Bear" {
		t.Fatalf("expected show title for episode, got %q", got)
	}
	movie := database.SubtitleSearchInput{MediaType: "movie", Title: "Dune"}
	if got := SearchTitle(movie); got != "Dune" {
		t.Fatalf("expected movie title, got %q", got)
	}
	episodeNoShowTitle := database.SubtitleSearchInput{MediaType: "tv", Title: "Fallback Title"}
	if got := SearchTitle(episodeNoShowTitle); got != "Fallback Title" {
		t.Fatalf("expected fallback to Title when ShowTitle is blank, got %q", got)
	}
}

func TestSearchYear(t *testing.T) {
	movie := database.SubtitleSearchInput{MediaType: "movie", MovieYear: 2021, ShowYear: 1999}
	if got := SearchYear(movie); got != 2021 {
		t.Fatalf("expected movie year, got %d", got)
	}
	show := database.SubtitleSearchInput{MediaType: "tv", MovieYear: 2021, ShowYear: 1999}
	if got := SearchYear(show); got != 1999 {
		t.Fatalf("expected show year, got %d", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := FirstNonEmpty("", "  ", "second", "third"); got != "second" {
		t.Fatalf("expected first non-blank value, got %q", got)
	}
	if got := FirstNonEmpty("", ""); got != "" {
		t.Fatalf("expected empty string when all blank, got %q", got)
	}
}

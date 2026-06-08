package symlink

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoviePath(t *testing.T) {
	// Filename is generated from title+year; raw NZB name only contributes the extension.
	got := MoviePath("/mnt/drakkar/media/movies", "Dune", 2021, 438631, "SomeRelease.1080p.mkv")
	want := "/mnt/drakkar/media/movies/Dune (2021) {tmdb-438631}/Dune (2021).mkv"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestMoviePathNoTMDB(t *testing.T) {
	got := MoviePath("/mnt/drakkar/media/movies", "Dune", 2021, 0, "release.mkv")
	want := "/mnt/drakkar/media/movies/Dune (2021)/Dune (2021).mkv"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestMoviePathNoYear(t *testing.T) {
	got := MoviePath("/mnt/drakkar/media/movies", "Dune", 0, 0, "release.mp4")
	want := "/mnt/drakkar/media/movies/Dune/Dune.mp4"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestMoviePathUnknownExt(t *testing.T) {
	// Unrecognised extension falls back to .mkv.
	got := MoviePath("/mnt/drakkar/media/movies", "Dune", 2021, 438631, "release.bin")
	want := "/mnt/drakkar/media/movies/Dune (2021) {tmdb-438631}/Dune (2021).mkv"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestEpisodePath(t *testing.T) {
	// Episode filename format: "Show - S01E02.mkv"
	got := EpisodePath("/mnt/drakkar/media/tv", "Loki", 2021, 362472, 2, 1, "Loki.S02E01.WEBDL.mkv")
	want := "/mnt/drakkar/media/tv/Loki (2021) {tvdb-362472}/Season 02/Loki - S02E01.mkv"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestEpisodePathNoTVDB(t *testing.T) {
	got := EpisodePath("/mnt/drakkar/media/tv", "Dutton Ranch", 2026, 0, 1, 1, "DuttonRanch.S01E01.mkv")
	want := "/mnt/drakkar/media/tv/Dutton Ranch (2026)/Season 01/Dutton Ranch - S01E01.mkv"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestEpisodePathHighNumbers(t *testing.T) {
	got := EpisodePath("/mnt/drakkar/media/tv", "Wednesday", 2022, 123456, 2, 8, "Wednesday.S02E08.4K.mkv")
	want := "/mnt/drakkar/media/tv/Wednesday (2022) {tvdb-123456}/Season 02/Wednesday - S02E08.mkv"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestPublishIdempotent(t *testing.T) {
	root := t.TempDir()
	finalPath := filepath.Join(root, "movie", "Dune.mkv")
	target := "/mnt/drakkar/vfs/content/releases/release-id/Dune.mkv"
	if err := NewPublisher().Publish(finalPath, target); err != nil {
		t.Fatal(err)
	}
	if err := NewPublisher().Publish(finalPath, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("got %s want %s", got, target)
	}
}

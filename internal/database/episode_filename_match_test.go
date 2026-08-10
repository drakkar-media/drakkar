package database

import "testing"

// TestEpisodeNumberMatchesFilenameHandlesDoubleEpisode covers the bug behind
// permanently-stuck "Consistency Issues" on the health page: a combined
// double-episode release file like "NCIS.New.Orleans.S03E17E18....mkv" only
// yields episode=17 from a strict ParseEpisodeFromFilename comparison, so a
// library item for S03E18 could never be matched to it -- publish and
// republish (RepublishLibraryItem -> republishEpisodeFromSourceRelease) both
// used this exact comparison, so the symlink for episode 18 was silently
// skipped on every publish/republish attempt, confirmed live for "NCIS: New
// Orleans" S03E18 (file S03E17E18) and "Gotham" S03E22 (file S03E21E22).
func TestEpisodeNumberMatchesFilenameHandlesDoubleEpisode(t *testing.T) {
	name := "NCIS.New.Orleans.S03E17E18.German.AC3D.DL.1080p.Web.x265-FuN.mkv"
	if !EpisodeNumberMatchesFilename(3, 17, name) {
		t.Error("expected episode 17 (first of the pair) to match")
	}
	if !EpisodeNumberMatchesFilename(3, 18, name) {
		t.Error("expected episode 18 (second of the pair) to match")
	}
	if EpisodeNumberMatchesFilename(3, 19, name) {
		t.Error("expected episode 19 (outside the pair) not to match")
	}
	if EpisodeNumberMatchesFilename(4, 17, name) {
		t.Error("expected a different season not to match")
	}
}

func TestEpisodeNumberMatchesFilenameSingleEpisode(t *testing.T) {
	name := "Gotham.S03E01.Mad.City.Better.to.Reign.in.Hell.1080p.BluRay.x264-OFT.mkv"
	if !EpisodeNumberMatchesFilename(3, 1, name) {
		t.Error("expected exact single-episode match")
	}
	if EpisodeNumberMatchesFilename(3, 2, name) {
		t.Error("expected a different episode not to match")
	}
}

func TestParseEpisodeRangeFromFilename(t *testing.T) {
	season, start, end := ParseEpisodeRangeFromFilename("Gotham.S03E21E22.Heroes.Rise.mkv")
	if season != 3 || start != 21 || end != 22 {
		t.Fatalf("expected (3, 21, 22), got (%d, %d, %d)", season, start, end)
	}

	season, start, end = ParseEpisodeRangeFromFilename("Gotham.S03E01.mkv")
	if season != 3 || start != 1 || end != 1 {
		t.Fatalf("expected (3, 1, 1), got (%d, %d, %d)", season, start, end)
	}
}

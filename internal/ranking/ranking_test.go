package ranking

import (
	"testing"
	"time"
)

func TestScoreRejectsWrongTitle(t *testing.T) {
	result := Score(Candidate{Title: "Other.Movie.2021.1080p"}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021})
	if !result.Rejected || result.RejectReason != "wrong_title" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestScorePreferredCandidate(t *testing.T) {
	result := Score(Candidate{
		Title:        "Dune.2021.1080p.WEB-DL",
		Resolution:   "1080p",
		Source:       "WEB-DL",
		Codec:        "x265",
		Language:     "en",
		Indexer:      "hydra",
		ReleaseGroup: "GROUP",
		UploadedAt:   time.Now(),
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021})
	if result.Rejected || result.Score <= 0 {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestScoreRejectsWrongMovieYear(t *testing.T) {
	result := Score(Candidate{
		Title:      "Dune.1984.1080p.BluRay",
		Resolution: "1080p",
		Source:     "bluray",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021})
	if !result.Rejected || result.RejectReason != "wrong_year" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestScoreRejectsBadSource(t *testing.T) {
	result := Score(Candidate{
		Title:      "Dune.2021.1080p.CAM",
		Resolution: "1080p",
		Source:     "cam",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021})
	if !result.Rejected || result.RejectReason != "bad_source" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestScorePrefersExactEpisodeOverSeasonPack(t *testing.T) {
	exact := Score(Candidate{
		Title:      "Loki.S01E02.1080p.WEB-DL",
		Resolution: "1080p",
		Source:     "web-dl",
		Language:   "en",
		UploadedAt: time.Now(),
	}, Requirements{Title: "Loki", MediaType: "episode", Year: 2021, SeasonNumber: 1, EpisodeNumber: 2})
	pack := Score(Candidate{
		Title:      "Loki.Season.1.Complete.1080p.WEB-DL",
		Resolution: "1080p",
		Source:     "web-dl",
		Language:   "en",
		UploadedAt: time.Now(),
	}, Requirements{Title: "Loki", MediaType: "episode", Year: 2021, SeasonNumber: 1, EpisodeNumber: 2})
	if exact.Rejected || pack.Rejected {
		t.Fatalf("unexpected reject exact=%+v pack=%+v", exact, pack)
	}
	if exact.Score <= pack.Score {
		t.Fatalf("expected exact episode score > season pack score, got exact=%d pack=%d", exact.Score, pack.Score)
	}
}

func TestScoreRejectsWrongEpisode(t *testing.T) {
	result := Score(Candidate{
		Title:      "Loki.S01E03.1080p.WEB-DL",
		Resolution: "1080p",
		Source:     "web-dl",
	}, Requirements{Title: "Loki", MediaType: "episode", Year: 2021, SeasonNumber: 1, EpisodeNumber: 2})
	if !result.Rejected || result.RejectReason != "wrong_episode" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestScoreWithPreferencesUsesOrderedResolution(t *testing.T) {
	prefs := Preferences{Resolutions: []string{"720p", "1080p"}}
	low := ScoreWithPreferences(Candidate{
		Title:      "Dune.2021.1080p.WEB-DL",
		Resolution: "1080p",
		Source:     "web-dl",
		Language:   "en",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021}, prefs)
	high := ScoreWithPreferences(Candidate{
		Title:      "Dune.2021.720p.WEB-DL",
		Resolution: "720p",
		Source:     "web-dl",
		Language:   "en",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021}, prefs)
	if high.Score <= low.Score {
		t.Fatalf("expected preferred resolution to win, got 720p=%d 1080p=%d", high.Score, low.Score)
	}
}

func TestScoreWithPreferencesRejectsTooLarge(t *testing.T) {
	result := ScoreWithPreferences(Candidate{
		Title:      "Dune.2021.1080p.WEB-DL",
		SizeBytes:  8 * 1024 * 1024 * 1024,
		Resolution: "1080p",
		Source:     "web-dl",
		Language:   "en",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021}, Preferences{MaxSizeMB: 4000})
	if !result.Rejected || result.RejectReason != "too_large" {
		t.Fatalf("unexpected result %+v", result)
	}
}

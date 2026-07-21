package ranking

import (
	"strings"
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

// TestScoreHeavilyPenalizesSingleFailureCount guards the 2026-07-17 fix:
// maxCandidateFailuresBeforeExclude was lowered from 5 to 1, so a single
// prior failure must now be "effectively excluded" (-50000), not just
// mildly penalized. This must stay in sync with
// workflow.maxCandidateFailuresBeforeGiveUp, which stops a candidate from
// getting a second real fetch attempt at the same threshold.
func TestScoreHeavilyPenalizesSingleFailureCount(t *testing.T) {
	clean := Score(Candidate{
		Title: "Dune.2021.1080p.WEB-DL", Resolution: "1080p", Source: "WEB-DL",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021})
	failedOnce := Score(Candidate{
		Title: "Dune.2021.1080p.WEB-DL", Resolution: "1080p", Source: "WEB-DL", FailureCount: 1,
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021})
	if clean.Score-failedOnce.Score < 40000 {
		t.Fatalf("expected a single failure to apply the heavy exclusion penalty, clean=%d failedOnce=%d", clean.Score, failedOnce.Score)
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

// TestScoreRejectsWrongYearForWholeShowSearch guards a real production
// incident (2026-07-21): a whole-show/season-pack search (MediaType "tv",
// set by searchRequirements for isWholeShowRequest) had no case at all in
// Score's MediaType switch, so year was never cross-checked -- a bare
// title-only search for "ONE PIECE" (the 2023 live-action show,
// first_air_date year 2023) matched and downloaded "One Piece (1999)
// S19E86", the unrelated classic anime, with zero rejection.
func TestScoreRejectsWrongYearForWholeShowSearch(t *testing.T) {
	result := Score(Candidate{
		Title:      "One.Piece.1999.S19E86.1080p.WEB-DL",
		Resolution: "1080p",
		Source:     "web-dl",
	}, Requirements{Title: "One Piece", MediaType: "tv", Year: 2023})
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

func TestScoreRejectsForeignLanguageOutsideProfilePreferences(t *testing.T) {
	result := ScoreWithPreferences(Candidate{
		Title:      "Yellowstone.S01E01.Spanish.1080p.WEB-DL",
		Resolution: "1080p",
		Source:     "web-dl",
		Language:   "foreign",
	}, Requirements{Title: "Yellowstone", MediaType: "episode", Year: 2018, SeasonNumber: 1, EpisodeNumber: 1}, Preferences{
		Languages: []string{"nl", "en"},
	})
	if !result.Rejected || result.RejectReason != "wrong_language" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestScoreAllowsUnknownLanguageWithProfilePreferences(t *testing.T) {
	result := ScoreWithPreferences(Candidate{
		Title:      "Yellowstone.S01E01.1080p.WEB-DL",
		Resolution: "1080p",
		Source:     "web-dl",
		Language:   "unknown",
	}, Requirements{Title: "Yellowstone", MediaType: "episode", Year: 2018, SeasonNumber: 1, EpisodeNumber: 1}, Preferences{
		Languages: []string{"nl", "en"},
	})
	if result.Rejected {
		t.Fatalf("unexpected rejection %+v", result)
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

// TestScoreWithPreferencesRejectsUnlistedResolution guards the fix for a
// real production incident: a profile's Resolutions field is presented to
// users as an allow-list (e.g. "HD - 720p/1080p"), but was only a scoring
// nudge (-120 for an unlisted resolution) rather than a hard reject. A show
// with that profile ended up with a 2160p HDR/Atmos remux selected over a
// perfectly good 1080p release for the same episode, because the remux's
// bonuses on other dimensions outweighed the resolution penalty -- the
// remux's bitrate then made read-ahead unable to keep up, causing repeated
// playback stalls. An unlisted resolution must now be rejected outright.
func TestScoreWithPreferencesRejectsUnlistedResolution(t *testing.T) {
	prefs := Preferences{Resolutions: []string{"720p", "1080p"}}
	result := ScoreWithPreferences(Candidate{
		Title:      "A.Knight.of.the.Seven.Kingdoms.S01E06.2160p.UHD.BluRay.REMUX",
		Resolution: "2160p",
		Source:     "bluray",
		Language:   "en",
	}, Requirements{Title: "A Knight of the Seven Kingdoms", MediaType: "episode", SeasonNumber: 1, EpisodeNumber: 6}, prefs)
	if !result.Rejected || result.RejectReason != "resolution_not_allowed" {
		t.Fatalf("expected resolution_not_allowed rejection, got %+v", result)
	}
}

// TestScoreWithPreferencesAllowsAnyResolutionWhenUnconfigured ensures a
// profile that never set Resolutions (or a search run with no profile at
// all) keeps accepting any resolution -- the hard filter above only applies
// once a profile actually lists specific allowed resolutions.
func TestScoreWithPreferencesAllowsAnyResolutionWhenUnconfigured(t *testing.T) {
	result := ScoreWithPreferences(Candidate{
		Title:      "Dune.2021.2160p.UHD.BluRay.REMUX",
		Resolution: "2160p",
		Source:     "bluray",
		Language:   "en",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021}, Preferences{})
	if result.Rejected {
		t.Fatalf("expected no resolution rejection with an empty preference list, got %+v", result)
	}
}

// TestScoreWithPreferencesRejectsUndetectedResolution guards a real production
// incident: a profile locked to 720p/1080p only still selected a DVD5 rip
// whose title had no resolution token, because the allow-list guard skipped
// candidates with an empty Resolution field entirely instead of treating an
// undetected resolution as not-allowed. Every real HD candidate for that movie
// failed for unrelated reasons, and the undetected-resolution rip -- never
// actually disqualified -- ended up selected and symlinked in its place.
func TestScoreWithPreferencesRejectsUndetectedResolution(t *testing.T) {
	prefs := Preferences{Resolutions: []string{"1080p", "720p"}}
	result := ScoreWithPreferences(Candidate{
		Title:      "Movie.Title.dvd5.dvdrip-GROUP",
		Resolution: "",
		Language:   "en",
	}, Requirements{Title: "Movie Title", MediaType: "movie"}, prefs)
	if !result.Rejected || result.RejectReason != "resolution_not_allowed" {
		t.Fatalf("expected resolution_not_allowed rejection for undetected resolution, got %+v", result)
	}
}

func TestScoreWithPreferencesRejectsTooLarge(t *testing.T) {
	// 8 GB candidate; 120-min runtime × 34 MB/min max = 4080 MB limit → too_large.
	result := ScoreWithPreferences(Candidate{
		Title:      "Dune.2021.1080p.WEB-DL",
		SizeBytes:  8 * 1024 * 1024 * 1024,
		Resolution: "1080p",
		Source:     "web-dl",
		Language:   "en",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021, RuntimeMinutes: 120},
		Preferences{MaxMBPerMinute: 34})
	if !result.Rejected || result.RejectReason != "too_large" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestScoreWithPreferencesTracksCustomFormatScoreSeparately(t *testing.T) {
	result := ScoreWithPreferences(Candidate{
		Title:      "Dune.2021.1080p.WEB-DL.Atmos-GRP",
		Resolution: "1080p",
		Source:     "web-dl",
		Language:   "en",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021}, Preferences{
		CustomFormats: []CustomFormat{
			{Name: "Atmos", Pattern: `(?i)\bAtmos\b`, Score: 125, Enabled: true},
		},
	})
	if result.Rejected {
		t.Fatalf("unexpected rejection %+v", result)
	}
	if result.CustomFormatScore != 125 {
		t.Fatalf("expected custom format subtotal 125, got %+v", result)
	}
	if result.Score < result.CustomFormatScore {
		t.Fatalf("expected total score to include custom format subtotal, got %+v", result)
	}
	if len(result.Explanations) == 0 {
		t.Fatalf("expected ranking explanations, got %+v", result)
	}
	foundCustomFormat := false
	for _, explanation := range result.Explanations {
		if strings.Contains(explanation, "Custom format Atmos") {
			foundCustomFormat = true
			break
		}
	}
	if !foundCustomFormat {
		t.Fatalf("expected custom format explanation, got %+v", result.Explanations)
	}
}

// TestTitleMatchRegressions guards against past false-positive and false-negative
// title matches that caused wrong content to be downloaded.
func TestScoreWithPreferencesBlockRulePenalty(t *testing.T) {
	result := ScoreWithPreferences(Candidate{
		Title:        "Dune.2021.1080p.WEB-DL-YIFY",
		Resolution:   "1080p",
		Source:       "web-dl",
		ReleaseGroup: "YIFY",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021}, Preferences{
		BlockRules: []BlockRule{
			{Type: "release_group", Pattern: "YIFY", MediaType: "both", Action: "penalty", ScorePenalty: 50, Enabled: true},
		},
	})
	if result.Rejected {
		t.Fatalf("penalty block rule must not reject, got %+v", result)
	}
	foundPenalty := false
	for _, e := range result.Explanations {
		if strings.Contains(e, "YIFY") && strings.Contains(e, "-50") {
			foundPenalty = true
			break
		}
	}
	if !foundPenalty {
		t.Fatalf("expected penalty explanation for YIFY, got %v", result.Explanations)
	}
}

func TestScoreWithPreferencesBlockRuleBlocks(t *testing.T) {
	result := ScoreWithPreferences(Candidate{
		Title:        "Dune.2021.1080p.WEB-DL-BANNED",
		Resolution:   "1080p",
		Source:       "web-dl",
		ReleaseGroup: "BANNED",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021}, Preferences{
		BlockRules: []BlockRule{
			{Type: "release_group", Pattern: "BANNED", MediaType: "both", Action: "block", Enabled: true},
		},
	})
	if !result.Rejected {
		t.Fatalf("block rule must reject candidate, got %+v", result)
	}
	if !strings.HasPrefix(result.RejectReason, "blocklist:") {
		t.Fatalf("expected reject_reason to start with blocklist:, got %q", result.RejectReason)
	}
}

func TestScoreWithPreferencesIndexerPolicy(t *testing.T) {
	base := ScoreWithPreferences(Candidate{
		Title:      "Dune.2021.1080p.WEB-DL",
		Resolution: "1080p",
		Source:     "web-dl",
		Language:   "en",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021}, Preferences{})
	withPolicy := ScoreWithPreferences(Candidate{
		Title:              "Dune.2021.1080p.WEB-DL",
		Resolution:         "1080p",
		Source:             "web-dl",
		Language:           "en",
		IndexerPolicyScore: 25,
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021}, Preferences{})
	if withPolicy.Score != base.Score+25 {
		t.Fatalf("expected indexer policy to add 25 to score, got base=%d policy=%d", base.Score, withPolicy.Score)
	}
}

// TestScoreIndexerPriorityLowerNumberWinsTies guards the NZBHydra2 "Priority"
// convention (1 highest, higher numbers lower priority, matching Sonarr/
// Radarr) -- a lower IndexerScore must add MORE to the total score than a
// higher one, not less.
func TestScoreIndexerPriorityLowerNumberWinsTies(t *testing.T) {
	reqs := Requirements{Title: "Dune", MediaType: "movie", Year: 2021}
	highPriority := ScoreWithPreferences(Candidate{
		Title:        "Dune.2021.1080p.WEB-DL",
		Resolution:   "1080p",
		Source:       "web-dl",
		Language:     "en",
		IndexerScore: 10,
	}, reqs, Preferences{})
	lowPriority := ScoreWithPreferences(Candidate{
		Title:        "Dune.2021.1080p.WEB-DL",
		Resolution:   "1080p",
		Source:       "web-dl",
		Language:     "en",
		IndexerScore: 30,
	}, reqs, Preferences{})
	if highPriority.Score <= lowPriority.Score {
		t.Fatalf("expected indexer score 10 (higher priority) to outscore 30 (lower priority), got 10=%d 30=%d", highPriority.Score, lowPriority.Score)
	}
}

func TestCompatibilityWarningsDolbyVision(t *testing.T) {
	result := ScoreWithPreferences(Candidate{
		Title:      "Dune.2021.2160p.WEB-DL.DV.HDR",
		Resolution: "2160p",
		Source:     "web-dl",
		Language:   "en",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021}, Preferences{})
	found := false
	for _, w := range result.CompatibilityWarnings {
		if strings.Contains(w, "Dolby Vision") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected Dolby Vision warning, got %v", result.CompatibilityWarnings)
	}
}

func TestCompatibilityWarningsAtmos(t *testing.T) {
	result := ScoreWithPreferences(Candidate{
		Title:      "Dune.2021.1080p.WEB-DL.TrueHD.Atmos",
		Resolution: "1080p",
		Source:     "web-dl",
		Language:   "en",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021}, Preferences{})
	found := false
	for _, w := range result.CompatibilityWarnings {
		if strings.Contains(w, "TrueHD") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected TrueHD/Atmos warning, got %v", result.CompatibilityWarnings)
	}
}

func TestCompatibilityWarningsAV1(t *testing.T) {
	result := ScoreWithPreferences(Candidate{
		Title:      "Dune.2021.1080p.WEB-DL.AV1",
		Resolution: "1080p",
		Source:     "web-dl",
		Language:   "en",
		Codec:      "AV1",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021}, Preferences{})
	found := false
	for _, w := range result.CompatibilityWarnings {
		if strings.Contains(w, "AV1") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected AV1 compatibility warning, got %v", result.CompatibilityWarnings)
	}
}

func TestCompatibilityWarningsNoneForStandardContent(t *testing.T) {
	result := ScoreWithPreferences(Candidate{
		Title:      "Dune.2021.1080p.WEB-DL.x264",
		Resolution: "1080p",
		Source:     "web-dl",
		Language:   "en",
		Codec:      "x264",
	}, Requirements{Title: "Dune", MediaType: "movie", Year: 2021}, Preferences{})
	if len(result.CompatibilityWarnings) > 0 {
		t.Fatalf("expected no warnings for standard H.264 content, got %v", result.CompatibilityWarnings)
	}
}

func TestTitleMatchRegressions(t *testing.T) {
	cases := []struct {
		name      string
		candidate string
		required  string
		wantMatch bool
	}{
		// Reno.911 must NOT match 9-1-1 (TVDB ID-based search bypassed check)
		{"reno911-vs-911", "Reno.911.2003.S01E01.720p.x265.10bit-vrc", "9-1-1", false},
		// 9-1-1 correct releases must still match
		{"911-correct", "9-1-1.S01E01.720p.WEB-DL", "9-1-1", true},
		// Secrets.of.The.Lost.Liners must NOT match Lost
		{"lost-liners-vs-lost", "Secrets.of.The.Lost.Liners.Series.1.5of6.1080p.WEB.x264.AAC-MVGroup", "Lost", false},
		// Lost correct releases must match
		{"lost-correct", "Lost.S01E01.1080p.BluRay", "Lost", true},
		{"lost-with-the", "The.Lost.S01E01.720p", "Lost", true},
		// DCs prefix allowed (1-word franchise tolerance)
		{"dcs-legends", "DCs.Legends.of.Tomorrow.2016.S06.1080p", "DC's Legends of Tomorrow", true},
		// Marvels prefix allowed
		{"marvels-agents", "Marvels.Agents.of.S.H.I.E.L.D.S01E01.1080p", "Agents of S.H.I.E.L.D.", true},
		// Scene ".s." possessive-apostrophe convention must match the same show
		{"greys-anatomy-dot-s", "Grey.s.Anatomy.S02E01.MULTi.VFi.1080p.WEBRip.EAC3.5.1.H265-TARDiS", "Grey's Anatomy", true},
		{"marvels-punisher-dot-s", "Marvel.s.The.Punisher.S01E01.720p.WEB-DL", "Marvel's The Punisher", true},
		{"mothers-pride-dot-s", "Mother.s.Pride.S01E01.HDTV.x264", "Mother's Pride", true},
		// Leading "The" stripped from candidate
		{"the-batman", "The.Batman.2022.1080p.BluRay", "Batman", true},
		// Leading "The" stripped from required
		{"batman-no-the", "Batman.2022.1080p.BluRay", "The Batman", true},
		// Anime release should not match 9-1-1
		{"anime-vs-911", "[SubsPlease] Kamiina Botan, Yoeru Sugata wa Yuri no Hana - 10 (1080p) [7A9116B7]", "9-1-1", false},
		// 9-1-1: Lone Star correct release
		{"lone-star-correct", "9-1-1.Lone.Star.S04E18.1080p.WEB-DL", "9-1-1: Lone Star", true},
		// TrustSource=true with structured release still applies title check
		{"trust-source-reno911", "Reno.911.2003.S01E01.720p", "9-1-1", false},
		// Production incident: a required title of "The Odyssey" wrongly
		// matched both of these unrelated releases via the 1-word
		// franchise-prefix tolerance (a non-article prefix word -- "Ocean",
		// "Troy" -- before a short 1-2 word required title). "Troy - The
		// Odyssey" was actually selected and symlinked in place of the real
		// movie.
		{"ocean-odyssey-vs-odyssey", "Ocean.Odyssey.Lions.of.the.Deep.German.DL.DOKU.1080p.BluRay.x264-TV4A", "The Odyssey", false},
		{"troy-odyssey-vs-odyssey", "Troy - The Odyssey dvd 5  dvdrip-EAGLE .part04", "The Odyssey", false},
		// The real movie must still match its own correct release, including
		// a DVD rip release of it.
		{"odyssey-correct", "The.Odyssey.2026.1080p.WEB-DL", "The Odyssey", true},
		{"odyssey-dvdrip-correct", "The.Odyssey.2026.DVDRip.XVID-GROUP", "The Odyssey", true},
		// Production incident: a required title of "NCIS" (an exact prefix,
		// offset 0 -- always allowed) wrongly matched spinoff shows whose
		// extra title words land right after "NCIS" and before the
		// season/episode marker. Confirmed live: dozens of "NCIS: New
		// Orleans" episodes were downloaded twice, once correctly under its
		// own library item and once wrongly under the base "NCIS" show's
		// identically-numbered episode.
		{"ncis-vs-new-orleans", "NCIS.New.Orleans.S02E03.Touched.by.the.Sun.1080p.AMZN.WEB-DL.DDP5.1.x264-NTb", "NCIS", false},
		{"ncis-vs-los-angeles", "ncis.los.angeles.417.hdtv-lol", "NCIS", false},
		{"ncis-vs-origins", "NCIS.Origins.S02E04.1080p.10bit.WEBRip.6CH.X265.HEVC-PSA", "NCIS", false},
		{"911-vs-nashville", "9-1-1-Nashville.2025.S01E11.Don.Begins.1080p.AMZN.Webrip.x265.10bit.EAC3.5.1.English-JBENT-TAoE", "9-1-1", false},
		// The base show must still match its own correct release.
		{"ncis-correct", "NCIS.S02E03.Vanished.1080p.WEB-DL", "NCIS", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Test via Score so the full pipeline (including TrustSource) is exercised
			// For cases testing TrustSource, use TrustSource=true to simulate ID-based search
			req := Requirements{Title: tc.required, MediaType: "episode", TrustSource: true}
			result := Score(Candidate{Title: tc.candidate}, req)
			isMatch := result.RejectReason != "wrong_title"
			if isMatch != tc.wantMatch {
				t.Fatalf("title=%q required=%q: wantMatch=%v got rejected=%v reason=%q score=%d",
					tc.candidate, tc.required, tc.wantMatch, result.Rejected, result.RejectReason, result.Score)
			}
		})
	}
}

func TestContainsEpisodeToken(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"Show.Name.S01E01.1080p.WEB-DL", true},
		{"Show.Name.S1E1.1080p.WEB-DL", true},
		{"Show.Name.1x01.HDTV", true},
		{"Show.Name.S01.Complete.1080p", false},
		{"Show.Name.2024.1080p.WEB-DL", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := containsEpisodeToken(tc.title); got != tc.want {
			t.Errorf("containsEpisodeToken(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

func TestMatchEpisode(t *testing.T) {
	cases := []struct {
		name       string
		title      string
		season, ep int
		want       episodeMatch
	}{
		{"exact-sxxexx", "Show.Name.S01E05.1080p.WEB-DL", 1, 5, episodeExact},
		{"exact-nxnn", "Show.Name.1x05.HDTV", 1, 5, episodeExact},
		{"season-pack-complete", "Show.Name.S01.Complete.1080p", 1, 5, episodeSeasonPack},
		{"season-pack-bare", "Show.Name.S01.1080p", 1, 5, episodeSeasonPack},
		{"mismatch-different-episode", "Show.Name.S01E09.1080p.WEB-DL", 1, 5, episodeMismatch},
		{"unknown-no-season-info", "Show.Name.2024.1080p.WEB-DL", 1, 5, episodeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchEpisode(tc.title, tc.season, tc.ep); got != tc.want {
				t.Errorf("matchEpisode(%q, %d, %d) = %v, want %v", tc.title, tc.season, tc.ep, got, tc.want)
			}
		})
	}
}

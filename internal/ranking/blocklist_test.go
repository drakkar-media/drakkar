package ranking

import "testing"

// helpers
func blockRule(ruleType, pattern, mediaType, action string) BlockRule {
	return BlockRule{ID: 1, Type: ruleType, Pattern: pattern, MediaType: mediaType, Action: action, Enabled: true}
}

func penaltyRule(ruleType, pattern string, penalty int) BlockRule {
	return BlockRule{ID: 2, Type: ruleType, Pattern: pattern, MediaType: "both", Action: "penalty", ScorePenalty: penalty, Enabled: true}
}

func candidate(title string) Candidate {
	return Candidate{Title: title}
}

// ── ParseReleaseGroup ────────────────────────────────────────────────────────

func TestParseReleaseGroup(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"Movie.Title.2025.2160p.WEB-DL.DDP5.1.Atmos.H.265-V3SP4EV3R", "V3SP4EV3R"},
		{"Show.Name.S02E04.1080p.WEB-DL.x265.AAC-GalaxyRG", "GalaxyRG"},
		{"Movie.2024.1080p.BluRay.x264-CtrlHD", "CtrlHD"},
		{"NoBraces", ""},
		{"Trailing-", ""},
		{"Has Space-GROUP NAME", ""},
		{"Movie.2024.1080p.BluRay-NTb.mkv", "NTb"},
	}
	for _, c := range cases {
		if got := ParseReleaseGroup(c.title); got != c.want {
			t.Errorf("ParseReleaseGroup(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}

// ── Block: release_group ─────────────────────────────────────────────────────

func TestBlockRuleExactReleaseGroup(t *testing.T) {
	rules := []BlockRule{blockRule("release_group", "GalaxyRG", "both", "block")}
	prefs := Preferences{BlockRules: rules}
	result := ScoreWithPreferences(
		Candidate{Title: "Show.Name.S01E01.1080p.WEB-DL.x265.AAC-GalaxyRG"},
		Requirements{Title: "Show Name", MediaType: "episode", SeasonNumber: 1, EpisodeNumber: 1},
		prefs,
	)
	if !result.Rejected {
		t.Fatal("expected rejection for GalaxyRG release group")
	}
	if result.RejectReason != "blocklist:release_group:GalaxyRG" {
		t.Errorf("unexpected reason: %s", result.RejectReason)
	}
}

func TestBlockRuleReleaseGroupCaseInsensitive(t *testing.T) {
	rules := []BlockRule{blockRule("release_group", "GALAXYRG", "both", "block")}
	prefs := Preferences{BlockRules: rules}
	result := ScoreWithPreferences(
		Candidate{Title: "Show.Name.S01E01.1080p.WEB-DL-GalaxyRG"},
		Requirements{Title: "Show Name", MediaType: "episode", SeasonNumber: 1, EpisodeNumber: 1},
		prefs,
	)
	if !result.Rejected {
		t.Fatal("expected case-insensitive rejection for GALAXYRG / GalaxyRG")
	}
}

func TestBlockRuleReleaseGroupFallback(t *testing.T) {
	// No dash at all — ParseReleaseGroup returns ""; rule should fall back to whole-title search.
	rules := []BlockRule{blockRule("release_group", "GalaxyRG", "both", "block")}
	prefs := Preferences{BlockRules: rules}
	result := ScoreWithPreferences(
		Candidate{Title: "GalaxyRG.Movie.Title.2024.1080p.WEBRIP"},
		Requirements{Title: "Movie Title", MediaType: "movie", Year: 2024},
		prefs,
	)
	if !result.Rejected {
		t.Fatal("expected whole-title fallback block for GalaxyRG")
	}
}

func TestBlockRuleDoesNotMatchDifferentGroup(t *testing.T) {
	rules := []BlockRule{blockRule("release_group", "GalaxyRG", "both", "block")}
	prefs := Preferences{BlockRules: rules}
	result := ScoreWithPreferences(
		Candidate{Title: "Movie.Title.2025.1080p.BluRay.x264-CtrlHD", Source: "bluray", Resolution: "1080p"},
		Requirements{Title: "Movie Title", MediaType: "movie", Year: 2025},
		prefs,
	)
	if result.Rejected {
		t.Fatalf("should not block CtrlHD release; got rejected: %s", result.RejectReason)
	}
}

// ── Block: title_pattern ─────────────────────────────────────────────────────

func TestBlockRuleTitlePattern(t *testing.T) {
	rules := []BlockRule{blockRule("title_pattern", "AI Upscale", "both", "block")}
	prefs := Preferences{BlockRules: rules}
	result := ScoreWithPreferences(
		Candidate{Title: "Movie.Title.2024.1080p.AI Upscale-GROUP"},
		Requirements{Title: "Movie Title", MediaType: "movie", Year: 2024},
		prefs,
	)
	if !result.Rejected {
		t.Fatal("expected rejection for AI Upscale title pattern")
	}
}

func TestBlockRuleTitlePatternCaseInsensitive(t *testing.T) {
	rules := []BlockRule{blockRule("title_pattern", "AI UPSCALE", "both", "block")}
	prefs := Preferences{BlockRules: rules}
	result := ScoreWithPreferences(
		Candidate{Title: "Movie.Title.2024.1080p.ai.upscale-GROUP"},
		Requirements{Title: "Movie Title", MediaType: "movie", Year: 2024},
		prefs,
	)
	if !result.Rejected {
		t.Fatal("expected case-insensitive title pattern block")
	}
}

// ── Block: regex ─────────────────────────────────────────────────────────────

func TestBlockRuleRegex(t *testing.T) {
	rules := []BlockRule{blockRule("regex", `zip[xX]`, "both", "block")}
	prefs := Preferences{BlockRules: rules}
	result := ScoreWithPreferences(
		Candidate{Title: "Show.Name.S01E01.1080p.WEB-DL.zipx-GROUP"},
		Requirements{Title: "Show Name", MediaType: "episode", SeasonNumber: 1, EpisodeNumber: 1},
		prefs,
	)
	if !result.Rejected {
		t.Fatal("expected rejection for zipx regex rule")
	}
}

// ── Block: missing_release_group ─────────────────────────────────────────────

func TestBlockRuleMissingGroupBlock(t *testing.T) {
	// Title with no dash at all — ParseReleaseGroup returns "" → missing group detected.
	rules := []BlockRule{blockRule("missing_release_group", "", "both", "block")}
	prefs := Preferences{BlockRules: rules}
	result := ScoreWithPreferences(
		Candidate{Title: "Movie.Title.2024.1080p.WEBRIP"},
		Requirements{Title: "Movie Title", MediaType: "movie", Year: 2024},
		prefs,
	)
	if !result.Rejected {
		t.Fatal("expected rejection for missing release group")
	}
}

func TestBlockRuleMissingGroupDoesNotBlockWhenGroupPresent(t *testing.T) {
	rules := []BlockRule{blockRule("missing_release_group", "", "both", "block")}
	prefs := Preferences{BlockRules: rules}
	result := ScoreWithPreferences(
		Candidate{Title: "Movie.Title.2024.1080p.BluRay.x264-CtrlHD", Source: "bluray", Resolution: "1080p"},
		Requirements{Title: "Movie Title", MediaType: "movie", Year: 2024},
		prefs,
	)
	if result.Rejected {
		t.Fatalf("should not block when release group is present; got: %s", result.RejectReason)
	}
}

// ── Media type filtering ──────────────────────────────────────────────────────

func TestBlockRuleMovieOnlyDoesNotBlockTV(t *testing.T) {
	rules := []BlockRule{blockRule("release_group", "GalaxyRG", "movie", "block")}
	prefs := Preferences{BlockRules: rules}
	result := ScoreWithPreferences(
		Candidate{Title: "Show.Name.S01E01.1080p.WEB-DL-GalaxyRG"},
		Requirements{Title: "Show Name", MediaType: "episode", SeasonNumber: 1, EpisodeNumber: 1},
		prefs,
	)
	if result.Rejected {
		t.Fatal("movie-only rule should not block TV episode")
	}
}

func TestBlockRuleTVOnlyDoesNotBlockMovie(t *testing.T) {
	rules := []BlockRule{blockRule("release_group", "GalaxyRG", "tv", "block")}
	prefs := Preferences{BlockRules: rules}
	result := ScoreWithPreferences(
		Candidate{Title: "Movie.Title.2025.1080p.WEB-DL-GalaxyRG"},
		Requirements{Title: "Movie Title", MediaType: "movie", Year: 2025},
		prefs,
	)
	if result.Rejected {
		t.Fatal("tv-only rule should not block movie")
	}
}

// ── Disabled rules ────────────────────────────────────────────────────────────

func TestBlockRuleDisabledIsIgnored(t *testing.T) {
	rule := blockRule("release_group", "GalaxyRG", "both", "block")
	rule.Enabled = false
	prefs := Preferences{BlockRules: []BlockRule{rule}}
	result := ScoreWithPreferences(
		Candidate{Title: "Show.Name.S01E01.1080p.WEB-DL-GalaxyRG"},
		Requirements{Title: "Show Name", MediaType: "episode", SeasonNumber: 1, EpisodeNumber: 1},
		prefs,
	)
	if result.Rejected {
		t.Fatal("disabled rule should not block")
	}
}

// ── Penalty action ────────────────────────────────────────────────────────────

func TestBlockRulePenaltyReducesScore(t *testing.T) {
	rules := []BlockRule{penaltyRule("release_group", "GalaxyRG", 500)}
	prefs := Preferences{
		Resolutions: []string{"1080p"},
		Sources:     []string{"web-dl"},
		BlockRules:  rules,
	}
	penalised := ScoreWithPreferences(
		Candidate{Title: "Movie.Title.2025.1080p.WEB-DL-GalaxyRG", Source: "web-dl", Resolution: "1080p"},
		Requirements{Title: "Movie Title", MediaType: "movie", Year: 2025},
		prefs,
	)
	plain := ScoreWithPreferences(
		Candidate{Title: "Movie.Title.2025.1080p.WEB-DL-NTb", Source: "web-dl", Resolution: "1080p"},
		Requirements{Title: "Movie Title", MediaType: "movie", Year: 2025},
		prefs,
	)
	if penalised.Rejected {
		t.Fatal("penalty rule should not reject release")
	}
	if penalised.Score >= plain.Score {
		t.Errorf("penalised score %d should be lower than plain score %d", penalised.Score, plain.Score)
	}
	if plain.Score-penalised.Score != 500 {
		t.Errorf("expected 500 penalty, got diff %d", plain.Score-penalised.Score)
	}
}

// ── TestBlockRules (API helper) ───────────────────────────────────────────────

func TestTestBlockRulesResult(t *testing.T) {
	rules := []BlockRule{
		{ID: 1, Type: "release_group", Pattern: "GalaxyRG", MediaType: "both", Action: "block", Enabled: true},
		{ID: 2, Type: "title_pattern", Pattern: "AI Upscale", MediaType: "both", Action: "block", Enabled: true},
	}

	r1 := TestBlockRules(rules, "Movie.2025.1080p.WEB-DL-GalaxyRG", "movie")
	if !r1.Blocked || r1.Allowed {
		t.Error("GalaxyRG should be blocked")
	}
	if len(r1.MatchedRules) != 1 {
		t.Errorf("expected 1 matched rule, got %d", len(r1.MatchedRules))
	}

	r2 := TestBlockRules(rules, "Movie.2025.1080p.BluRay-NTb", "movie")
	if r2.Blocked || !r2.Allowed {
		t.Errorf("NTb should be allowed, got blocked=%v", r2.Blocked)
	}
	if len(r2.MatchedRules) != 0 {
		t.Errorf("expected 0 matched rules, got %d", len(r2.MatchedRules))
	}
}

// ── Known good releases are not blocked ───────────────────────────────────────

func TestKnownGoodReleasesAllowed(t *testing.T) {
	defaultRules := []BlockRule{
		{ID: 1, Type: "release_group", Pattern: "V3SP4EV3R", MediaType: "both", Action: "block", Enabled: true},
		{ID: 2, Type: "release_group", Pattern: "GalaxyRG", MediaType: "both", Action: "block", Enabled: true},
		{ID: 3, Type: "title_pattern", Pattern: "AI Upscale", MediaType: "both", Action: "block", Enabled: true},
		{ID: 4, Type: "title_pattern", Pattern: "BR-DISK", MediaType: "both", Action: "block", Enabled: true},
	}
	prefs := Preferences{BlockRules: defaultRules}

	good := []struct{ title, mediaType string }{
		{"Movie.Title.2025.2160p.WEB-DL.DDP5.1.Atmos.H.265-NTb", "movie"},
		{"Movie.Title.2025.1080p.BluRay.x264-CtrlHD", "movie"},
		{"Show.Name.S01E01.1080p.WEB-DL.DDP5.1.H.264-FLUX", "episode"},
	}
	for _, g := range good {
		req := Requirements{Title: g.title, MediaType: g.mediaType}
		if g.mediaType == "episode" {
			req = Requirements{Title: "Show Name", MediaType: "episode", SeasonNumber: 1, EpisodeNumber: 1, TrustSource: true}
		} else {
			req = Requirements{Title: "Movie Title", MediaType: "movie", Year: 2025, TrustSource: true}
		}
		result := ScoreWithPreferences(Candidate{Title: g.title}, req, prefs)
		if result.Rejected && result.RejectReason != "" && len(result.RejectReason) > 9 && result.RejectReason[:9] == "blocklist" {
			t.Errorf("good release %q was blocked: %s", g.title, result.RejectReason)
		}
	}
}

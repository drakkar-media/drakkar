package policy

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestDecisionKeyForReason(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		want   string
	}{
		// ── Drakkar-native: article/download failures ─────────────────────────
		{"article missing", "article missing", "missingArticles"},
		{"430 bare", "status 430", "missingArticles"},
		{"nntp_article_unavailable", "nntp_article_unavailable", "missingArticles"},
		{"status 403", "status 403", "nzbFetch403"},
		{"status 404", "status 404", "nzbFetch4xx"},
		{"status 410", "status 410", "nzbFetch4xx"},
		{"status 401", "status 401", "nzbFetch4xx"},
		{"status 451", "status 451", "nzbFetch4xx"},
		{"nzb_fetch generic", "nzb_fetch_failed", "nzbFetchFailed"},
		{"fetch status generic", "fetch status: reset", "nzbFetchFailed"},

		// ── archive / preflight ─────────────────────────────────────────────────
		{"preflight", "preflight_failed", "preflightFailed"},
		{"publish_failed", "publish_failed", "publishFailed"},
		{"no publishable", "no publishable files", "publishFailed"},
		{"archive_video_not_found", "archive_video_not_found", "noEligibleFiles"},
		{"no eligible", "no eligible files in archive", "noEligibleFiles"},
		{"generic archive_ prefix", "archive_headers_invalid", "archiveNeedsExtraction"},
		{"generic archive bare word", "archive extraction error", "archiveNeedsExtraction"},

		// ── search / candidate failures ──────────────────────────────────────────
		{"all_candidates_wrong_title", "all_candidates_wrong_title", "allCandidatesWrongTitle"},
		{"all_candidates_bad_source", "all_candidates_bad_source", "badSource"},
		{"bad_source standalone", "bad_source", "badSource"},
		{"generic all_candidates", "all_candidates_rejected", "allCandidatesRejected"},
		{"no_release_found", "no_release_found", "noReleaseFound"},
		{"no releases spaced", "no releases found", "noReleaseFound"},
		{"interrupted_by_restart", "interrupted_by_restart", "interruptedByRestart"},
		{"stale_worker", "stale_worker", "interruptedByRestart"},
		{"too_many_failures", "too_many_failures", "interruptedByRestart"},

		// ── Sonarr/Radarr-compatible keys ─────────────────────────────────────────
		{"sample unable", "unable to determine if sample", "unableToDetermineSample"},
		{"sample indeterminate", "sample indeterminate", "unableToDetermineSample"},
		{"sample bare", "sample", "sample"},
		{"no audio", "no audio tracks found", "noAudioTracks"},
		{"already imported", "episode already imported", "episodeAlreadyImported"},
		{"not custom format upgrade", "not custom format upgrade", "notCustomFormatUpgrade"},
		{"not upgrade for existing episode", "not upgrade for existing episode", "notEpisodeUpgrade"},
		{"not upgrade for existing movie", "not upgrade for existing movie", "notMovieUpgrade"},
		{"metadata_conflict", "metadata_conflict", "notMovieUpgrade"},
		{"invalid season", "invalid season number", "invalidSeasonEpisode"},
		{"invalid episode", "invalid episode number", "invalidSeasonEpisode"},

		// ── unknown ───────────────────────────────────────────────────────────────
		{"empty", "", ""},
		{"unrecognized", "a completely novel reason", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DecisionKeyForReason(c.reason); got != c.want {
				t.Errorf("DecisionKeyForReason(%q) = %q, want %q", c.reason, got, c.want)
			}
		})
	}
}

// TestDecisionKeyForReasonAllCandidatesWrongTitleOrdering guards the ordering
// explicitly required by the "Generic all_candidates after the specific
// ones" comment in settings.go: "all_candidates_wrong_title" contains
// "all_candidates" as a substring and must resolve to the dedicated
// allCandidatesWrongTitle key, not fall through to the generic
// allCandidatesRejected bucket that a reordering would produce.
func TestDecisionKeyForReasonAllCandidatesWrongTitleOrdering(t *testing.T) {
	if got := DecisionKeyForReason("all_candidates_wrong_title"); got != "allCandidatesWrongTitle" {
		t.Fatalf("DecisionKeyForReason(\"all_candidates_wrong_title\") = %q, want %q (specific case must run before the generic all_candidates case)", got, "allCandidatesWrongTitle")
	}
}

// TestDecisionKeyForReasonAllCandidatesBadSourceOrdering is the same
// regression guard for the "all_candidates_bad_source" / generic
// "all_candidates" / standalone "bad_source" three-way overlap.
func TestDecisionKeyForReasonAllCandidatesBadSourceOrdering(t *testing.T) {
	if got := DecisionKeyForReason("all_candidates_bad_source"); got != "badSource" {
		t.Fatalf("DecisionKeyForReason(\"all_candidates_bad_source\") = %q, want %q", got, "badSource")
	}
}

// TestDecisionKeyForReasonArchiveVideoNotFoundOrdering guards the
// "archive_* must be after archive_video_not_found" comment: without that
// ordering, archive_video_not_found would be swallowed by the generic
// "archive_"/"archive" case and misclassified as archiveNeedsExtraction
// instead of noEligibleFiles.
func TestDecisionKeyForReasonArchiveVideoNotFoundOrdering(t *testing.T) {
	if got := DecisionKeyForReason("archive_video_not_found"); got != "noEligibleFiles" {
		t.Fatalf("DecisionKeyForReason(\"archive_video_not_found\") = %q, want %q (must be checked before the generic archive_ case)", got, "noEligibleFiles")
	}
}

func TestActionForReason(t *testing.T) {
	settings := DefaultSettings()

	cases := []struct {
		reason string
		want   QueueDecisionAction
	}{
		{"status 430", QueueActionRemoveBlocklistAndSearch},
		{"article missing", QueueActionRemoveBlocklistAndSearch},
		{"status 403", QueueActionDoNothing},
		{"status 404", QueueActionRemoveBlocklistAndSearch},
		{"preflight_failed", QueueActionRemoveBlocklistAndSearch},
		{"all_candidates_wrong_title", QueueActionSearchAgain},
		{"all_candidates_bad_source", QueueActionRemoveBlocklistAndSearch},
		{"no_release_found", QueueActionDoNothing},
		{"interrupted_by_restart", QueueActionSearchAgain},
		// A reason with no DecisionKeyForReason mapping must fall back to
		// do-nothing rather than a zero-valued/empty action.
		{"totally unrecognized reason text", QueueActionDoNothing},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			if got := ActionForReason(settings, c.reason); got != c.want {
				t.Errorf("ActionForReason(default, %q) = %q, want %q", c.reason, got, c.want)
			}
		})
	}
}

// TestActionForReasonMissingConfiguredKey covers the case where
// DecisionKeyForReason resolves a key but the settings map has no entry for
// it (e.g. a user-supplied settings override that dropped a key) -- must
// fall back to QueueActionDoNothing, not panic on the missing map entry.
func TestActionForReasonMissingConfiguredKey(t *testing.T) {
	settings := Settings{QueueDecisionActions: map[string]QueueDecisionAction{}}
	if got := ActionForReason(settings, "missing_articles"); got != QueueActionDoNothing {
		t.Fatalf("expected QueueActionDoNothing for an unconfigured key, got %q", got)
	}
}

func TestDefaultSettings(t *testing.T) {
	s := DefaultSettings()
	if s.DuplicateNzbBehavior != "mark_failed" {
		t.Errorf("DuplicateNzbBehavior = %q, want mark_failed", s.DuplicateNzbBehavior)
	}
	if s.ImportStrategy != "symlink" {
		t.Errorf("ImportStrategy = %q, want symlink", s.ImportStrategy)
	}
	if s.BlocklistTTLDays != 30 {
		t.Errorf("BlocklistTTLDays = %d, want 30", s.BlocklistTTLDays)
	}
	if s.FailNzbWithoutVideo {
		t.Error("FailNzbWithoutVideo should default to false")
	}
	if len(s.QueueDecisionActions) == 0 {
		t.Error("expected a non-empty default QueueDecisionActions map")
	}
	if len(s.IgnoredPatterns) == 0 {
		t.Error("expected non-empty default IgnoredPatterns")
	}
}

// TestMergeSettings exercises mergeSettings/Merge. Each subtest calls
// DefaultSettings() fresh rather than sharing one "base" across subtests:
// mergeSettings assigns out := base and then mutates out.QueueDecisionActions
// (a map, so a reference) and reslices out.IgnoredPatterns from base's
// backing array in place, so reusing one base value across independent
// subtests would let an earlier subtest's merge silently corrupt what a
// later subtest reads as its "unmodified base" -- this is production
// behavior in settings.go we must not paper over by accident in the tests.
func TestMergeSettings(t *testing.T) {
	t.Run("empty override preserves base", func(t *testing.T) {
		base := DefaultSettings()
		merged := mergeSettings(base, Settings{})
		if merged.DuplicateNzbBehavior != base.DuplicateNzbBehavior {
			t.Errorf("DuplicateNzbBehavior changed: %q", merged.DuplicateNzbBehavior)
		}
		if merged.ImportStrategy != base.ImportStrategy {
			t.Errorf("ImportStrategy changed: %q", merged.ImportStrategy)
		}
		if merged.BlocklistTTLDays != base.BlocklistTTLDays {
			t.Errorf("BlocklistTTLDays changed: %d", merged.BlocklistTTLDays)
		}
		if !reflect.DeepEqual(merged.IgnoredPatterns, base.IgnoredPatterns) {
			t.Errorf("IgnoredPatterns changed: %v", merged.IgnoredPatterns)
		}
	})

	t.Run("non-empty scalar fields override", func(t *testing.T) {
		base := DefaultSettings()
		override := Settings{
			DuplicateNzbBehavior: "keep_both",
			ImportStrategy:       "hardlink",
			ManualUploadCategory: "manual",
			BlocklistTTLDays:     7,
			FailNzbWithoutVideo:  true,
		}
		merged := mergeSettings(base, override)
		if merged.DuplicateNzbBehavior != "keep_both" {
			t.Errorf("DuplicateNzbBehavior = %q, want keep_both", merged.DuplicateNzbBehavior)
		}
		if merged.ImportStrategy != "hardlink" {
			t.Errorf("ImportStrategy = %q, want hardlink", merged.ImportStrategy)
		}
		if merged.ManualUploadCategory != "manual" {
			t.Errorf("ManualUploadCategory = %q, want manual", merged.ManualUploadCategory)
		}
		if merged.BlocklistTTLDays != 7 {
			t.Errorf("BlocklistTTLDays = %d, want 7", merged.BlocklistTTLDays)
		}
		if !merged.FailNzbWithoutVideo {
			t.Error("FailNzbWithoutVideo should be true after override")
		}
	})

	t.Run("zero BlocklistTTLDays does not override", func(t *testing.T) {
		base := DefaultSettings()
		merged := mergeSettings(base, Settings{BlocklistTTLDays: 0})
		if merged.BlocklistTTLDays != base.BlocklistTTLDays {
			t.Errorf("BlocklistTTLDays = %d, want unchanged %d", merged.BlocklistTTLDays, base.BlocklistTTLDays)
		}
	})

	t.Run("QueueDecisionActions merge adds/overrides individual keys without dropping others", func(t *testing.T) {
		base := DefaultSettings()
		override := Settings{
			QueueDecisionActions: map[string]QueueDecisionAction{
				"missingArticles": QueueActionRemove, // override an existing key
				"brandNewKey":     QueueActionSearchAgain,
				"":                QueueActionRemove, // blank key must be skipped
				"blankValueKey":   "",                // blank value must be skipped
			},
		}
		merged := mergeSettings(base, override)
		if merged.QueueDecisionActions["missingArticles"] != QueueActionRemove {
			t.Errorf("missingArticles = %q, want %q", merged.QueueDecisionActions["missingArticles"], QueueActionRemove)
		}
		if merged.QueueDecisionActions["brandNewKey"] != QueueActionSearchAgain {
			t.Errorf("brandNewKey = %q, want %q", merged.QueueDecisionActions["brandNewKey"], QueueActionSearchAgain)
		}
		if _, ok := merged.QueueDecisionActions[""]; ok {
			t.Error("blank key should not have been merged in")
		}
		if _, ok := merged.QueueDecisionActions["blankValueKey"]; ok {
			t.Error("blank value should not have been merged in")
		}
		// A key not touched by the override must survive unchanged.
		if merged.QueueDecisionActions["noReleaseFound"] != QueueActionDoNothing {
			t.Errorf("untouched key noReleaseFound = %q, want unchanged %q", merged.QueueDecisionActions["noReleaseFound"], QueueActionDoNothing)
		}
	})

	t.Run("IgnoredPatterns override replaces wholesale and trims blanks", func(t *testing.T) {
		base := DefaultSettings()
		override := Settings{IgnoredPatterns: []string{" *.foo ", "", "*.bar"}}
		merged := mergeSettings(base, override)
		want := []string{"*.foo", "*.bar"}
		if !reflect.DeepEqual(merged.IgnoredPatterns, want) {
			t.Errorf("IgnoredPatterns = %v, want %v", merged.IgnoredPatterns, want)
		}
	})
}

func TestMergeIsExportedAlias(t *testing.T) {
	base := DefaultSettings()
	override := Settings{ImportStrategy: "hardlink"}
	if got := Merge(base, override); got.ImportStrategy != "hardlink" {
		t.Errorf("Merge() = %q, want hardlink", got.ImportStrategy)
	}
}

// fakeStore is an in-memory implementation of the Store interface, mirroring
// the real database.DB.GetAppSetting/PutAppSetting JSON marshal/unmarshal
// round-trip (see internal/database/app_settings_repository.go) without
// touching a real database.
type fakeStore struct {
	values map[string][]byte
}

func newFakeStore() *fakeStore {
	return &fakeStore{values: map[string][]byte{}}
}

func (f *fakeStore) GetAppSetting(ctx context.Context, key string, dst any) (bool, error) {
	raw, ok := f.values[key]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, err
	}
	return true, nil
}

func (f *fakeStore) PutAppSetting(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f.values[key] = raw
	return nil
}

func TestServiceSettingsReturnsDefaultsWhenNothingStored(t *testing.T) {
	svc := NewService(newFakeStore())
	got, err := svc.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, DefaultSettings()) {
		t.Errorf("Settings() = %+v, want defaults %+v", got, DefaultSettings())
	}
}

func TestServiceSettingsNilReceiverAndNilStoreReturnDefaults(t *testing.T) {
	var nilSvc *Service
	got, err := nilSvc.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, DefaultSettings()) {
		t.Errorf("nil Service.Settings() = %+v, want defaults", got)
	}

	svcNoStore := NewService(nil)
	got, err = svcNoStore.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, DefaultSettings()) {
		t.Errorf("Service with nil store Settings() = %+v, want defaults", got)
	}
}

func TestServiceUpdateThenSettingsRoundTrips(t *testing.T) {
	svc := NewService(newFakeStore())
	ctx := context.Background()

	updated, err := svc.Update(ctx, Settings{ImportStrategy: "hardlink", BlocklistTTLDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ImportStrategy != "hardlink" {
		t.Fatalf("Update() ImportStrategy = %q, want hardlink", updated.ImportStrategy)
	}
	if updated.BlocklistTTLDays != 14 {
		t.Fatalf("Update() BlocklistTTLDays = %d, want 14", updated.BlocklistTTLDays)
	}

	fetched, err := svc.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fetched, updated) {
		t.Errorf("Settings() after Update() = %+v, want %+v", fetched, updated)
	}
}

func TestServiceUpdateNilReceiverAndNilStoreDoNotPanic(t *testing.T) {
	var nilSvc *Service
	got, err := nilSvc.Update(context.Background(), Settings{ImportStrategy: "hardlink"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ImportStrategy != "hardlink" {
		t.Errorf("nil Service.Update() = %+v, want ImportStrategy=hardlink", got)
	}

	svcNoStore := NewService(nil)
	got, err = svcNoStore.Update(context.Background(), Settings{ImportStrategy: "hardlink"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ImportStrategy != "hardlink" {
		t.Errorf("Service with nil store Update() = %+v, want ImportStrategy=hardlink", got)
	}
}

package policy

import "testing"

// Reason strings below are drawn from actual producers in the codebase, not
// invented for the test:
//   - internal/database/archive_inspect.go: errArchiveCompressionUnsupported,
//     errArchiveSolidUnsupported, errArchiveEncrypted, errArchiveVideoNotFound,
//     errNNTPArticleUnavailable, and its "article missing"/"status 430" text.
//   - internal/database/workflow_repository.go / internal/workflow/service.go:
//     "interrupted_by_restart", "stale_worker", "too_many_failures",
//     "missing_articles", "nntp_article_unavailable", "article not found",
//     "status 423", "no_releases".
//   - internal/ranking/ranking.go: RejectReason "wrong_title", "bad_source".

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		want   FailureKey
	}{
		// ── missing articles (NNTP 430/423, both direct and wrapped) ──────────
		{"status 430 bare", "status 430", KeyMissingArticles},
		{"body status 430 wrapped", "fetch segment: body status 430", KeyMissingArticles},
		{"status 423", "nntp status 423", KeyMissingArticles},
		{"article not found", "article not found on server", KeyMissingArticles},
		{"nntp_article_unavailable", "nntp_article_unavailable", KeyMissingArticles},
		{"article missing", "article missing: <msgid@example>", KeyMissingArticles},

		// ── archive failures ───────────────────────────────────────────────────
		{"archive_compression_unsupported", "archive_compression_unsupported", KeyUnsupportedArchive},
		{"archive_solid_unsupported", "archive_solid_unsupported", KeyUnsupportedArchive},
		{"solid archive bare word", "solid archive detected", KeyUnsupportedArchive},
		{"archive_encrypted", "archive_encrypted", KeyEncryptedArchive},
		{"encrypted bare word", "rar volume is encrypted", KeyEncryptedArchive},

		// ── NZB parse / fetch ──────────────────────────────────────────────────
		{"nzb_parse", "nzb_parse_failed: malformed xml", KeyNZBParseFailed},
		{"parse nzb", "unable to parse nzb document", KeyNZBParseFailed},
		{"indexer error", "indexer error: 500", KeyNZBParseFailed},
		{"status 403", "nzb fetch: status 403", KeyNZBFetch403},
		{"status 429", "status 429 too many requests", KeyNZBFetch403},
		{"status 404", "status 404", KeyNZBFetch4xx},
		{"status 410", "status 410", KeyNZBFetch4xx},
		{"status 401", "status 401", KeyNZBFetch4xx},
		{"status 451", "status 451", KeyNZBFetch4xx},
		{"nzb_fetch generic", "nzb_fetch_failed", KeyNZBFetchFailed},
		{"nzb fetch spaced", "nzb fetch timed out", KeyNZBFetchFailed},
		{"fetch status generic", "fetch status: connection reset", KeyNZBFetchFailed},

		// ── preflight / publish / symlink ───────────────────────────────────────
		{"preflight_failed", "preflight_failed", KeyPreflightFailed},
		{"publish_failed", "publish_failed", KeyPublishFailed},
		{"no publishable", "no publishable files found", KeyPublishFailed},
		{"symlink_failed", "symlink_failed: EEXIST", KeySymlinkFailed},

		// ── no release / candidates ──────────────────────────────────────────────
		{"no_release_found", "no_release_found", KeyNoReleaseFound},
		{"no releases spaced", "no releases matched", KeyNoReleaseFound},
		{"no_releases underscore", "no_releases available", KeyNoReleaseFound},
		{"all_candidates generic", "all_candidates_rejected", KeyAllCandidatesRejected},
		{"wrong_title", "wrong_title", KeyWrongTitle},
		{"wrong title spaced", "wrong title match", KeyWrongTitle},
		{"bad_source standalone", "bad_source", KeyBadSource},

		// ── restart / stale worker / too many failures ──────────────────────────
		{"interrupted_by_restart", "interrupted_by_restart", KeyInterruptedByRestart},
		{"stale_worker", "stale_worker", KeyInterruptedByRestart},
		{"too_many_failures", "too_many_failures", KeyInterruptedByRestart},

		// ── unknown / fallback ───────────────────────────────────────────────────
		{"empty string", "", KeyUnknown},
		{"unrecognized text", "some completely novel failure text", KeyUnknown},

		// ── case-insensitivity and whitespace trimming ──────────────────────────
		{"uppercase status 430", "  STATUS 430  ", KeyMissingArticles},
		{"mixed case archive_encrypted", "Archive_Encrypted", KeyEncryptedArchive},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.reason); got != c.want {
				t.Errorf("Classify(%q) = %q, want %q", c.reason, got, c.want)
			}
		})
	}
}

// TestClassifyAllCandidatesBadSourceOrdering guards the ordering the code
// comments explicitly call out: "all_candidates_bad_source" contains both
// "all_candidates" and "bad_source" as substrings, and must be checked before
// either of those broader cases, or it would be shadowed by the generic
// "all_candidates" branch and misclassified as KeyAllCandidatesRejected
// instead of KeyBadSource.
func TestClassifyAllCandidatesBadSourceOrdering(t *testing.T) {
	got := Classify("all_candidates_bad_source")
	if got != KeyBadSource {
		t.Fatalf("Classify(\"all_candidates_bad_source\") = %q, want %q (the specific all_candidates_bad_source case must be checked before the generic all_candidates case)", got, KeyBadSource)
	}
}

// TestClassifyAllCandidatesGenericShadowsWrongTitle documents the current,
// intentional (if slightly surprising) ordering in Classify: unlike
// DecisionKeyForReason in settings.go (which has a dedicated
// "all_candidates_wrong_title" -> allCandidatesWrongTitle case checked before
// its generic all_candidates case), Classify has no such dedicated case, so
// "all_candidates_wrong_title" falls into the generic "all_candidates" branch
// before the later "wrong_title" case is ever reached, and resolves to
// KeyAllCandidatesRejected rather than KeyWrongTitle. This still produces the
// same recovery Action (ActionSearchAgain) either way, but a change to this
// ordering would silently change which FailureKey callers observe -- pin it
// down so that regressions are caught explicitly rather than only through an
// unrelated action-level assertion.
func TestClassifyAllCandidatesGenericShadowsWrongTitle(t *testing.T) {
	got := Classify("all_candidates_wrong_title")
	if got != KeyAllCandidatesRejected {
		t.Fatalf("Classify(\"all_candidates_wrong_title\") = %q, want %q (generic all_candidates case must run before the wrong_title case)", got, KeyAllCandidatesRejected)
	}
	// Both KeyAllCandidatesRejected and KeyWrongTitle decide to the same
	// action, so this ordering quirk is currently harmless at the Action
	// level -- assert that invariant holds too, so if it ever stops holding,
	// the classification-level test above is the one that must be revisited.
	if Decide(got) != Decide(KeyWrongTitle) {
		t.Fatalf("expected KeyAllCandidatesRejected and KeyWrongTitle to decide to the same action, got %q vs %q", Decide(got), Decide(KeyWrongTitle))
	}
}

// TestClassifyBadSourceAfterWrongTitleOrdering pins down the second ordering
// called out in the code comments: standalone "bad_source" is checked after
// "wrong_title", so a reason string containing both substrings would resolve
// via whichever appears first in the switch. There is no real producer that
// emits both together today, but "all_candidates_bad_source" above already
// covers the substring-shadowing risk for that combination specifically.
// This test instead confirms plain "bad_source" (no all_candidates prefix)
// still reaches its own case and isn't accidentally caught by an earlier one.
func TestClassifyBadSourceAfterWrongTitleOrdering(t *testing.T) {
	if got := Classify("bad_source"); got != KeyBadSource {
		t.Fatalf("Classify(\"bad_source\") = %q, want %q", got, KeyBadSource)
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		key  FailureKey
		want Action
	}{
		{KeyMissingArticles, ActionBlocklistAndSearch},
		{KeyUnsupportedArchive, ActionBlocklistAndSearch},
		{KeyEncryptedArchive, ActionBlocklistAndSearch},
		{KeyNZBParseFailed, ActionBlocklistAndSearch},
		{KeyNZBFetchFailed, ActionBlocklistAndSearch},
		{KeyNZBFetch4xx, ActionBlocklistAndSearch},
		{KeyNZBFetch403, ActionRetryLater},
		{KeyPreflightFailed, ActionSearchAgain},
		{KeyPublishFailed, ActionRetryLater},
		{KeySymlinkFailed, ActionRetryLater},
		{KeyNoReleaseFound, ActionDoNothing},
		{KeyAllCandidatesRejected, ActionSearchAgain},
		{KeyBadSource, ActionBlocklistAndSearch},
		{KeyWrongTitle, ActionSearchAgain},
		{KeyInterruptedByRestart, ActionSearchAgain},
		{KeyUnknown, ActionDoNothing},
		// A key not present in defaultMatrix at all must fall back to
		// ActionDoNothing rather than panicking or zero-valuing.
		{FailureKey("totally_unmapped_key"), ActionDoNothing},
	}
	for _, c := range cases {
		t.Run(string(c.key), func(t *testing.T) {
			if got := Decide(c.key); got != c.want {
				t.Errorf("Decide(%q) = %q, want %q", c.key, got, c.want)
			}
		})
	}
}

func TestDecideFromReason(t *testing.T) {
	cases := []struct {
		reason string
		want   Action
	}{
		{"status 430", ActionBlocklistAndSearch},
		{"nntp_article_unavailable", ActionBlocklistAndSearch},
		{"archive_encrypted", ActionBlocklistAndSearch},
		{"status 403", ActionRetryLater},
		{"status 404", ActionBlocklistAndSearch},
		{"no_release_found", ActionDoNothing},
		{"too_many_failures", ActionSearchAgain},
		{"interrupted_by_restart", ActionSearchAgain},
		{"wrong_title", ActionSearchAgain},
		{"", ActionDoNothing},
		{"never seen before failure", ActionDoNothing},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			if got := DecideFromReason(c.reason); got != c.want {
				t.Errorf("DecideFromReason(%q) = %q, want %q", c.reason, got, c.want)
			}
		})
	}
}

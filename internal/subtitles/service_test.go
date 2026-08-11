package subtitles

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/database"
)

type repoStub struct {
	publicationPaths         []string
	items                    []database.SubtitleFileSummary
	candidates               []database.SubtitleCandidateSummary
	replaced                 []string
	deletedID                int64
	deletedGroup             database.SubtitleDeleteGroup
	searchInput              database.SubtitleSearchInput
	downloadCandidate        database.SubtitleCandidateSummary
	storedCandidates         []database.SubtitleCandidateRecord
	appSettings              map[string]string
	embeddedLanguages        []string
	containerDurationSeconds float64
}

func (r *repoStub) GetEmbeddedSubtitleLanguagesForLibraryItem(ctx context.Context, libraryItemID int64) ([]string, error) {
	return r.embeddedLanguages, nil
}

func (r *repoStub) GetContainerDurationForLibraryItem(ctx context.Context, libraryItemID int64) (float64, bool, error) {
	if r.containerDurationSeconds <= 0 {
		return 0, false, nil
	}
	return r.containerDurationSeconds, true, nil
}

// GetAppSetting/PutAppSetting back the per-provider daily call budget with a
// plain in-memory map, JSON-encoded to mirror the real Postgres-backed
// implementation's marshal/unmarshal round trip.
func (r *repoStub) GetAppSetting(ctx context.Context, key string, dst any) (bool, error) {
	raw, ok := r.appSettings[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal([]byte(raw), dst)
}

func (r *repoStub) PutAppSetting(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if r.appSettings == nil {
		r.appSettings = make(map[string]string)
	}
	r.appSettings[key] = string(raw)
	return nil
}

func (r *repoStub) ListSubtitleFiles(ctx context.Context, libraryItemID int64) ([]database.SubtitleFileSummary, error) {
	return r.items, nil
}

func (r *repoStub) ListSubtitleCandidates(ctx context.Context, libraryItemID int64) ([]database.SubtitleCandidateSummary, error) {
	return r.candidates, nil
}

// GetSubtitleCandidate looks candidateID up in r.candidates when present, so
// tests with more than one candidate (e.g. one per language) get the right
// one back; r.downloadCandidate remains as a fallback for tests that only
// care about a single fixed candidate and never populate r.candidates.
func (r *repoStub) GetSubtitleCandidate(ctx context.Context, candidateID int64) (database.SubtitleCandidateSummary, error) {
	for _, c := range r.candidates {
		if c.ID == candidateID {
			return c, nil
		}
	}
	return r.downloadCandidate, nil
}

func (r *repoStub) GetSubtitleSearchInput(ctx context.Context, libraryItemID int64) (database.SubtitleSearchInput, error) {
	return r.searchInput, nil
}

func (r *repoStub) ListPublicationPathsForLibraryItem(ctx context.Context, libraryItemID int64) ([]string, error) {
	return r.publicationPaths, nil
}

func (r *repoStub) ReplaceSubtitleFiles(ctx context.Context, libraryItemID int64, provider, language string, paths []string) error {
	r.replaced = append([]string(nil), paths...)
	return nil
}

func (r *repoStub) ReplaceSubtitleCandidates(ctx context.Context, libraryItemID int64, provider string, candidates []database.SubtitleCandidateRecord) error {
	r.storedCandidates = append([]database.SubtitleCandidateRecord(nil), candidates...)
	return nil
}

func (r *repoStub) DeleteSubtitleFile(ctx context.Context, subtitleID int64) (database.SubtitleDeleteGroup, error) {
	r.deletedID = subtitleID
	return r.deletedGroup, nil
}

func (r *repoStub) ListSubtitleLibrary(ctx context.Context, filter database.SubtitleLibraryFilter) (database.SubtitleLibraryPage, error) {
	return database.SubtitleLibraryPage{}, nil
}

type providerStub struct {
	search   []ProviderCandidate
	body     []byte
	fileName string
	err      error
}

func (p providerStub) Name() string { return "subdl" }
func (p providerStub) Search(ctx context.Context, input database.SubtitleSearchInput, languages []string) ([]ProviderCandidate, error) {
	return p.search, nil
}
func (p providerStub) Download(ctx context.Context, rawURL string) (string, []byte, error) {
	if p.err != nil {
		return "", nil, p.err
	}
	return p.fileName, p.body, nil
}

// searchCall records one Search invocation for assertions in tests that
// care about which providers were actually called, and with which
// languages -- the whole point of the per-language-skip and
// provider-rotation behavior being tested.
type searchCall struct {
	provider  string
	languages []string
}

// countingProviderStub returns candidates keyed by language and records
// every Search call (provider name + requested languages) into *calls, so
// tests can assert on-the-wire provider call behavior without needing a
// real provider.
type countingProviderStub struct {
	name       string
	candidates map[string][]ProviderCandidate
	calls      *[]searchCall
}

func (p countingProviderStub) Name() string { return p.name }
func (p countingProviderStub) Search(ctx context.Context, input database.SubtitleSearchInput, languages []string) ([]ProviderCandidate, error) {
	*p.calls = append(*p.calls, searchCall{provider: p.name, languages: append([]string(nil), languages...)})
	var out []ProviderCandidate
	for _, lang := range languages {
		out = append(out, p.candidates[lang]...)
	}
	return out, nil
}
func (p countingProviderStub) Download(ctx context.Context, rawURL string) (string, []byte, error) {
	return "sub.srt", []byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n"), nil
}

type providerStubNamed struct {
	name     string
	search   []ProviderCandidate
	body     []byte
	fileName string
	err      error
}

func (p providerStubNamed) Name() string { return p.name }
func (p providerStubNamed) Search(ctx context.Context, input database.SubtitleSearchInput, languages []string) ([]ProviderCandidate, error) {
	return p.search, nil
}
func (p providerStubNamed) Download(ctx context.Context, rawURL string) (string, []byte, error) {
	if p.err != nil {
		return "", nil, p.err
	}
	return p.fileName, p.body, nil
}

func TestUploadSubtitleWritesAdjacentFiles(t *testing.T) {
	root := t.TempDir()
	publicationPath := filepath.Join(root, "movies", "Dune (2021).mkv")
	if err := os.MkdirAll(filepath.Dir(publicationPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicationPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo := &repoStub{publicationPaths: []string{publicationPath}}
	service := NewService(repo, nil)

	result, err := service.UploadSubtitle(context.Background(), 42, "EN", "dune.srt", strings.NewReader("1\n00:00:01,000 --> 00:00:02,000\nHello\n"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Language != "en" || len(result.CreatedPaths) != 1 {
		t.Fatalf("unexpected result %+v", result)
	}
	want := filepath.Join(root, "movies", "Dune (2021).en.srt")
	if result.CreatedPaths[0] != want {
		t.Fatalf("unexpected subtitle path %s", result.CreatedPaths[0])
	}
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Hello") {
		t.Fatalf("unexpected subtitle body %q", string(body))
	}
}

func TestUploadSubtitleRequiresPublication(t *testing.T) {
	service := NewService(&repoStub{}, nil)
	_, err := service.UploadSubtitle(context.Background(), 42, "en", "dune.srt", strings.NewReader("abc"))
	if err == nil || err != ErrNoPublishedMedia {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestListSubtitles(t *testing.T) {
	now := time.Now().UTC()
	items := []database.SubtitleFileSummary{{ID: 1, LibraryItemID: 42, Provider: "manual", Language: "en", Path: "/tmp/test.srt", CreatedAt: now}}
	service := NewService(&repoStub{items: items}, nil)
	out, err := service.ListSubtitles(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Path != "/tmp/test.srt" {
		t.Fatalf("unexpected subtitles %+v", out)
	}
}

func TestSearchCandidates(t *testing.T) {
	repo := &repoStub{
		searchInput: database.SubtitleSearchInput{LibraryItemID: 42, MediaType: "movie", Title: "Dune", MovieYear: 2021, TMDBID: 438631},
	}
	service := NewService(repo, []string{"en", "nl"}, providerStub{
		search: []ProviderCandidate{{
			Language:        "en",
			Title:           "Dune.2021.en.srt",
			ReleaseName:     "Dune.2021.1080p.WEB-DL",
			Format:          "srt",
			ExternalID:      "file123",
			DownloadURL:     "http://example/file123",
			HearingImpaired: false,
		}},
	})
	result, err := service.SearchCandidates(context.Background(), 42, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidateCount != 1 {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(repo.storedCandidates) != 1 || repo.storedCandidates[0].ExternalID != "file123" {
		t.Fatalf("unexpected stored candidates %+v", repo.storedCandidates)
	}
}

func TestSearchCandidatesPrefersExactEpisodeOverSeasonPack(t *testing.T) {
	repo := &repoStub{
		searchInput: database.SubtitleSearchInput{
			LibraryItemID: 42,
			MediaType:     "episode",
			ShowTitle:     "The Bear",
			ShowYear:      2022,
			SeasonNumber:  2,
			EpisodeNumber: 3,
			TVDBID:        412567,
		},
	}
	service := NewService(repo, []string{"en"}, providerStub{
		search: []ProviderCandidate{
			{
				Language:      "en",
				Title:         "The.Bear.S02.COMPLETE.en.srt",
				ReleaseName:   "The.Bear.S02.COMPLETE.1080p.WEB",
				Format:        "srt",
				ExternalID:    "pack",
				DownloadURL:   "http://example/pack",
				SeasonNumber:  2,
				EpisodeNumber: 0,
			},
			{
				Language:      "en",
				Title:         "The.Bear.S02E03.en.srt",
				ReleaseName:   "The.Bear.S02E03.1080p.WEB",
				Format:        "srt",
				ExternalID:    "exact",
				DownloadURL:   "http://example/exact",
				SeasonNumber:  2,
				EpisodeNumber: 3,
			},
		},
	})
	result, err := service.SearchCandidates(context.Background(), 42, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidateCount != 2 {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(repo.storedCandidates) != 2 || repo.storedCandidates[0].ExternalID != "exact" {
		t.Fatalf("unexpected candidate ordering %+v", repo.storedCandidates)
	}
	if repo.storedCandidates[0].Score <= repo.storedCandidates[1].Score {
		t.Fatalf("expected exact episode to score higher: %+v", repo.storedCandidates)
	}
}

func TestSearchCandidatesPrefersTitleAndYearMatch(t *testing.T) {
	repo := &repoStub{
		searchInput: database.SubtitleSearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
			TMDBID:        438631,
		},
	}
	service := NewService(repo, []string{"en"}, providerStub{
		search: []ProviderCandidate{
			{
				Language:    "en",
				Title:       "Random.Movie.en.srt",
				ReleaseName: "Random.Movie.2015.1080p",
				Format:      "srt",
				ExternalID:  "weak",
				DownloadURL: "http://example/weak",
			},
			{
				Language:    "en",
				Title:       "Dune.2021.en.srt",
				ReleaseName: "Dune.2021.1080p.WEB-DL",
				Format:      "srt",
				ExternalID:  "strong",
				DownloadURL: "http://example/strong",
			},
		},
	})
	if _, err := service.SearchCandidates(context.Background(), 42, nil); err != nil {
		t.Fatal(err)
	}
	if len(repo.storedCandidates) != 2 || repo.storedCandidates[0].ExternalID != "strong" {
		t.Fatalf("unexpected candidate ordering %+v", repo.storedCandidates)
	}
}

func TestSearchCandidatesPrefersProviderBiasWhenMatchesTie(t *testing.T) {
	repo := &repoStub{
		searchInput: database.SubtitleSearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
			TMDBID:        438631,
		},
	}
	subdlSvc := NewService(repo, []string{"en"}, providerStub{
		search: []ProviderCandidate{{
			Language:    "en",
			Title:       "Dune.2021.en.srt",
			ReleaseName: "Dune.2021.1080p.WEB-DL",
			Format:      "srt",
			ExternalID:  "subdl-1",
			DownloadURL: "http://example/subdl-1",
		}},
	})
	if _, err := subdlSvc.SearchCandidates(context.Background(), 42, nil); err != nil {
		t.Fatal(err)
	}
	subdlScore := repo.storedCandidates[0].Score

	repo.storedCandidates = nil
	openSvc := NewService(repo, []string{"en"}, providerStubNamed{
		name: "opensubtitles",
		search: []ProviderCandidate{{
			Language:    "en",
			Title:       "Dune.2021.en.srt",
			ReleaseName: "Dune.2021.1080p.WEB-DL",
			Format:      "srt",
			ExternalID:  "os-1",
			DownloadURL: "777",
		}},
	})
	if _, err := openSvc.SearchCandidates(context.Background(), 42, nil); err != nil {
		t.Fatal(err)
	}
	openScore := repo.storedCandidates[0].Score
	if openScore <= subdlScore {
		t.Fatalf("expected opensubtitles score > subdl score, got %d <= %d", openScore, subdlScore)
	}
}

// TestSearchCandidatesSkipsLanguagesAlreadyDownloaded guards the core fix
// requested for the subtitle system: a language that already has a
// subtitle_files row must not be re-searched. Only "nl" (missing) should
// ever reach the provider; "en" (already satisfied) must never appear in
// any Search call's requested languages.
func TestSearchCandidatesSkipsLanguagesAlreadyDownloaded(t *testing.T) {
	var calls []searchCall
	repo := &repoStub{
		items: []database.SubtitleFileSummary{
			{ID: 1, LibraryItemID: 42, Provider: "manual", Language: "en", Path: "/x/en.srt"},
		},
		searchInput: database.SubtitleSearchInput{LibraryItemID: 42, MediaType: "movie", Title: "Dune", MovieYear: 2021},
	}
	service := NewService(repo, nil, countingProviderStub{
		name: "subdl",
		candidates: map[string][]ProviderCandidate{
			"en": {{Language: "en", Title: "en", ExternalID: "en-1", DownloadURL: "en-1"}},
			"nl": {{Language: "nl", Title: "nl", ExternalID: "nl-1", DownloadURL: "nl-1"}},
		},
		calls: &calls,
	})

	result, err := service.SearchCandidates(context.Background(), 42, []string{"en", "nl"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || len(calls[0].languages) != 1 || calls[0].languages[0] != "nl" {
		t.Fatalf("expected exactly one call requesting only 'nl', got %+v", calls)
	}
	if result.CandidateCount != 1 {
		t.Fatalf("expected only the nl candidate to be stored, got %+v", result)
	}
}

// TestSearchCandidatesSkipsLanguagesAlreadyEmbedded mirrors
// TestSearchCandidatesSkipsLanguagesAlreadyDownloaded but for a language the
// media file already has embedded (per the ffprobe-based probe) rather
// than one with an existing downloaded subtitle_files row -- both must
// behave identically from SearchCandidates' point of view.
func TestSearchCandidatesSkipsLanguagesAlreadyEmbedded(t *testing.T) {
	var calls []searchCall
	repo := &repoStub{
		embeddedLanguages: []string{"en"},
		searchInput:       database.SubtitleSearchInput{LibraryItemID: 42, MediaType: "movie", Title: "Dune", MovieYear: 2021},
	}
	service := NewService(repo, nil, countingProviderStub{
		name: "subdl",
		candidates: map[string][]ProviderCandidate{
			"en": {{Language: "en", Title: "en", ExternalID: "en-1", DownloadURL: "en-1"}},
			"nl": {{Language: "nl", Title: "nl", ExternalID: "nl-1", DownloadURL: "nl-1"}},
		},
		calls: &calls,
	})

	result, err := service.SearchCandidates(context.Background(), 42, []string{"en", "nl"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || len(calls[0].languages) != 1 || calls[0].languages[0] != "nl" {
		t.Fatalf("expected exactly one call requesting only 'nl' (en already embedded), got %+v", calls)
	}
	if result.CandidateCount != 1 {
		t.Fatalf("expected only the nl candidate to be stored, got %+v", result)
	}
}

// TestSearchCandidatesSkipsProviderEntirelyWhenFullySatisfied guards the
// zero-call case: if every requested language already has a file, no
// provider should be contacted at all.
func TestSearchCandidatesSkipsProviderEntirelyWhenFullySatisfied(t *testing.T) {
	var calls []searchCall
	repo := &repoStub{
		items: []database.SubtitleFileSummary{
			{ID: 1, LibraryItemID: 42, Provider: "manual", Language: "en", Path: "/x/en.srt"},
			{ID: 2, LibraryItemID: 42, Provider: "manual", Language: "nl", Path: "/x/nl.srt"},
		},
	}
	service := NewService(repo, nil, countingProviderStub{name: "subdl", calls: &calls})

	result, err := service.SearchCandidates(context.Background(), 42, []string{"en", "nl"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected zero provider calls, got %+v", calls)
	}
	if result.CandidateCount != 0 {
		t.Fatalf("expected no candidates, got %+v", result)
	}
}

// TestSearchCandidatesFallsBackToSecondProviderForMissingLanguage guards
// that provider distribution doesn't come at the cost of correctness: if
// the first-tried provider can't find a still-missing language, the next
// provider must still be tried for it, rather than giving up.
func TestSearchCandidatesFallsBackToSecondProviderForMissingLanguage(t *testing.T) {
	var calls []searchCall
	repo := &repoStub{
		searchInput: database.SubtitleSearchInput{LibraryItemID: 1, MediaType: "movie", Title: "Dune", MovieYear: 2021},
	}
	// libraryItemID 1 with 2 providers: assignedProviderOrder(["a","b"], 1)
	// rotates to start at "b" (1 % 2 == 1) -- "b" only has "en", so "nl"
	// must fall through to "a".
	providerA := countingProviderStub{
		name:       "a",
		candidates: map[string][]ProviderCandidate{"nl": {{Language: "nl", Title: "nl", ExternalID: "a-nl", DownloadURL: "a-nl"}}},
		calls:      &calls,
	}
	providerB := countingProviderStub{
		name:       "b",
		candidates: map[string][]ProviderCandidate{"en": {{Language: "en", Title: "en", ExternalID: "b-en", DownloadURL: "b-en"}}},
		calls:      &calls,
	}
	service := NewService(repo, nil, providerA, providerB)

	result, err := service.SearchCandidates(context.Background(), 1, []string{"en", "nl"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected both providers to be tried, got %+v", calls)
	}
	if result.CandidateCount != 2 {
		t.Fatalf("expected one candidate from each provider, got %+v", result)
	}
}

// TestAllowProviderCallEnforcesDailyBudget guards the rate-limit half of the
// fix: once a provider's configured daily budget is exhausted, it must not
// be called again that day, even though the language it would have searched
// is still missing.
func TestAllowProviderCallEnforcesDailyBudget(t *testing.T) {
	var calls []searchCall
	repo := &repoStub{
		searchInput: database.SubtitleSearchInput{LibraryItemID: 42, MediaType: "movie", Title: "Dune", MovieYear: 2021},
	}
	service := NewService(repo, nil, countingProviderStub{
		name:       "subdl",
		candidates: map[string][]ProviderCandidate{"en": {{Language: "en", Title: "en", ExternalID: "en-1", DownloadURL: "en-1"}}},
		calls:      &calls,
	})
	service.SetProviderBudgets(map[string]int{"subdl": 1})

	if _, err := service.SearchCandidates(context.Background(), 42, []string{"en"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 call after first search, got %+v", calls)
	}

	// "en" is still missing (SearchCandidates never creates files itself),
	// so a second search would normally call the provider again -- but the
	// budget of 1 is already spent.
	if _, err := service.SearchCandidates(context.Background(), 42, []string{"en"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected the exhausted provider to be skipped, still got %+v", calls)
	}
}

// TestAssignedProviderOrderSpreadsAcrossItems is a plain unit test of the
// rotation helper: different library items should not all start at the same
// provider, which is the mechanism that spreads real API calls across
// providers instead of every item hitting every provider.
func TestAssignedProviderOrderSpreadsAcrossItems(t *testing.T) {
	names := []string{"opensubtitles", "subdl"}
	firstOf := func(id int64) string { return assignedProviderOrder(names, id)[0] }
	if firstOf(0) == firstOf(1) {
		t.Fatalf("expected item 0 and item 1 to start with different providers, both got %q", firstOf(0))
	}
	// The rotation must still return every provider (as a fallback chain),
	// just reordered.
	order := assignedProviderOrder(names, 1)
	if len(order) != 2 {
		t.Fatalf("expected rotation to preserve all providers, got %+v", order)
	}
}

// TestSearchAndDownloadBestDownloadsOnePerMissingLanguage guards the other
// half of "one subtitle per language is enough": when multiple languages
// are missing and candidates exist for each, every missing language should
// get its own downloaded file in one call, not just a single overall best.
func TestSearchAndDownloadBestDownloadsOnePerMissingLanguage(t *testing.T) {
	root := t.TempDir()
	publicationPath := filepath.Join(root, "movies", "Dune (2021).mkv")
	if err := os.MkdirAll(filepath.Dir(publicationPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicationPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &repoStub{
		publicationPaths: []string{publicationPath},
		searchInput:      database.SubtitleSearchInput{LibraryItemID: 42, MediaType: "movie", Title: "Dune", MovieYear: 2021},
		// Pre-populated directly (rather than produced by the provider stub's
		// Search+ReplaceSubtitleCandidates, which repoStub deliberately keeps
		// decoupled from ListSubtitleCandidates -- see GetSubtitleCandidate's
		// comment) so this test only exercises the per-language download
		// selection in SearchAndDownloadBest, matching how the other
		// SearchAndDownloadBest tests in this file already set up fixtures.
		candidates: []database.SubtitleCandidateSummary{
			{ID: 1, LibraryItemID: 42, Provider: "subdl", Language: "en", Format: "srt", DownloadURL: "en-1"},
			{ID: 2, LibraryItemID: 42, Provider: "subdl", Language: "nl", Format: "srt", DownloadURL: "nl-1"},
		},
	}
	service := NewService(repo, nil, providerStub{
		fileName: "sub.srt",
		body:     []byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n"),
	})

	results, err := service.SearchAndDownloadBest(context.Background(), 42, []string{"en", "nl"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected one download per language, got %+v", results)
	}
	gotLangs := map[string]bool{}
	for _, r := range results {
		gotLangs[r.Language] = true
	}
	if !gotLangs["en"] || !gotLangs["nl"] {
		t.Fatalf("expected both en and nl downloaded, got %+v", results)
	}
}

func TestTriggerAutomaticSearch(t *testing.T) {
	root := t.TempDir()
	publicationPath := filepath.Join(root, "movies", "Dune (2021).mkv")
	if err := os.MkdirAll(filepath.Dir(publicationPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicationPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &repoStub{
		searchInput:      database.SubtitleSearchInput{LibraryItemID: 42, MediaType: "movie", Title: "Dune", MovieYear: 2021, TMDBID: 438631},
		publicationPaths: []string{publicationPath},
		candidates: []database.SubtitleCandidateSummary{{
			ID:            9,
			LibraryItemID: 42,
			Provider:      "subdl",
			Language:      "en",
			Format:        "srt",
			DownloadURL:   "http://example/file123.srt",
		}},
		downloadCandidate: database.SubtitleCandidateSummary{
			ID:            9,
			LibraryItemID: 42,
			Provider:      "subdl",
			Language:      "en",
			Format:        "srt",
			DownloadURL:   "http://example/file123.srt",
		},
	}
	service := NewService(repo, []string{"en"}, providerStub{
		search: []ProviderCandidate{{
			Language:    "en",
			Title:       "Dune.2021.en.srt",
			ReleaseName: "Dune.2021.1080p.WEB-DL",
			Format:      "srt",
			ExternalID:  "file123",
			DownloadURL: "http://example/file123",
		}},
		fileName: "file123.srt",
		body:     []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"),
	})
	var ran bool
	service.SetAsyncRunner(func(fn func()) {
		ran = true
		fn()
	})
	service.TriggerAutomaticSearch(42)
	if !ran {
		t.Fatal("expected async runner to execute")
	}
	if len(repo.storedCandidates) != 1 || repo.storedCandidates[0].ExternalID != "file123" {
		t.Fatalf("unexpected stored candidates %+v", repo.storedCandidates)
	}
	body, err := os.ReadFile(filepath.Join(root, "movies", "Dune (2021).en.srt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Hello") {
		t.Fatalf("unexpected subtitle body %q", string(body))
	}
}

func TestSearchAndDownloadBestSkipsExistingSubtitles(t *testing.T) {
	root := t.TempDir()
	publicationPath := filepath.Join(root, "movies", "Dune (2021).mkv")
	if err := os.MkdirAll(filepath.Dir(publicationPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicationPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &repoStub{
		publicationPaths: []string{publicationPath},
		items: []database.SubtitleFileSummary{{
			ID: 1, LibraryItemID: 42, Provider: "manual", Language: "en", Path: filepath.Join(root, "movies", "Dune (2021).en.srt"),
		}},
		searchInput: database.SubtitleSearchInput{LibraryItemID: 42, MediaType: "movie", Title: "Dune", MovieYear: 2021, TMDBID: 438631},
		candidates: []database.SubtitleCandidateSummary{{
			ID:            9,
			LibraryItemID: 42,
			Provider:      "subdl",
			Language:      "en",
			Format:        "srt",
			DownloadURL:   "http://example/file123.srt",
		}},
	}
	service := NewService(repo, []string{"en"}, providerStub{
		search: []ProviderCandidate{{
			Language:    "en",
			Title:       "Dune.2021.en.srt",
			ReleaseName: "Dune.2021.1080p.WEB-DL",
			Format:      "srt",
			ExternalID:  "file123",
			DownloadURL: "http://example/file123",
		}},
		fileName: "file123.srt",
		body:     []byte("should-not-write"),
	})
	result, err := service.SearchAndDownloadBest(context.Background(), 42, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Fatalf("expected no auto download, got %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "movies", "Dune (2021).en.srt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no new subtitle file, err=%v", err)
	}
}

func TestSearchAndDownloadBestFallsThroughFailedCandidates(t *testing.T) {
	root := t.TempDir()
	publicationPath := filepath.Join(root, "movies", "Dune (2021).mkv")
	if err := os.MkdirAll(filepath.Dir(publicationPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicationPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &repoStub{
		publicationPaths: []string{publicationPath},
		searchInput:      database.SubtitleSearchInput{LibraryItemID: 42, MediaType: "movie", Title: "Dune", MovieYear: 2021, TMDBID: 438631},
		candidates: []database.SubtitleCandidateSummary{
			{ID: 9, LibraryItemID: 42, Provider: "missing", Language: "en", Format: "srt", DownloadURL: "missing"},
			{ID: 10, LibraryItemID: 42, Provider: "subdl", Language: "en", Format: "srt", DownloadURL: "good"},
		},
		downloadCandidate: database.SubtitleCandidateSummary{
			ID:            10,
			LibraryItemID: 42,
			Provider:      "subdl",
			Language:      "en",
			Format:        "srt",
			DownloadURL:   "good",
		},
	}
	service := NewService(repo, []string{"en"}, providerStub{
		search: []ProviderCandidate{
			{Language: "en", Title: "bad", ReleaseName: "bad", Format: "srt", ExternalID: "x", DownloadURL: "missing"},
			{Language: "en", Title: "good", ReleaseName: "good", Format: "srt", ExternalID: "y", DownloadURL: "good"},
		},
		fileName: "good.srt",
		body:     []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"),
	})
	result, err := service.SearchAndDownloadBest(context.Background(), 42, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || len(result[0].CreatedPaths) != 1 {
		t.Fatalf("unexpected result %+v", result)
	}
	body, err := os.ReadFile(filepath.Join(root, "movies", "Dune (2021).en.srt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Hello") {
		t.Fatalf("unexpected subtitle body %q", string(body))
	}
}

func TestDownloadCandidatePublishesSubtitle(t *testing.T) {
	root := t.TempDir()
	publicationPath := filepath.Join(root, "movies", "Dune (2021).mkv")
	if err := os.MkdirAll(filepath.Dir(publicationPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicationPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &repoStub{
		publicationPaths: []string{publicationPath},
		downloadCandidate: database.SubtitleCandidateSummary{
			ID:            7,
			LibraryItemID: 42,
			Provider:      "subdl",
			Language:      "en",
			Format:        "srt",
			DownloadURL:   "http://example/file123.srt",
		},
	}
	service := NewService(repo, []string{"en"}, providerStub{
		fileName: "file123.srt",
		body:     []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"),
	})
	result, err := service.DownloadCandidate(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "subdl" || len(result.CreatedPaths) != 1 {
		t.Fatalf("unexpected result %+v", result)
	}
	body, err := os.ReadFile(filepath.Join(root, "movies", "Dune (2021).en.srt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Hello") {
		t.Fatalf("unexpected subtitle body %q", string(body))
	}
}

// TestDownloadCandidateCorrectsFramerateMismatch guards the actual wiring:
// DownloadCandidate must run the freshly-downloaded body through the
// framerate-mismatch sync correction before publishing it, when the repo
// reports a known container duration that implies a real framerate
// mismatch against the subtitle's own timestamps.
func TestDownloadCandidateCorrectsFramerateMismatch(t *testing.T) {
	root := t.TempDir()
	publicationPath := filepath.Join(root, "movies", "Dune (2021).mkv")
	if err := os.MkdirAll(filepath.Dir(publicationPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicationPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Subtitle's own last cue implies a 12s runtime; the "real" video is
	// reported as 12 * (23.976/25) -- the classic PAL/film framerate
	// mismatch, which correctFramerateMismatch must recognize and correct.
	videoDuration := 12.0 * (23.976 / 25.0)
	repo := &repoStub{
		publicationPaths:         []string{publicationPath},
		containerDurationSeconds: videoDuration,
		downloadCandidate: database.SubtitleCandidateSummary{
			ID:            7,
			LibraryItemID: 42,
			Provider:      "subdl",
			Language:      "en",
			Format:        "srt",
			DownloadURL:   "http://example/file123.srt",
		},
	}
	service := NewService(repo, []string{"en"}, providerStub{
		fileName: "file123.srt",
		body:     []byte(sampleSRT),
	})
	if _, err := service.DownloadCandidate(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "movies", "Dune (2021).en.srt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == sampleSRT {
		t.Fatal("expected the published subtitle to have corrected timestamps, got the original unmodified body")
	}
	lastMs := maxSubtitleTimestampMs(body)
	wantMs := int64(videoDuration * 1000)
	if diff := lastMs - wantMs; diff < -50 || diff > 50 {
		t.Fatalf("published subtitle's last cue = %dms, want within 50ms of %dms", lastMs, wantMs)
	}
}

func TestDownloadCandidateExtractsZipSubtitle(t *testing.T) {
	root := t.TempDir()
	publicationPath := filepath.Join(root, "tv", "The Bear.mkv")
	if err := os.MkdirAll(filepath.Dir(publicationPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicationPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	first, err := zw.Create("other/random.vtt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Write([]byte("WEBVTT")); err != nil {
		t.Fatal(err)
	}
	best, err := zw.Create("The.Bear.S02E03.en.srt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := best.Write([]byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	repo := &repoStub{
		publicationPaths: []string{publicationPath},
		downloadCandidate: database.SubtitleCandidateSummary{
			ID:            7,
			LibraryItemID: 42,
			Provider:      "subdl",
			Language:      "en",
			Title:         "The.Bear.S02E03.en.zip",
			ReleaseName:   "The.Bear.S02E03.1080p.WEB",
			Format:        "zip",
			DownloadURL:   "http://example/file123.zip",
		},
	}
	service := NewService(repo, []string{"en"}, providerStub{
		fileName: "file123.zip",
		body:     archive.Bytes(),
	})
	result, err := service.DownloadCandidate(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "subdl" || len(result.CreatedPaths) != 1 {
		t.Fatalf("unexpected result %+v", result)
	}
	body, err := os.ReadFile(filepath.Join(root, "tv", "The Bear.en.srt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Hello") {
		t.Fatalf("unexpected subtitle body %q", string(body))
	}
}

func TestRepublishStoredSubtitles(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "old", "Dune (2021).en.srt")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	newPublicationPath := filepath.Join(root, "movies", "Dune (2021).mkv")
	if err := os.MkdirAll(filepath.Dir(newPublicationPath), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := &repoStub{
		publicationPaths: []string{newPublicationPath},
		items: []database.SubtitleFileSummary{{
			ID: 1, LibraryItemID: 42, Provider: "manual", Language: "en", Path: oldPath,
		}},
	}
	service := NewService(repo, nil)
	if err := service.RepublishStoredSubtitles(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "movies", "Dune (2021).en.srt")
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello" {
		t.Fatalf("unexpected subtitle body %q", string(body))
	}
	if len(repo.replaced) != 1 || repo.replaced[0] != want {
		t.Fatalf("unexpected replaced paths %+v", repo.replaced)
	}
}

func TestDeleteSubtitleRemovesFile(t *testing.T) {
	root := t.TempDir()
	pathA := filepath.Join(root, "movies", "Dune (2021).en.srt")
	pathB := filepath.Join(root, "movies", "Dune (2021).alt.en.srt")
	if err := os.MkdirAll(filepath.Dir(pathA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathA, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &repoStub{deletedGroup: database.SubtitleDeleteGroup{Paths: []string{pathA, pathB}}}
	service := NewService(repo, nil)
	if err := service.DeleteSubtitle(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if repo.deletedID != 7 {
		t.Fatalf("unexpected deleted id %d", repo.deletedID)
	}
	if _, err := os.Stat(pathA); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected file removed, got err=%v", err)
	}
	if _, err := os.Stat(pathB); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected file removed, got err=%v", err)
	}
}

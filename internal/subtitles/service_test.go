package subtitles

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hjongedijk/drakkar/internal/database"
)

type repoStub struct {
	publicationPaths  []string
	items             []database.SubtitleFileSummary
	candidates        []database.SubtitleCandidateSummary
	replaced          []string
	deletedID         int64
	deletedGroup      database.SubtitleDeleteGroup
	searchInput       database.SubtitleSearchInput
	downloadCandidate database.SubtitleCandidateSummary
	storedCandidates  []database.SubtitleCandidateRecord
}

func (r *repoStub) ListSubtitleFiles(ctx context.Context, libraryItemID int64) ([]database.SubtitleFileSummary, error) {
	return r.items, nil
}

func (r *repoStub) ListSubtitleCandidates(ctx context.Context, libraryItemID int64) ([]database.SubtitleCandidateSummary, error) {
	return r.candidates, nil
}

func (r *repoStub) GetSubtitleCandidate(ctx context.Context, candidateID int64) (database.SubtitleCandidateSummary, error) {
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
	if len(result.CreatedPaths) != 0 {
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
	if len(result.CreatedPaths) != 1 {
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

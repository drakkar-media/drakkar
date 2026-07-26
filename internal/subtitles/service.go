// Package subtitles orchestrates subtitle discovery, scoring, download, and
// publication for library items, delegating provider-specific search and
// download behavior to registered subtitles.Provider implementations
// (opensubtitles, subdl).
package subtitles

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/metrics"
	"github.com/drakkar-media/drakkar/internal/subtitleutil"
)

const maxUploadBytes = 2 << 20

// Sentinel errors returned by Service methods. Callers use errors.Is to
// distinguish user-correctable input problems (e.g. ErrUnsupportedSubtitle)
// from environment/state problems (e.g. ErrNoPublishedMedia).
var (
	ErrLanguageRequired      = errors.New("subtitle language required")
	ErrSubtitleEmpty         = errors.New("subtitle upload empty")
	ErrSubtitleTooLarge      = errors.New("subtitle upload too large")
	ErrNoPublishedMedia      = errors.New("no published media for library item")
	ErrUnsupportedSubtitle   = errors.New("unsupported subtitle extension")
	ErrSubtitleArchiveEmpty  = errors.New("subtitle archive has no usable subtitle files")
	ErrNoSubtitleProviders   = errors.New("no subtitle providers configured")
	ErrSubtitleCandidateMiss = errors.New("subtitle candidate provider unavailable")
)

// Repository defines the persistence operations required by Service to
// manage subtitle files and search candidates for library items.
type Repository interface {
	ListSubtitleFiles(ctx context.Context, libraryItemID int64) ([]database.SubtitleFileSummary, error)
	ListSubtitleCandidates(ctx context.Context, libraryItemID int64) ([]database.SubtitleCandidateSummary, error)
	GetSubtitleCandidate(ctx context.Context, candidateID int64) (database.SubtitleCandidateSummary, error)
	GetSubtitleSearchInput(ctx context.Context, libraryItemID int64) (database.SubtitleSearchInput, error)
	ListPublicationPathsForLibraryItem(ctx context.Context, libraryItemID int64) ([]string, error)
	ReplaceSubtitleFiles(ctx context.Context, libraryItemID int64, provider, language string, paths []string) error
	ReplaceSubtitleCandidates(ctx context.Context, libraryItemID int64, provider string, candidates []database.SubtitleCandidateRecord) error
	DeleteSubtitleFile(ctx context.Context, subtitleID int64) (database.SubtitleDeleteGroup, error)
	ListSubtitleLibrary(ctx context.Context, filter database.SubtitleLibraryFilter) (database.SubtitleLibraryPage, error)
}

// Provider defines the operations a subtitle source (e.g. subdl,
// opensubtitles) must implement to be registered with Service.
//
// Implementations must be safe for concurrent use, since Service may invoke
// them from the async search-and-download path (see TriggerAutomaticSearch)
// while a foreground request is also in flight.
type Provider interface {
	// Name returns the lowercase provider identifier used to route stored
	// candidates back to the provider that produced them.
	Name() string
	Search(ctx context.Context, input database.SubtitleSearchInput, languages []string) ([]ProviderCandidate, error)
	Download(ctx context.Context, rawURL string) (string, []byte, error)
}

// ProviderCandidate is a single subtitle result returned by a Provider's
// Search, before it has been scored or persisted as a
// database.SubtitleCandidateRecord.
type ProviderCandidate struct {
	Language        string
	Title           string
	ReleaseName     string
	Format          string
	HearingImpaired bool
	ExternalID      string
	DownloadURL     string
	SeasonNumber    int
	EpisodeNumber   int
}

// UploadResult describes a subtitle that was successfully written to disk
// and recorded against a library item, whether via manual upload or provider
// download.
type UploadResult struct {
	LibraryItemID int64    `json:"libraryItemId"`
	Language      string   `json:"language"`
	Provider      string   `json:"provider"`
	CreatedPaths  []string `json:"createdPaths"`
}

// SearchResult summarizes the outcome of a SearchCandidates call.
type SearchResult struct {
	LibraryItemID  int64 `json:"libraryItemId"`
	CandidateCount int   `json:"candidateCount"`
}

// Service orchestrates subtitle search, scoring, download, and publication
// across all registered providers.
//
// Service is safe for concurrent use: providers is built once at
// construction and never mutated, and runAsync is only reassigned during
// setup (see SetAsyncRunner).
type Service struct {
	repo             Repository
	providers        map[string]Provider
	defaultLanguages []string
	runAsync         func(func())
}

// NewService creates a Service backed by repo, using defaultLanguages when a
// caller does not specify languages explicitly, and registering providers
// (keyed by their lowercased, trimmed Name()). A nil provider is skipped.
func NewService(repo Repository, defaultLanguages []string, providers ...Provider) *Service {
	lookup := make(map[string]Provider, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		lookup[strings.ToLower(strings.TrimSpace(provider.Name()))] = provider
	}
	return &Service{
		repo:             repo,
		providers:        lookup,
		defaultLanguages: normalizeLanguages(defaultLanguages),
		runAsync: func(fn func()) {
			go fn()
		},
	}
}

// SetAsyncRunner overrides how TriggerAutomaticSearch schedules its
// background work (default: a bare `go fn()`). A nil fn is ignored. Intended
// for tests that need deterministic execution instead of a real goroutine.
func (s *Service) SetAsyncRunner(fn func(func())) {
	if fn == nil {
		return
	}
	s.runAsync = fn
}

// ListSubtitles returns the subtitle files currently stored for libraryItemID.
func (s *Service) ListSubtitles(ctx context.Context, libraryItemID int64) ([]database.SubtitleFileSummary, error) {
	return s.repo.ListSubtitleFiles(ctx, libraryItemID)
}

// ListCandidates returns the persisted subtitle search candidates for
// libraryItemID, as last produced by SearchCandidates.
func (s *Service) ListCandidates(ctx context.Context, libraryItemID int64) ([]database.SubtitleCandidateSummary, error) {
	return s.repo.ListSubtitleCandidates(ctx, libraryItemID)
}

// SearchCandidates queries every registered provider for libraryItemID and
// replaces that provider's stored candidates with the results, scored and
// sorted best-first.
//
// languages, if empty, falls back to the service's configured default
// languages. Providers are queried in a fixed (name-sorted) order for
// deterministic output; a failure from any provider aborts the whole call
// without touching candidates from providers already processed in this
// invocation, since their ReplaceSubtitleCandidates call already committed.
//
// Errors:
//   - ErrNoSubtitleProviders: no providers are registered.
func (s *Service) SearchCandidates(ctx context.Context, libraryItemID int64, languages []string) (SearchResult, error) {
	if len(s.providers) == 0 {
		return SearchResult{}, ErrNoSubtitleProviders
	}
	input, err := s.repo.GetSubtitleSearchInput(ctx, libraryItemID)
	if err != nil {
		return SearchResult{}, err
	}
	languages = s.requestedLanguages(languages)

	total := 0
	for _, name := range sortedProviderNames(s.providers) {
		provider := s.providers[name]
		raw, err := provider.Search(ctx, input, languages)
		if err != nil {
			return SearchResult{}, err
		}
		candidates := make([]database.SubtitleCandidateRecord, 0, len(raw))
		for _, item := range raw {
			candidates = append(candidates, database.SubtitleCandidateRecord{
				Provider:        provider.Name(),
				Language:        normalizeLanguage(item.Language),
				Title:           strings.TrimSpace(item.Title),
				ReleaseName:     strings.TrimSpace(item.ReleaseName),
				Format:          strings.ToLower(strings.TrimSpace(item.Format)),
				HearingImpaired: item.HearingImpaired,
				Score:           scoreCandidate(provider.Name(), input, languages, item),
				ExternalID:      strings.TrimSpace(item.ExternalID),
				DownloadURL:     strings.TrimSpace(item.DownloadURL),
			})
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			return candidates[i].Score > candidates[j].Score
		})
		if err := s.repo.ReplaceSubtitleCandidates(ctx, libraryItemID, provider.Name(), candidates); err != nil {
			return SearchResult{}, err
		}
		total += len(candidates)
	}
	return SearchResult{LibraryItemID: libraryItemID, CandidateCount: total}, nil
}

// TriggerAutomaticSearch schedules a best-effort SearchAndDownloadBest for
// libraryItemID on the configured async runner (a background goroutine by
// default) and returns immediately without reporting the outcome. Intended
// to be called from request paths (e.g. after import) where subtitle
// fetching must not block or fail the caller.
func (s *Service) TriggerAutomaticSearch(libraryItemID int64) {
	if len(s.providers) == 0 || s.runAsync == nil {
		return
	}
	s.runAsync(func() {
		_, _ = s.SearchAndDownloadBest(context.Background(), libraryItemID, nil)
	})
}

// SearchAndDownloadBest searches all providers for libraryItemID and, if no
// subtitle file already exists, downloads the highest-scored candidate.
//
// It is a no-op (returning a zero UploadResult, nil error) when subtitles
// already exist or no candidates are found. When the best candidate's
// download fails, it falls through to the next-best candidate in score
// order; only if every candidate fails is the last error returned.
func (s *Service) SearchAndDownloadBest(ctx context.Context, libraryItemID int64, languages []string) (UploadResult, error) {
	if _, err := s.SearchCandidates(ctx, libraryItemID, languages); err != nil {
		return UploadResult{}, err
	}
	existing, err := s.repo.ListSubtitleFiles(ctx, libraryItemID)
	if err != nil {
		return UploadResult{}, err
	}
	if len(existing) > 0 {
		return UploadResult{}, nil
	}
	candidates, err := s.repo.ListSubtitleCandidates(ctx, libraryItemID)
	if err != nil {
		return UploadResult{}, err
	}
	if len(candidates) == 0 {
		return UploadResult{}, nil
	}
	var lastErr error
	for _, candidate := range candidates {
		result, err := s.DownloadCandidate(ctx, candidate.ID)
		if err == nil {
			return result, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return UploadResult{}, lastErr
	}
	return UploadResult{}, nil
}

// DownloadCandidate downloads a previously-searched candidate by ID,
// extracts a usable subtitle file from it (unpacking a zip archive if
// necessary), and publishes it alongside the library item's published media.
//
// Every failure path (download, extraction, publication) increments
// metrics.M.SubtitleFailures; a successful publish increments
// metrics.M.SubtitleDownloads.
//
// Errors:
//   - ErrSubtitleCandidateMiss: the candidate's provider is not registered
//     with this Service (e.g. it was disabled after the candidate was found).
func (s *Service) DownloadCandidate(ctx context.Context, candidateID int64) (UploadResult, error) {
	candidate, err := s.repo.GetSubtitleCandidate(ctx, candidateID)
	if err != nil {
		return UploadResult{}, err
	}
	provider := s.providers[strings.ToLower(strings.TrimSpace(candidate.Provider))]
	if provider == nil {
		return UploadResult{}, ErrSubtitleCandidateMiss
	}
	fileName, body, err := provider.Download(ctx, candidate.DownloadURL)
	if err != nil {
		metrics.M.SubtitleFailures.Add(1)
		return UploadResult{}, err
	}
	ext, body, err := extractDownloadedSubtitle(fileName, candidate, body)
	if err != nil {
		metrics.M.SubtitleFailures.Add(1)
		return UploadResult{}, err
	}
	result, err := s.publishSubtitleBody(ctx, candidate.LibraryItemID, candidate.Provider, candidate.Language, ext, body)
	if err != nil {
		metrics.M.SubtitleFailures.Add(1)
		return UploadResult{}, err
	}
	metrics.M.SubtitleDownloads.Add(1)
	return result, nil
}

// DeleteSubtitle removes a subtitle's database record and its published
// files from disk. A file already missing from disk (os.ErrNotExist) is
// tolerated since the DB record must still be cleared.
func (s *Service) DeleteSubtitle(ctx context.Context, subtitleID int64) error {
	group, err := s.repo.DeleteSubtitleFile(ctx, subtitleID)
	if err != nil {
		return err
	}
	for _, path := range group.Paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// DeleteAllForItem removes every subtitle file (across all providers and
// languages) for a single library item — used by the bulk-delete action on
// the library-wide subtitle manager page.
func (s *Service) DeleteAllForItem(ctx context.Context, libraryItemID int64) error {
	files, err := s.repo.ListSubtitleFiles(ctx, libraryItemID)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := s.DeleteSubtitle(ctx, file.ID); err != nil {
			return err
		}
	}
	return nil
}

// ListLibraryState returns a filtered, paginated view of subtitle state
// across the library, for the library-wide subtitle manager page.
func (s *Service) ListLibraryState(ctx context.Context, filter database.SubtitleLibraryFilter) (database.SubtitleLibraryPage, error) {
	return s.repo.ListSubtitleLibrary(ctx, filter)
}

// RepublishStoredSubtitles rewrites the on-disk subtitle files for
// libraryItemID from their stored content, one file per publication path
// (e.g. after a symlink was recreated or the media was re-published under a
// new name). It groups existing subtitle rows by (provider, language) and
// republishes each group from the first group member still readable on
// disk.
//
// Errors:
//   - ErrNoPublishedMedia: the library item has no current publication paths
//     to republish subtitles alongside.
func (s *Service) RepublishStoredSubtitles(ctx context.Context, libraryItemID int64) error {
	items, err := s.repo.ListSubtitleFiles(ctx, libraryItemID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}

	publicationPaths, err := s.repo.ListPublicationPathsForLibraryItem(ctx, libraryItemID)
	if err != nil {
		return err
	}
	if len(publicationPaths) == 0 {
		return ErrNoPublishedMedia
	}

	type subtitleGroup struct {
		provider string
		language string
		source   []database.SubtitleFileSummary
	}
	groups := make(map[string]*subtitleGroup)
	for _, item := range items {
		key := item.Provider + "\x00" + item.Language
		group := groups[key]
		if group == nil {
			group = &subtitleGroup{
				provider: item.Provider,
				language: item.Language,
			}
			groups[key] = group
		}
		group.source = append(group.source, item)
	}

	for _, group := range groups {
		sourcePath, body, ext, err := firstReadableSubtitle(group.source)
		if err != nil {
			// Source files are gone from disk (e.g. media directory was cleaned up).
			// Remove the stale DB rows so they don't reappear on future rebuilds.
			if errors.Is(err, ErrSubtitleEmpty) {
				for _, item := range group.source {
					s.repo.DeleteSubtitleFile(ctx, item.ID) //nolint:errcheck
				}
				continue
			}
			return err
		}
		createdPaths := make([]string, 0, len(publicationPaths))
		for _, publicationPath := range publicationPaths {
			subtitlePath := subtitlePathForPublication(publicationPath, group.language, ext)
			if err := writeFileAtomic(subtitlePath, body); err != nil {
				return err
			}
			createdPaths = append(createdPaths, subtitlePath)
		}
		for _, item := range group.source {
			if item.Path == sourcePath {
				continue
			}
			if !containsPath(createdPaths, item.Path) {
				_ = os.Remove(item.Path)
			}
		}
		if err := s.repo.ReplaceSubtitleFiles(ctx, libraryItemID, group.provider, group.language, createdPaths); err != nil {
			return err
		}
	}
	return nil
}

// UploadSubtitle publishes a manually supplied subtitle file for
// libraryItemID under the given language, capping the read at
// maxUploadBytes+1 to detect and reject oversized uploads without buffering
// unbounded input.
//
// Errors:
//   - ErrLanguageRequired: language is empty after normalization.
//   - ErrSubtitleEmpty: the uploaded body is empty.
//   - ErrSubtitleTooLarge: the uploaded body exceeds maxUploadBytes.
//   - ErrUnsupportedSubtitle: fileName's extension is not .srt or .vtt.
//   - ErrNoPublishedMedia: the library item has no current publication paths.
func (s *Service) UploadSubtitle(ctx context.Context, libraryItemID int64, language, fileName string, src io.Reader) (UploadResult, error) {
	language = normalizeLanguage(language)
	if language == "" {
		return UploadResult{}, ErrLanguageRequired
	}

	body, err := io.ReadAll(io.LimitReader(src, maxUploadBytes+1))
	if err != nil {
		return UploadResult{}, err
	}
	if len(body) == 0 {
		return UploadResult{}, ErrSubtitleEmpty
	}
	if len(body) > maxUploadBytes {
		return UploadResult{}, ErrSubtitleTooLarge
	}

	ext := strings.ToLower(filepath.Ext(fileName))
	if ext == "" {
		ext = ".srt"
	}
	if ext != ".srt" && ext != ".vtt" {
		return UploadResult{}, ErrUnsupportedSubtitle
	}

	return s.publishSubtitleBody(ctx, libraryItemID, "manual", language, ext, body)
}

// publishSubtitleBody writes body to disk alongside every current
// publication path for libraryItemID and records the resulting paths,
// shared by both the manual-upload (UploadSubtitle) and provider-download
// (DownloadCandidate) paths so a subtitle always lands next to its media
// under the same naming convention.
func (s *Service) publishSubtitleBody(ctx context.Context, libraryItemID int64, provider, language, ext string, body []byte) (UploadResult, error) {
	publicationPaths, err := s.repo.ListPublicationPathsForLibraryItem(ctx, libraryItemID)
	if err != nil {
		return UploadResult{}, err
	}
	if len(publicationPaths) == 0 {
		return UploadResult{}, ErrNoPublishedMedia
	}

	createdPaths := make([]string, 0, len(publicationPaths))
	for _, publicationPath := range publicationPaths {
		subtitlePath := subtitlePathForPublication(publicationPath, language, ext)
		if err := writeFileAtomic(subtitlePath, body); err != nil {
			return UploadResult{}, err
		}
		createdPaths = append(createdPaths, subtitlePath)
	}

	if err := s.repo.ReplaceSubtitleFiles(ctx, libraryItemID, provider, language, createdPaths); err != nil {
		return UploadResult{}, err
	}

	return UploadResult{
		LibraryItemID: libraryItemID,
		Language:      language,
		Provider:      provider,
		CreatedPaths:  createdPaths,
	}, nil
}

// subtitlePathForPublication derives the sidecar subtitle path for a
// published media file, following the "name.language.ext" convention that
// media players (e.g. Plex) use to auto-detect subtitle tracks.
func subtitlePathForPublication(publicationPath, language, ext string) string {
	base := strings.TrimSuffix(filepath.Base(publicationPath), filepath.Ext(publicationPath))
	return filepath.Join(filepath.Dir(publicationPath), fmt.Sprintf("%s.%s%s", base, language, ext))
}

// normalizeLanguage lower-cases and trims language, returning "" if the
// result contains anything other than lowercase letters, digits, or '-'.
func normalizeLanguage(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	// Reject anything that isn't a plain language code — this value is
	// interpolated directly into a filesystem path in
	// subtitlePathForPublication, so path separators or ".." must not survive.
	for _, r := range language {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return ""
		}
	}
	return language
}

// normalizeLanguages normalizes each value via normalizeLanguage, dropping
// blanks and duplicates while preserving first-seen order.
func normalizeLanguages(values []string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, value := range values {
		normalized := normalizeLanguage(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func sortedProviderNames(providers map[string]Provider) []string {
	out := make([]string, 0, len(providers))
	for name := range providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// requestedLanguages normalizes values and falls back to the service's
// configured default languages when the caller did not specify any.
func (s *Service) requestedLanguages(values []string) []string {
	out := normalizeLanguages(values)
	if len(out) > 0 {
		return out
	}
	return append([]string(nil), s.defaultLanguages...)
}

// firstReadableSubtitle returns the path, content, and extension of the
// first item that is still a non-empty, readable .srt/.vtt file on disk.
// Items that fail to read (e.g. deleted out from under the DB record) are
// skipped rather than treated as fatal, since a later group member may still
// be readable.
//
// Errors:
//   - ErrSubtitleEmpty: no item in items is readable.
func firstReadableSubtitle(items []database.SubtitleFileSummary) (string, []byte, string, error) {
	for _, item := range items {
		ext := strings.ToLower(filepath.Ext(item.Path))
		if ext != ".srt" && ext != ".vtt" {
			continue
		}
		body, err := os.ReadFile(item.Path)
		if err != nil {
			continue
		}
		if len(body) == 0 {
			continue
		}
		return item.Path, body, ext, nil
	}
	return "", nil, "", ErrSubtitleEmpty
}

func containsPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}

// writeFileAtomic writes body to path via a temp-file-then-rename so
// concurrent readers (or a crash mid-write) never observe a partially
// written subtitle file.
func writeFileAtomic(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// extractDownloadedSubtitle resolves body into a usable subtitle extension
// and content, unpacking a zip archive when the resolved extension is
// ".zip". The extension is taken from fileName when present, falling back
// to the candidate's declared format (e.g. when the provider's URL carries
// no file extension).
//
// Errors:
//   - ErrUnsupportedSubtitle: the resolved extension is not srt, vtt, or zip.
func extractDownloadedSubtitle(fileName string, candidate database.SubtitleCandidateSummary, body []byte) (string, []byte, error) {
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext == "" {
		ext = "." + strings.TrimPrefix(strings.ToLower(candidate.Format), ".")
	}
	switch ext {
	case ".srt", ".vtt":
		return ext, body, nil
	case ".zip":
		entry, entryBody, err := extractSubtitleFromZip(body, candidate)
		if err != nil {
			return "", nil, err
		}
		return strings.ToLower(filepath.Ext(entry)), entryBody, nil
	default:
		return "", nil, ErrUnsupportedSubtitle
	}
}

// extractSubtitleFromZip picks the best-matching .srt/.vtt entry from a
// subtitle archive using scoreArchiveEntry, since a single archive may
// bundle subtitles for multiple releases, languages, or formats. Entries
// larger than maxUploadBytes or empty are skipped.
//
// Errors:
//   - ErrSubtitleArchiveEmpty: the archive contains no usable entry.
func extractSubtitleFromZip(body []byte, candidate database.SubtitleCandidateSummary) (string, []byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", nil, err
	}
	bestName := ""
	var bestBody []byte
	bestScore := -1 << 30
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(file.Name))
		if ext != ".srt" && ext != ".vtt" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return "", nil, err
		}
		entryBody, readErr := io.ReadAll(io.LimitReader(rc, maxUploadBytes+1))
		_ = rc.Close()
		if readErr != nil {
			return "", nil, readErr
		}
		if len(entryBody) == 0 || len(entryBody) > maxUploadBytes {
			continue
		}
		score := scoreArchiveEntry(file.Name, candidate)
		if score > bestScore {
			bestScore = score
			bestName = file.Name
			bestBody = entryBody
		}
	}
	if bestName == "" {
		return "", nil, ErrSubtitleArchiveEmpty
	}
	return bestName, bestBody, nil
}

// scoreArchiveEntry ranks a zip entry's filename against the candidate it
// was downloaded for, favoring entries whose name references the same
// language, release, or title, and preferring .srt over .vtt as a tiebreak.
// Higher is better; callers compare scores across entries in the same
// archive only.
func scoreArchiveEntry(name string, candidate database.SubtitleCandidateSummary) int {
	score := 0
	text := normalizeSubtitleText(name)
	if lang := normalizeLanguage(candidate.Language); lang != "" && strings.Contains(text, lang) {
		score += 20
	}
	release := normalizeSubtitleText(candidate.ReleaseName)
	if release != "" && strings.Contains(text, release) {
		score += 25
	}
	title := normalizeSubtitleText(candidate.Title)
	if title != "" && strings.Contains(text, title) {
		score += 15
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".srt":
		score += 8
	case ".vtt":
		score += 4
	}
	return score
}

// scoreCandidate ranks a provider search result against the search input so
// SearchCandidates can sort each provider's candidates best-first. The score
// rewards an exact preferred-language match (weighted by the caller's
// language preference order), season/episode agreement for TV content,
// title/year token overlap, and srt over vtt; it penalizes hearing-impaired
// tracks and season/episode mismatches. The base score and bias terms are
// arbitrary but stable within a single call, so only relative ordering
// matters.
func scoreCandidate(provider string, input database.SubtitleSearchInput, languages []string, item ProviderCandidate) int {
	score := 100
	score += providerScoreBias(provider)
	language := normalizeLanguage(item.Language)
	for index, preferred := range languages {
		if preferred == language {
			score += 40 - index*5
			break
		}
	}
	if item.HearingImpaired {
		score -= 5
	}
	text := normalizeSubtitleText(item.ReleaseName + " " + item.Title)
	targetTitle := normalizeSubtitleText(subtitleutil.SearchTitle(input))
	switch strings.ToLower(strings.TrimSpace(input.MediaType)) {
	case "episode", "tv":
		if input.SeasonNumber > 0 && item.SeasonNumber == input.SeasonNumber {
			score += 15
		} else if input.SeasonNumber > 0 && item.SeasonNumber > 0 {
			score -= 20
		}
		if input.EpisodeNumber > 0 && item.EpisodeNumber == input.EpisodeNumber {
			score += 20
		} else if input.EpisodeNumber > 0 && item.EpisodeNumber == 0 {
			score -= 15
		} else if input.EpisodeNumber > 0 && item.EpisodeNumber > 0 {
			score -= 25
		}
		if seasonEpisodeTokenMatch(text, input.SeasonNumber, input.EpisodeNumber) {
			score += 18
		}
	case "movie":
		score += 10
	}
	if targetTitle != "" && strings.Contains(text, targetTitle) {
		score += 18
	}
	if year := subtitleutil.SearchYear(input); year > 0 && strings.Contains(text, strconv.Itoa(year)) {
		score += 10
	}
	switch strings.ToLower(strings.TrimSpace(item.Format)) {
	case "srt":
		score += 8
	case "vtt":
		score += 4
	}
	return score
}

// providerScoreBias applies a fixed per-provider quality bias in
// scoreCandidate, reflecting observed relative reliability (opensubtitles
// over subdl) when other ranking factors are equal.
func providerScoreBias(provider string) int {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "opensubtitles":
		return 6
	case "subdl":
		return 3
	default:
		return 0
	}
}

var subtitleTextPunct = regexp.MustCompile(`[^a-z0-9]+`)

func normalizeSubtitleText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	value = subtitleTextPunct.ReplaceAllString(value, " ")
	return strings.Join(strings.Fields(value), " ")
}

// seasonEpisodeTokenMatch reports whether text (a normalized release
// name/title) contains a common season/episode notation (S01E02, 1x02, or
// "season 1 episode 2") for the given season and episode, used as an
// additional scoreCandidate signal beyond the structured SeasonNumber/
// EpisodeNumber fields, which some providers leave unset.
func seasonEpisodeTokenMatch(text string, season, episode int) bool {
	if season <= 0 || episode <= 0 || text == "" {
		return false
	}
	sTokens := []string{
		fmt.Sprintf("s%02de%02d", season, episode),
		fmt.Sprintf("%dx%02d", season, episode),
		fmt.Sprintf("season %d episode %d", season, episode),
	}
	for _, token := range sTokens {
		if strings.Contains(text, token) {
			return true
		}
	}
	return false
}

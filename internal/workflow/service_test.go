package workflow

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/hydra"
	"github.com/drakkar-media/drakkar/internal/library"
	"github.com/drakkar-media/drakkar/internal/seerr"
	"github.com/drakkar-media/drakkar/internal/tmdb"
	"github.com/drakkar-media/drakkar/internal/tvdb"
)

type repoStub struct {
	persistedDispatchedURLs map[string]bool
	requests                []database.MediaRequestSummary
	searchInput             database.LibrarySearchInput
	searchInputByItem       map[int64]database.LibrarySearchInput
	history                 map[string]database.CandidateHistory
	defaultProfile          database.QualityProfile
	itemProfile             *database.QualityProfile
	conflict                string
	searchApplied           []database.SearchCandidateRecord
	searchFailed            []string
	pending                 []database.PendingLibrarySearchTarget
	backlog                 int
	selectedBacklog         int
	failedQueues            []database.FailedQueueRetryTarget
	selectedQueues          []database.SelectedQueueRetryTarget
	upgradable              []int64
	movieCalls              int
	tvCalls                 int
	fetching                int64
	imported                database.ImportedNZB
	indexed                 int64
	selected                database.ReleaseSummary
	selectedByID            map[int64]database.ReleaseSummary
	promoted                *database.ReleaseSummary
	alternative             *database.ReleaseSummary
	next                    *database.ReleaseSummary
	failed                  []string
	rejected                []string
	retryTarget             database.QueueRetryTarget
	skipped                 []int64
	requeued                []int64
	stored                  database.StoredNZBDocument
	restoredGroup           []int64
	calibrateFn             func(context.Context, int64) error
	movieMeta               struct {
		libraryItemID int64
		tmdbID        int64
		title         string
		year          int
		imdbID        string
	}
	episodeMeta struct {
		libraryItemID int64
		tmdbID        int64
		show          string
		year          int
		imdbID        string
		episodeTitle  string
	}
	missingShows        []database.ShowWithMissingEpisodes
	batchCreatedIDs     []int64
	batchCreatedBatches int
}

func (r *repoStub) ListMediaRequests(ctx context.Context) ([]database.MediaRequestSummary, error) {
	return r.requests, nil
}
func (r *repoStub) UpsertMovieRequest(ctx context.Context, externalID string, tmdbID int64, title string, year int) (int64, bool, error) {
	r.movieCalls++
	return 11, true, nil
}
func (r *repoStub) UpsertEpisodeRequest(ctx context.Context, externalID string, tvdbID, tmdbID int64, show string, year, season, episode int, episodeTitle string) (int64, bool, error) {
	r.tvCalls++
	return 12, true, nil
}
func (r *repoStub) EnrichMovieMetadata(ctx context.Context, libraryItemID, tmdbID int64, title string, year int, imdbID string) error {
	r.movieMeta = struct {
		libraryItemID int64
		tmdbID        int64
		title         string
		year          int
		imdbID        string
	}{libraryItemID, tmdbID, title, year, imdbID}
	return nil
}
func (r *repoStub) EnrichEpisodeMetadata(ctx context.Context, libraryItemID, tmdbID int64, show string, year int, imdbID, episodeTitle string) error {
	r.episodeMeta = struct {
		libraryItemID int64
		tmdbID        int64
		show          string
		year          int
		imdbID        string
		episodeTitle  string
	}{libraryItemID, tmdbID, show, year, imdbID, episodeTitle}
	return nil
}

func (r *repoStub) EnrichMovieFull(_ context.Context, _ int64, e database.MovieEnrichment) error {
	r.movieMeta = struct {
		libraryItemID int64
		tmdbID        int64
		title         string
		year          int
		imdbID        string
	}{0, e.TMDBID, e.Title, e.Year, e.IMDbID}
	return nil
}

func (r *repoStub) EnrichTVFull(_ context.Context, _ int64, episodeTitle string, e database.TVShowEnrichment) error {
	r.episodeMeta = struct {
		libraryItemID int64
		tmdbID        int64
		show          string
		year          int
		imdbID        string
		episodeTitle  string
	}{0, e.TMDBID, e.ShowTitle, e.Year, e.IMDbID, episodeTitle}
	return nil
}
func (r *repoStub) DetectMovieSearchConflict(ctx context.Context, libraryItemID int64) (string, error) {
	return r.conflict, nil
}
func (r *repoStub) GetDefaultQualityProfile(ctx context.Context) (database.QualityProfile, error) {
	return r.defaultProfile, nil
}
func (r *repoStub) GetLibrarySearchInput(ctx context.Context, libraryItemID int64) (database.LibrarySearchInput, error) {
	if r.searchInputByItem != nil {
		if input, ok := r.searchInputByItem[libraryItemID]; ok {
			return input, nil
		}
	}
	return r.searchInput, nil
}
func (r *repoStub) LookupCandidateHistory(ctx context.Context, libraryItemID int64) (map[string]database.CandidateHistory, error) {
	return r.history, nil
}
func (r *repoStub) ListPendingLibrarySearchTargets(ctx context.Context) ([]database.PendingLibrarySearchTarget, error) {
	return r.pending, nil
}
func (r *repoStub) CountActiveSearchBacklog(ctx context.Context) (int, error) {
	return r.backlog, nil
}
func (r *repoStub) CountSelectedQueueBacklog(ctx context.Context) (int, error) {
	return r.selectedBacklog, nil
}
func (r *repoStub) GetShowWithMissingEpisodes(_ context.Context, tvShowID int64) (*database.ShowWithMissingEpisodes, error) {
	for _, show := range r.missingShows {
		if show.TVShowID == tvShowID {
			item := show
			return &item, nil
		}
	}
	return nil, nil
}
func (r *repoStub) ListPendingTVShowLibraryItemIDs(_ context.Context, _ int64) ([]int64, error) {
	return append([]int64(nil), r.batchCreatedIDs...), nil
}
func (r *repoStub) ListFailedQueueRetryTargets(ctx context.Context, limit int) ([]database.FailedQueueRetryTarget, error) {
	return r.failedQueues, nil
}
func (r *repoStub) ListSelectedQueueRetryTargets(ctx context.Context, limit int) ([]database.SelectedQueueRetryTarget, error) {
	return r.selectedQueues, nil
}
func (r *repoStub) GetQueueRetryTarget(ctx context.Context, queueItemID int64) (database.QueueRetryTarget, error) {
	return r.retryTarget, nil
}
func (r *repoStub) BlocklistQueueSelectedRelease(ctx context.Context, queueItemID int64, reason string, ttlDays int) error {
	r.failed = append(r.failed, "blocklist:"+reason)
	return nil
}
func (r *repoStub) ClearQueueSelectedRelease(ctx context.Context, queueItemID int64) error {
	r.skipped = append(r.skipped, queueItemID)
	return nil
}
func (r *repoStub) RequeueSelectedRelease(ctx context.Context, queueItemID int64) error {
	r.requeued = append(r.requeued, queueItemID)
	return nil
}
func (r *repoStub) InsertManualReleaseCandidate(ctx context.Context, libraryItemID int64, title, externalURL, indexerName, resolution string, sizeBytes int64, score int) (int64, error) {
	return 1, nil
}

func (r *repoStub) ReplaceSearchCandidates(ctx context.Context, libraryItemID int64, candidates []database.SearchCandidateRecord) (*int64, error) {
	r.searchApplied = candidates
	if len(candidates) == 0 || candidates[0].Rejected {
		return nil, nil
	}
	value := int64(88)
	r.selected = database.ReleaseSummary{
		SelectedReleaseID:  value,
		ReleaseCandidateID: 1,
		LibraryItemID:      libraryItemID,
		Title:              candidates[0].Title,
		ExternalURL:        candidates[0].ExternalURL,
	}
	return &value, nil
}

func (r *repoStub) PromoteExistingCandidate(ctx context.Context, libraryItemID int64) (*int64, error) {
	return nil, nil
}

func (r *repoStub) MarkLibrarySearchFailed(ctx context.Context, libraryItemID int64, reason string) error {
	r.searchFailed = append(r.searchFailed, fmt.Sprintf("%d:%s", libraryItemID, reason))
	return nil
}

func (r *repoStub) GetSelectedReleaseSummary(ctx context.Context, selectedReleaseID int64) (database.ReleaseSummary, error) {
	if r.selectedByID != nil {
		if item, ok := r.selectedByID[selectedReleaseID]; ok {
			return item, nil
		}
	}
	return r.selected, nil
}

func (r *repoStub) GetLatestSelectedReleaseSummaryByLibraryItem(_ context.Context, libraryItemID int64) (*database.ReleaseSummary, error) {
	if r.selected.LibraryItemID == libraryItemID && r.selected.SelectedReleaseID != 0 {
		item := r.selected
		return &item, nil
	}
	return nil, nil
}

func (r *repoStub) GetStoredNZBDocument(ctx context.Context, selectedReleaseID int64) (database.StoredNZBDocument, error) {
	return r.stored, nil
}

func (r *repoStub) PromoteBestRetryCandidate(ctx context.Context, libraryItemID int64) (*database.ReleaseSummary, error) {
	if r.promoted == nil {
		return nil, nil
	}
	next := *r.promoted
	r.selected = next
	return &next, nil
}

func (r *repoStub) PromoteAlternativeRetryCandidate(ctx context.Context, libraryItemID int64, excludeReleaseCandidateID int64) (*database.ReleaseSummary, error) {
	if r.alternative == nil {
		return nil, nil
	}
	next := *r.alternative
	r.selected = next
	return &next, nil
}

func (r *repoStub) SelectReleaseCandidate(ctx context.Context, releaseCandidateID int64) (*database.ReleaseSummary, error) {
	r.selected = database.ReleaseSummary{
		SelectedReleaseID:  101,
		ReleaseCandidateID: releaseCandidateID,
		LibraryItemID:      42,
		Title:              "Manual.Select.Release",
		ExternalURL:        "http://example/manual.nzb",
	}
	return &r.selected, nil
}

func (r *repoStub) RejectReleaseCandidate(ctx context.Context, releaseCandidateID int64, reason string) (*database.ReleaseSummary, error) {
	r.rejected = append(r.rejected, reason)
	if r.next == nil {
		return nil, nil
	}
	next := *r.next
	r.selected = next
	r.next = nil
	return &next, nil
}

func (r *repoStub) RestoreReleaseCandidate(ctx context.Context, releaseCandidateID int64) error {
	r.rejected = append(r.rejected, "restored")
	return nil
}

func (r *repoStub) RestoreRejectedReleaseCandidates(ctx context.Context, libraryItemID int64) (database.RejectedReleaseRestoreResult, error) {
	r.restoredGroup = append(r.restoredGroup, libraryItemID)
	return database.RejectedReleaseRestoreResult{LibraryItemID: libraryItemID, Restored: 2}, nil
}

func (r *repoStub) SkipReleaseCandidate(ctx context.Context, releaseCandidateID int64) (*database.ReleaseSummary, error) {
	r.skipped = append(r.skipped, releaseCandidateID)
	if r.next == nil {
		return nil, nil
	}
	next := *r.next
	r.selected = next
	r.next = nil
	return &next, nil
}

func (r *repoStub) MarkSelectedReleaseFetching(ctx context.Context, selectedReleaseID int64) error {
	r.fetching = selectedReleaseID
	return nil
}

func (r *repoStub) RecentlyDispatchedURLPersisted(ctx context.Context, rawURL string, cooldown time.Duration) (bool, error) {
	return r.persistedDispatchedURLs[rawURL], nil
}

func (r *repoStub) MarkURLDispatchedPersisted(ctx context.Context, rawURL string) error {
	if r.persistedDispatchedURLs == nil {
		r.persistedDispatchedURLs = make(map[string]bool)
	}
	r.persistedDispatchedURLs[rawURL] = true
	return nil
}

func (r *repoStub) StoreRawNZBDocument(ctx context.Context, selectedReleaseID int64, fileName string, xml []byte, externalURL string) error {
	return nil
}

func (r *repoStub) ImportSelectedReleaseNZB(ctx context.Context, selectedReleaseID int64, imported database.ImportedNZB) (database.QueueSnapshot, error) {
	r.imported = imported
	// Simulate real import creating virtual files so VirtualFileCount==0 fast-fail doesn't trigger.
	// Only update r.selected when selectedByID has no explicit override for this release.
	if _, hasOverride := r.selectedByID[selectedReleaseID]; !hasOverride {
		r.selected.VirtualFileCount = 1
	}
	return database.QueueSnapshot{
		QueueItemID:     99,
		LibraryItemID:   42,
		LibraryTitle:    "Dune",
		State:           database.QueueIndexing,
		SelectedRelease: &selectedReleaseID,
		NZBDocumentID:   func() *int64 { value := int64(123); return &value }(),
	}, nil
}

func (r *repoStub) SetImportedNZBIndexed(ctx context.Context, queueItemID int64) error {
	r.indexed = queueItemID
	return nil
}

func (r *repoStub) FailSelectedReleaseAndPromoteNext(ctx context.Context, selectedReleaseID int64, reason string) (*database.ReleaseSummary, error) {
	r.failed = append(r.failed, reason)
	if r.next == nil {
		return nil, nil
	}
	next := *r.next
	r.selected = next
	r.next = nil
	return &next, nil
}

func (r *repoStub) ShouldAttemptSeasonPack(_ context.Context, _ int64, _ int) (bool, error) {
	return false, nil // tests skip season pack logic by default
}

func (r *repoStub) RecordSeasonPackAttempt(_ context.Context, _ int64, _ int, _ string) error {
	return nil
}

func (r *repoStub) ClearFailedQueueItems(_ context.Context) (int, error) { return 0, nil }
func (r *repoStub) ListMetadataBackfillTargets(_ context.Context) ([]database.MetadataBackfillTarget, error) {
	return nil, nil
}
func (r *repoStub) ListShowsWithMissingEpisodes(_ context.Context) ([]database.ShowWithMissingEpisodes, error) {
	return r.missingShows, nil
}
func (r *repoStub) EnsureEpisodeLibraryItem(_ context.Context, _ int64, _ string, _, _ int, _, _ string) (bool, error) {
	return false, nil
}
func (r *repoStub) EnsureEpisodeLibraryItemsBatch(_ context.Context, _ int64, _ string, episodes []database.MissingEpisodeBatchInput) ([]int64, error) {
	r.batchCreatedBatches++
	if len(r.batchCreatedIDs) == 0 {
		return nil, nil
	}
	out := append([]int64(nil), r.batchCreatedIDs...)
	return out, nil
}
func (r *repoStub) ListCustomFormats(_ context.Context) ([]database.CustomFormat, error) {
	return nil, nil
}
func (r *repoStub) GetLibraryItemQualityProfile(_ context.Context, _ int64) (*database.QualityProfile, error) {
	return r.itemProfile, nil
}
func (r *repoStub) GetQualityProfileByName(_ context.Context, _ string) (database.QualityProfile, error) {
	return database.QualityProfile{}, nil
}
func (r *repoStub) ListQualityDefinitions(_ context.Context) ([]database.QualityDefinition, error) {
	return nil, nil
}
func (r *repoStub) ListUpgradableLibraryItems(_ context.Context) ([]int64, error) {
	return r.upgradable, nil
}
func (r *repoStub) CreateImportedNZB(_ context.Context, _ database.ImportedNZB) (database.QueueSnapshot, error) {
	return database.QueueSnapshot{}, nil
}
func (r *repoStub) ListSabQueueItems(_ context.Context, _ string, _, _ int) ([]database.SabQueueItem, int, error) {
	return nil, 0, nil
}
func (r *repoStub) ListSabHistoryItems(_ context.Context, _ string, _, _ int) ([]database.SabHistoryItem, int, error) {
	return nil, 0, nil
}
func (r *repoStub) DismissSabItems(_ context.Context, _ []int64) error { return nil }
func (r *repoStub) DeleteSymlinkPublicationsForLibraryItem(_ context.Context, _ int64) ([]string, error) {
	return nil, nil
}
func (r *repoStub) ResetLibraryItemState(_ context.Context, _ int64) error { return nil }
func (r *repoStub) ListUnrecoverableLibraryItems(_ context.Context) ([]int64, error) {
	return nil, nil
}
func (r *repoStub) ListMovieTmdbIDs(_ context.Context) ([]int64, error) {
	return nil, nil
}
func (r *repoStub) ListTVShowTmdbIDsWithSeasons(_ context.Context) ([]database.TVShowSeerrInfo, error) {
	return nil, nil
}
func (r *repoStub) ListReleaseBlockRules(_ context.Context) ([]database.ReleaseBlockRule, error) {
	return nil, nil
}

func (r *repoStub) LoadIndexerPolicyMap(_ context.Context) (map[string]int, error) {
	return nil, nil
}
func (r *repoStub) ListPublishedDirectNzbSegments(_ context.Context) ([]database.PublishedDirectNzbSegment, error) {
	return nil, nil
}
func (r *repoStub) TouchQueueItemSearched(_ context.Context, _ int64) error {
	return nil
}
func (r *repoStub) CalibrateNZBOffsets(ctx context.Context, id int64) error {
	if r.calibrateFn != nil {
		return r.calibrateFn(ctx, id)
	}
	return nil
}

type seerrStub struct {
	requests []seerr.Request
}

func (s seerrStub) PendingRequests(ctx context.Context) ([]seerr.Request, error) {
	return s.requests, nil
}
func (s seerrStub) CreateRequest(_ context.Context, _ string, _ int64) error        { return nil }
func (s seerrStub) CreateTVSeasonRequest(_ context.Context, _ int64, _ []int) error { return nil }
func (s seerrStub) CreateTVSeasonRequestNoWait(_ context.Context, _ int64, _ []int) error {
	return nil
}
func (s seerrStub) PartialTVItems(_ context.Context) ([]seerr.PartialTVItem, error) { return nil, nil }

type seasonRequestSeerrStub struct {
	seerrStub
	seasonRequestID int64
	seasonNumbers   []int
}

func (s *seasonRequestSeerrStub) CreateTVSeasonRequest(_ context.Context, tmdbID int64, seasons []int) error {
	s.seasonRequestID = tmdbID
	s.seasonNumbers = append([]int(nil), seasons...)
	return nil
}

func (s *seasonRequestSeerrStub) CreateTVSeasonRequestNoWait(_ context.Context, tmdbID int64, seasons []int) error {
	return s.CreateTVSeasonRequest(context.Background(), tmdbID, seasons)
}

type hydraStub struct {
	results    []hydra.SearchResult
	recent     map[string][]hydra.SearchResult
	recentErr  map[string]error
	byQuery    map[string][]hydra.SearchResult
	seqByQuery map[string][]hydraReply
	errByQuery map[string]error
	queries    *[]string
	requests   *[]hydra.SearchRequest
}

type hydraReply struct {
	results []hydra.SearchResult
	err     error
}

func (h hydraStub) Search(ctx context.Context, request hydra.SearchRequest) ([]hydra.SearchResult, error) {
	query := request.Query
	if query == "" {
		if request.IMDbID != "" {
			query = request.IMDbID
		} else if request.TVDBID > 0 {
			query = fmt.Sprintf("tvdb:%d", request.TVDBID)
		}
	}
	if h.queries != nil {
		*h.queries = append(*h.queries, query)
	}
	if h.requests != nil {
		*h.requests = append(*h.requests, request)
	}
	if h.seqByQuery != nil {
		if replies, ok := h.seqByQuery[query]; ok && len(replies) > 0 {
			reply := replies[0]
			h.seqByQuery[query] = replies[1:]
			return reply.results, reply.err
		}
	}
	if h.errByQuery != nil {
		if err, ok := h.errByQuery[query]; ok {
			return nil, err
		}
	}
	if h.byQuery != nil {
		return h.byQuery[query], nil
	}
	return h.results, nil
}

func (h hydraStub) SearchRecent(ctx context.Context, mediaType string) ([]hydra.SearchResult, error) {
	if h.recentErr != nil {
		if err, ok := h.recentErr[mediaType]; ok {
			return nil, err
		}
	}
	if h.recent != nil {
		return h.recent[mediaType], nil
	}
	return h.results, nil
}

type fetcherStub struct {
	fileName string
	raw      []byte
}

func (f fetcherStub) Fetch(ctx context.Context, rawURL string) (string, []byte, error) {
	return f.fileName, f.raw, nil
}

type tmdbStub struct{}

func (tmdbStub) Enabled() bool { return true }
func (tmdbStub) MovieDetails(ctx context.Context, tmdbID int64) (tmdb.MovieDetails, error) {
	return tmdb.MovieDetails{Title: "Dune", Year: 2021, IMDbID: "tt1160419"}, nil
}
func (tmdbStub) TVDetails(ctx context.Context, tmdbID int64) (tmdb.TVDetails, error) {
	return tmdb.TVDetails{Name: "Loki", Year: 2021, IMDbID: "tt9140554"}, nil
}
func (tmdbStub) TVSeasonNumbers(_ context.Context, _ int64) ([]int, error) { return nil, nil }
func (tmdbStub) TVSeason(_ context.Context, _ int64, _ int) (tmdb.TVSeason, error) {
	return tmdb.TVSeason{}, nil
}

type tmdbDisabledStub struct{}

func (tmdbDisabledStub) Enabled() bool { return false }
func (tmdbDisabledStub) MovieDetails(ctx context.Context, tmdbID int64) (tmdb.MovieDetails, error) {
	return tmdb.MovieDetails{}, nil
}
func (tmdbDisabledStub) TVDetails(ctx context.Context, tmdbID int64) (tmdb.TVDetails, error) {
	return tmdb.TVDetails{}, nil
}
func (tmdbDisabledStub) TVSeasonNumbers(_ context.Context, _ int64) ([]int, error) { return nil, nil }
func (tmdbDisabledStub) TVSeason(_ context.Context, _ int64, _ int) (tmdb.TVSeason, error) {
	return tmdb.TVSeason{}, nil
}

type tvdbStub struct{}

func (tvdbStub) Enabled() bool { return true }
func (tvdbStub) SeriesDetails(ctx context.Context, tvdbID int64) (tvdb.SeriesDetails, error) {
	return tvdb.SeriesDetails{Name: "The Bear", Year: 2022, IMDbID: "tt14452776"}, nil
}

type tmdbFillMissingStub struct{}

func (tmdbFillMissingStub) Enabled() bool { return true }
func (tmdbFillMissingStub) MovieDetails(context.Context, int64) (tmdb.MovieDetails, error) {
	return tmdb.MovieDetails{}, nil
}
func (tmdbFillMissingStub) TVDetails(context.Context, int64) (tmdb.TVDetails, error) {
	return tmdb.TVDetails{}, nil
}
func (tmdbFillMissingStub) TVSeasonNumbers(_ context.Context, _ int64) ([]int, error) {
	return []int{1}, nil
}
func (tmdbFillMissingStub) TVSeason(_ context.Context, _ int64, seasonNumber int) (tmdb.TVSeason, error) {
	return tmdb.TVSeason{
		Episodes: []tmdb.TVEpisode{
			{EpisodeNumber: 1, Name: "Pilot", AirDate: "2024-01-01"},
			{EpisodeNumber: 2, Name: "Second", AirDate: "2024-01-08"},
		},
	}, nil
}

type tmdbFillMissingRecentStub struct{}

func (tmdbFillMissingRecentStub) Enabled() bool { return true }
func (tmdbFillMissingRecentStub) MovieDetails(context.Context, int64) (tmdb.MovieDetails, error) {
	return tmdb.MovieDetails{}, nil
}
func (tmdbFillMissingRecentStub) TVDetails(context.Context, int64) (tmdb.TVDetails, error) {
	return tmdb.TVDetails{}, nil
}
func (tmdbFillMissingRecentStub) TVSeasonNumbers(_ context.Context, _ int64) ([]int, error) {
	return []int{1}, nil
}
func (tmdbFillMissingRecentStub) TVSeason(_ context.Context, _ int64, _ int) (tmdb.TVSeason, error) {
	now := time.Now().UTC()
	return tmdb.TVSeason{
		Episodes: []tmdb.TVEpisode{
			{EpisodeNumber: 1, Name: "Fresh One", AirDate: now.AddDate(0, 0, -1).Format("2006-01-02")},
			{EpisodeNumber: 2, Name: "Fresh Two", AirDate: now.Format("2006-01-02")},
		},
	}, nil
}

func TestSyncRequests(t *testing.T) {
	repo := &repoStub{}
	service := NewService(repo, seerrStub{requests: []seerr.Request{
		{ID: 1, Type: "movie", MediaTitle: "Dune", MediaYear: 2021, TMDBID: 438631},
		{ID: 2, Type: "tv", MediaTitle: "Loki", MediaYear: 2021, TVDBID: 362472, TMDBID: 84958, SeasonNumber: 2, EpisodeNumber: 1},
	}}, hydraStub{})
	service.SetTMDBClient(tmdbStub{})

	result, err := service.SyncRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 2 || repo.movieCalls != 1 || repo.tvCalls != 1 {
		t.Fatalf("unexpected sync result %+v repo=%+v", result, repo)
	}
	// Enrichment for new items runs in a goroutine — give it a moment.
	time.Sleep(50 * time.Millisecond)
	if repo.movieMeta.tmdbID != 438631 || repo.movieMeta.imdbID != "tt1160419" || repo.movieMeta.title != "Dune" {
		t.Fatalf("unexpected movie metadata %+v", repo.movieMeta)
	}
	if repo.episodeMeta.tmdbID != 84958 || repo.episodeMeta.show != "Loki" || repo.episodeMeta.imdbID != "tt9140554" {
		t.Fatalf("unexpected episode metadata %+v", repo.episodeMeta)
	}
}

func TestSyncRequestsTVDBFallback(t *testing.T) {
	repo := &repoStub{}
	service := NewService(repo, seerrStub{requests: []seerr.Request{
		{ID: 3, Type: "tv", MediaTitle: "The Bear", MediaYear: 2022, TVDBID: 412567, SeasonNumber: 1, EpisodeNumber: 1, EpisodeTitle: "System"},
	}}, hydraStub{})
	service.SetTMDBClient(tmdbDisabledStub{})
	service.SetTVDBClient(tvdbStub{})

	result, err := service.SyncRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || repo.tvCalls != 1 {
		t.Fatalf("unexpected sync result %+v repo=%+v", result, repo)
	}
	// Enrichment for new items runs in a goroutine — give it a moment.
	time.Sleep(50 * time.Millisecond)
	if repo.episodeMeta.tmdbID != 0 || repo.episodeMeta.show != "The Bear" || repo.episodeMeta.imdbID != "tt14452776" || repo.episodeMeta.year != 2022 {
		t.Fatalf("unexpected episode metadata %+v", repo.episodeMeta)
	}
}

func TestCreateSeerrSeasonRequest(t *testing.T) {
	seerrClient := &seasonRequestSeerrStub{
		seerrStub: seerrStub{
			requests: []seerr.Request{
				{ID: 2, Type: "tv", MediaTitle: "Loki", MediaYear: 2021, TVDBID: 362472, TMDBID: 84958, SeasonNumber: 2, EpisodeNumber: 1},
			},
		},
	}
	service := NewService(&repoStub{}, seerrClient, hydraStub{})

	result, err := service.CreateSeerrSeasonRequest(context.Background(), 84958, []int{2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Seen != 1 || result.Created != 1 {
		t.Fatalf("unexpected sync result %+v", result)
	}
	if seerrClient.seasonRequestID != 84958 {
		t.Fatalf("unexpected tmdb id %d", seerrClient.seasonRequestID)
	}
	if len(seerrClient.seasonNumbers) != 1 || seerrClient.seasonNumbers[0] != 2 {
		t.Fatalf("unexpected seasons %+v", seerrClient.seasonNumbers)
	}
}

func TestSearchLibrary(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{results: []hydra.SearchResult{
		{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
		{Title: "Other.Movie.2021.720p", Link: "http://example/other", Indexer: "hydra", SizeBytes: 4567, PublishedAt: time.Now()},
	}})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidateCount != 2 || result.SelectedReleaseID == nil {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(repo.searchApplied) != 2 || repo.searchApplied[0].Rejected {
		t.Fatalf("unexpected candidates %+v", repo.searchApplied)
	}
	if repo.fetching != 88 || repo.indexed != 99 || repo.imported.FileName != "dune.nzb" {
		t.Fatalf("unexpected import state fetching=%d indexed=%d imported=%+v", repo.fetching, repo.indexed, repo.imported)
	}
}

func TestSearchLibraryFallsBackToLaterQuery(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			IMDbID:        "tt1160419",
			MovieYear:     2021,
		},
	}
	var queries []string
	service := NewService(repo, seerrStub{}, hydraStub{byQuery: map[string][]hydra.SearchResult{
		"tt1160419": {},
		"Dune 2021": {
			{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
		},
	}, queries: &queries})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil || result.Query != "Dune 2021" {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(queries) < 2 || queries[0] != "tt1160419" || queries[1] != "Dune 2021" {
		t.Fatalf("unexpected queries %+v", queries)
	}
}

func TestSearchLibraryUsesStructuredIMDbSearchRequest(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			IMDbID:        "tt1160419",
			MovieYear:     2021,
		},
	}
	var requests []hydra.SearchRequest
	service := NewService(repo, seerrStub{}, hydraStub{
		byQuery: map[string][]hydra.SearchResult{
			"tt1160419": {
				{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
			},
		},
		requests: &requests,
	})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil {
		t.Fatalf("expected selected release, got %+v", result)
	}
	if len(requests) == 0 || requests[0].IMDbID != "tt1160419" || requests[0].MediaType != "movie" {
		t.Fatalf("expected structured movie request, got %+v", requests)
	}
	if requests[0].Query != "" {
		t.Fatalf("expected id-only lookup to omit free-text query, got %+v", requests[0])
	}
}

func TestSearchLibraryMarksMetadataConflictWithoutHydraCall(t *testing.T) {
	repo := &repoStub{conflict: "metadata_conflict"}
	var requests []hydra.SearchRequest
	hydraClient := hydraStub{requests: &requests}
	service := NewService(repo, nil, hydraClient)

	result, err := service.SearchLibrary(context.Background(), 34)
	if err != nil {
		t.Fatalf("SearchLibrary error = %v", err)
	}
	if result.LibraryItemID != 34 {
		t.Fatalf("expected library item 34, got %+v", result)
	}
	if len(repo.searchFailed) != 1 || repo.searchFailed[0] != "34:metadata_conflict" {
		t.Fatalf("expected metadata_conflict failure, got %#v", repo.searchFailed)
	}
	if len(requests) != 0 {
		t.Fatalf("expected no hydra search, got %#v", requests)
	}
}

// TestSearchLibrarySkipsSearchWithNoTitleToVerifyAgainst guards against the
// cross-show contamination bug found in production: containsNormalized's
// title check trivially accepts ANY candidate when the required title
// tokenizes to zero words (see ranking.titlesWordMatch). A library item
// whose show/episode metadata hasn't finished populating yet (Title and
// ShowTitle both blank) must never reach Hydra/ranking at all -- two real
// shows each had a completely unrelated show's season pack selected and
// fanned out across every sibling episode this way.
func TestSearchLibrarySkipsSearchWithNoTitleToVerifyAgainst(t *testing.T) {
	repo := &repoStub{searchInput: database.LibrarySearchInput{LibraryItemID: 77, MediaType: "episode"}}
	var requests []hydra.SearchRequest
	hydraClient := hydraStub{requests: &requests}
	service := NewService(repo, nil, hydraClient)

	result, err := service.SearchLibrary(context.Background(), 77)
	if err != nil {
		t.Fatalf("SearchLibrary error = %v", err)
	}
	if result.LibraryItemID != 77 {
		t.Fatalf("expected library item 77, got %+v", result)
	}
	if len(repo.searchFailed) != 1 || repo.searchFailed[0] != "77:no title available to verify search results against" {
		t.Fatalf("expected a no-title search-failed marker, got %#v", repo.searchFailed)
	}
	if len(requests) != 0 {
		t.Fatalf("expected no hydra search when there is no title to verify against, got %#v", requests)
	}
}

func TestSearchLibraryFallsBackWhenEarlierQueryOnlyReturnsRejectedCandidates(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			IMDbID:        "tt1160419",
			MovieYear:     2021,
		},
	}
	var queries []string
	service := NewService(repo, seerrStub{}, hydraStub{byQuery: map[string][]hydra.SearchResult{
		"tt1160419": {
			{Title: "Other.Movie.2022.720p", Link: "http://example/bad", Indexer: "hydra", SizeBytes: 555, PublishedAt: time.Now()},
		},
		"Dune 2021": {
			{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/good", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
		},
	}, queries: &queries})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil || result.Query != "Dune 2021" {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(queries) < 2 || queries[0] != "tt1160419" || queries[1] != "Dune 2021" {
		t.Fatalf("unexpected queries %+v", queries)
	}
	if len(repo.searchApplied) != 2 || repo.searchApplied[0].Rejected || repo.searchApplied[0].ExternalURL != "http://example/good" {
		t.Fatalf("unexpected final candidates %+v", repo.searchApplied)
	}
}

func TestSearchLibraryPrefersExactEpisodeOverSeasonPack(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "episode",
			Title:         "Loki S01E02",
			ShowTitle:     "Loki",
			ShowYear:      2021,
			SeasonNumber:  1,
			EpisodeNumber: 2,
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{results: []hydra.SearchResult{
		{Title: "Loki.Season.1.Complete.1080p.WEB-DL", Link: "http://example/pack", Indexer: "hydra", SizeBytes: 5000, PublishedAt: time.Now()},
		{Title: "Loki.S01E02.1080p.WEB-DL", Link: "http://example/exact", Indexer: "hydra", SizeBytes: 1200, PublishedAt: time.Now()},
	}})
	service.fetcher = fetcherStub{
		fileName: "loki.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Loki S01E02.mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.tv</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil {
		t.Fatalf("expected selected release, got %+v", result)
	}
	if len(repo.searchApplied) != 2 {
		t.Fatalf("unexpected candidates %+v", repo.searchApplied)
	}
	if repo.searchApplied[0].Title != "Loki.S01E02.1080p.WEB-DL" {
		t.Fatalf("expected exact episode ranked first, got %+v", repo.searchApplied)
	}
}

// TestHasSeasonPackTokenIgnoresRepackTag guards against a real production bug
// (House of the Dragon S03): "REPACK" is a routine "corrected re-release" tag
// with no relation to season packs, but a bare substring check on "pack"
// matched inside it, so filterToPacksOnly kept an ordinary single-episode
// REPACK release as if it were a season pack — trySeasonPack then selected it
// for unrelated episodes of the season that hadn't aired yet.
func TestHasSeasonPackTokenIgnoresRepackTag(t *testing.T) {
	singleEpisodeRepack := normalizeSearchText("House.of.the.Dragon.S03E02.REPACK.Queens.Landing.1080p.AMZN.WEB-DL.DD+5.1.H.264-playWEB")
	if hasSeasonPackToken(singleEpisodeRepack, 3) {
		t.Fatalf("REPACK single-episode release must not be classified as a season pack: %q", singleEpisodeRepack)
	}

	genuinePack := normalizeSearchText("House.of.the.Dragon.S03.Complete.1080p.AMZN.WEB-DL.DD+5.1.H.264-GRP")
	if !hasSeasonPackToken(genuinePack, 3) {
		t.Fatalf("expected a genuine season pack to still be detected: %q", genuinePack)
	}

	repackedPack := normalizeSearchText("House.of.the.Dragon.S03.REPACK.Complete.1080p.AMZN.WEB-DL.DD+5.1.H.264-GRP")
	if !hasSeasonPackToken(repackedPack, 3) {
		t.Fatalf("expected a repacked season pack (still containing the standalone word 'complete') to be detected: %q", repackedPack)
	}
}

func TestFilterToPacksOnlyDropsRepackSingleEpisode(t *testing.T) {
	candidates := []database.SearchCandidateRecord{
		{Title: "House.of.the.Dragon.S03E02.REPACK.Queens.Landing.1080p.AMZN.WEB-DL.DD+5.1.H.264-playWEB", ExternalURL: "http://example/repack-e02"},
		{Title: "House.of.the.Dragon.S03.Complete.1080p.AMZN.WEB-DL.DD+5.1.H.264-GRP", ExternalURL: "http://example/pack"},
	}
	filtered := filterToPacksOnly(candidates, 3)
	if len(filtered) != 1 || filtered[0].ExternalURL != "http://example/pack" {
		t.Fatalf("expected only the genuine season pack to survive filtering, got %+v", filtered)
	}
}

// TestMergeSearchCandidatesCollapsesSameIndexerSameTitleDifferentURL guards
// against a real production bug: NZB Finder sent a warning threatening account
// termination after repeated duplicate downloads of the same releases. Root
// cause was that the same indexer can mint a fresh external_url/GUID for the
// same underlying release across different search tiers within one search
// pass (e.g. a tier1 ID search and a tier2 title search both matching the
// same posting). Keying merge dedup purely on ExternalURL let that "same"
// release survive into release_candidates as multiple distinct rows, and
// FailSelectedReleaseAndPromoteNext's fallback chain cycled between them on
// failure, re-fetching the identical NZB under a different URL every time.
func TestMergeSearchCandidatesCollapsesSameIndexerSameTitleDifferentURL(t *testing.T) {
	tier1 := []database.SearchCandidateRecord{
		{Title: "NCIS.Los.Angeles.S09E02.Peruanische.Blueten.GERMAN.DUBBED.WebHDRiP.x264-SOF", ExternalURL: "http://hydra/getnzb/api/111", IndexerName: "NZB Finder", SizeBytes: 1_000_000_000, Score: 50},
	}
	tier2 := []database.SearchCandidateRecord{
		{Title: "NCIS.Los.Angeles.S09E02.Peruanische.Blueten.GERMAN.DUBBED.WebHDRiP.x264-SOF", ExternalURL: "http://hydra/getnzb/api/222", IndexerName: "NZB Finder", SizeBytes: 1_000_000_000, Score: 50},
	}
	merged := mergeSearchCandidates(tier1, tier2)
	if len(merged) != 1 {
		t.Fatalf("expected the two same-indexer, same-title hits to collapse into 1 candidate, got %d: %+v", len(merged), merged)
	}
}

// TestMergeSearchCandidatesKeepsSameTitleFromDifferentIndexers preserves the
// documented, intentional behavior: the same release reported by two
// different indexers stays as two separate candidates so the fallback chain
// can still try every available source.
func TestMergeSearchCandidatesKeepsSameTitleFromDifferentIndexers(t *testing.T) {
	existing := []database.SearchCandidateRecord{
		{Title: "Show.S01E01.1080p.WEB-DL", ExternalURL: "http://hydra/a", IndexerName: "NZBGeek", SizeBytes: 1_000_000_000, Score: 50},
	}
	incoming := []database.SearchCandidateRecord{
		{Title: "Show.S01E01.1080p.WEB-DL", ExternalURL: "http://hydra/b", IndexerName: "NZB Finder", SizeBytes: 1_000_000_000, Score: 50},
	}
	merged := mergeSearchCandidates(existing, incoming)
	if len(merged) != 2 {
		t.Fatalf("expected the two different-indexer hits to stay separate, got %d: %+v", len(merged), merged)
	}
}

func TestSearchLibraryPenalizesPreviouslyFailedCandidateAcrossRefresh(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
		history: map[string]database.CandidateHistory{
			"http://example/old": {
				ExternalURL:       "http://example/old",
				FailureCount:      2,
				LastFailureReason: "context deadline exceeded",
			},
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{results: []hydra.SearchResult{
		{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/old", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
		{Title: "Dune.2021.1080p.WEB-DL.x265-NEW", Link: "http://example/new", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
	}})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil {
		t.Fatalf("expected selected release, got %+v", result)
	}
	if len(repo.searchApplied) != 2 {
		t.Fatalf("unexpected candidates %+v", repo.searchApplied)
	}
	if repo.searchApplied[0].ExternalURL != "http://example/new" {
		t.Fatalf("expected clean candidate ranked first, got %+v", repo.searchApplied)
	}
	if repo.searchApplied[1].FailureCount != 2 || repo.searchApplied[1].LastFailureReason != "context deadline exceeded" {
		t.Fatalf("expected history carried forward, got %+v", repo.searchApplied[1])
	}
	if !repo.searchApplied[1].Rejected {
		t.Fatalf("expected previously failed candidate to be durably rejected after 2 hard failures: %+v", repo.searchApplied[1])
	}
}

func TestSearchLibraryRejectsSinglePreviouslyFailedHardCandidate(t *testing.T) {
	// Hard failure (e.g. context deadline) on first attempt → durably rejected (threshold=1).
	// No selectable candidates remain so no release is selected.
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
		history: map[string]database.CandidateHistory{
			"http://example/old": {
				ExternalURL:       "http://example/old",
				FailureCount:      1,
				LastFailureReason: "context deadline exceeded",
			},
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{results: []hydra.SearchResult{
		{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/old", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
	}})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID != nil {
		t.Fatalf("expected no selection when only candidate has a hard failure, got %+v", result)
	}
}

func TestSearchLibrarySoftensArchiveFailurePenalty(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
		history: map[string]database.CandidateHistory{
			"http://example/archive": {
				ExternalURL:       "http://example/archive",
				FailureCount:      2,
				LastFailureReason: "archive_headers_invalid",
			},
			"http://example/hard": {
				ExternalURL:       "http://example/hard",
				FailureCount:      2,
				LastFailureReason: "context deadline exceeded",
			},
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{results: []hydra.SearchResult{
		{Title: "Dune.2021.1080p.WEB-DL.x265-ARCHIVE", Link: "http://example/archive", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
		{Title: "Dune.2021.1080p.WEB-DL.x265-HARD", Link: "http://example/hard", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
	}})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil {
		t.Fatalf("expected selected release, got %+v", result)
	}
	if len(repo.searchApplied) != 2 {
		t.Fatalf("unexpected candidates %+v", repo.searchApplied)
	}
	if repo.searchApplied[0].ExternalURL != "http://example/archive" {
		t.Fatalf("expected retryable archive-failure candidate ranked ahead of hard-failure candidate, got %+v", repo.searchApplied)
	}
	if repo.searchApplied[0].Score <= repo.searchApplied[1].Score {
		t.Fatalf("expected softer archive penalty to preserve a higher score, got %+v", repo.searchApplied)
	}
}

func TestSearchLibraryAllowsRepeatedArchiveFailuresLonger(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
		history: map[string]database.CandidateHistory{
			"http://example/archive": {
				ExternalURL:       "http://example/archive",
				FailureCount:      2,
				LastFailureReason: "archive_headers_invalid",
			},
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{results: []hydra.SearchResult{
		{Title: "Dune.2021.1080p.WEB-DL.x265-ARCHIVE", Link: "http://example/archive", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
	}})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil {
		t.Fatalf("expected archive retry candidate to remain eligible at 2 failures (effective 1, threshold 3), got %+v", result)
	}
	if len(repo.searchApplied) != 1 || repo.searchApplied[0].Rejected {
		t.Fatalf("expected archive retry candidate not to be durably rejected yet, got %+v", repo.searchApplied)
	}
}

func TestSearchLibraryStillRejectsRepeatedlyFailedCandidate(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
		history: map[string]database.CandidateHistory{
			"http://example/old": {
				ExternalURL:       "http://example/old",
				FailureCount:      5,
				LastFailureReason: "context deadline exceeded",
			},
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{results: []hydra.SearchResult{
		{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/old", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
	}})

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID != nil {
		t.Fatalf("expected repeatedly failed candidate to stay rejected, got %+v", result)
	}
	if len(repo.searchApplied) != 1 || !repo.searchApplied[0].Rejected || repo.searchApplied[0].RejectReason != "previously_failed" {
		t.Fatalf("expected durable reject for repeated failures, got %+v", repo.searchApplied)
	}
}

func TestSearchLibraryContinuesPastSelectedCandidateWithFailureHistory(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			IMDbID:        "tt1160419",
			MovieYear:     2021,
		},
		history: map[string]database.CandidateHistory{
			"http://example/old": {
				ExternalURL:       "http://example/old",
				FailureCount:      1,
				LastFailureReason: "context deadline exceeded",
			},
		},
	}
	var queries []string
	service := NewService(repo, seerrStub{}, hydraStub{byQuery: map[string][]hydra.SearchResult{
		"tt1160419": {
			{Title: "Dune.2021.1080p.WEB-DL.x265-OLD", Link: "http://example/old", Indexer: "hydra", SizeBytes: 1200, PublishedAt: time.Now()},
		},
		"Dune 2021": {
			{Title: "Dune.2021.1080p.WEB-DL.x265-NEW", Link: "http://example/new", Indexer: "hydra", SizeBytes: 1200, PublishedAt: time.Now()},
		},
	}, queries: &queries})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil {
		t.Fatalf("expected selected release, got %+v", result)
	}
	if len(queries) < 2 || queries[0] != "tt1160419" || queries[1] != "Dune 2021" {
		t.Fatalf("expected search to continue to later query, got %+v", queries)
	}
	if repo.searchApplied[0].ExternalURL != "http://example/new" {
		t.Fatalf("expected fresh candidate ranked first after later query, got %+v", repo.searchApplied)
	}
}

func TestSearchLibrarySkipsRecentIdenticalHydraRequest(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
	}
	var queries []string
	service := NewService(repo, seerrStub{}, hydraStub{
		byQuery: map[string][]hydra.SearchResult{"Dune 2021": nil, "Dune": nil},
		queries: &queries,
	})

	first, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if first.CandidateCount != 0 || second.CandidateCount != 0 {
		t.Fatalf("expected no candidates, got first=%+v second=%+v", first, second)
	}
	if len(queries) != 2 {
		t.Fatalf("expected Hydra to be called only for the first search plan, got %+v", queries)
	}
	if queries[0] != "Dune 2021" || queries[1] != "Dune" {
		t.Fatalf("unexpected Hydra queries %+v", queries)
	}
}

func TestBuildSearchRequestsIncludesEpisodeQueryVariants(t *testing.T) {
	plan := buildSearchRequests(database.LibrarySearchInput{
		LibraryItemID: 42,
		MediaType:     "episode",
		Title:         "Loki S01E02",
		ShowTitle:     "Loki",
		EpisodeTitle:  "The Variant",
		ShowYear:      2021,
		SeasonNumber:  1,
		EpisodeNumber: 2,
	})
	var queries []string
	for _, req := range plan.Tier2 {
		queries = append(queries, req.Query)
	}
	expected := []string{
		"Loki",
		"Loki 2021",
		"Loki The Variant",
	}
	for _, q := range expected {
		found := false
		for _, got := range queries {
			if got == q {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected query %q in %+v", q, queries)
		}
	}
	if queries[0] != "Loki" {
		t.Fatalf("expected most-specific query first, got %+v", queries)
	}
}

func TestLargestFileFirstSegmentPrefersPayloadOverPar2AndSample(t *testing.T) {
	msgID := largestFileFirstSegment([]database.ImportedNZBFile{
		{
			FileName:      "Movie.vol000+01.par2",
			FileSizeBytes: 9_000,
			Segments:      []database.ImportedNZBSegment{{MessageID: "<par2@host>"}},
		},
		{
			FileName:      "sample.mkv",
			FileSizeBytes: 8_000,
			Segments:      []database.ImportedNZBSegment{{MessageID: "<sample@host>"}},
		},
		{
			FileName:      "Movie.part01.rar",
			FileSizeBytes: 7_000,
			Segments:      []database.ImportedNZBSegment{{MessageID: "<payload@host>"}},
		},
	})
	if msgID != "<payload@host>" {
		t.Fatalf("expected payload segment, got %q", msgID)
	}
}

func TestLargestFileFirstSegmentFallsBackWhenOnlySupportFilesExist(t *testing.T) {
	msgID := largestFileFirstSegment([]database.ImportedNZBFile{
		{
			FileName:      "Movie.vol000+01.par2",
			FileSizeBytes: 9_000,
			Segments:      []database.ImportedNZBSegment{{MessageID: "<par2@host>"}},
		},
		{
			FileName:      "proof.jpg",
			FileSizeBytes: 1_000,
			Segments:      []database.ImportedNZBSegment{{MessageID: "<jpg@host>"}},
		},
	})
	if msgID != "<par2@host>" {
		t.Fatalf("expected fallback support-file segment, got %q", msgID)
	}
}

// Per RFC 3977 and Newshosting's own support docs, "article missing"/status
// 430/423 mean the specific article is confirmed gone — that's the clearest
// signal to reject this candidate and try the next one, not something to
// shrug off as advisory. Only genuine connection/timing errors, where the
// article's actual status is unknown, should be treated as advisory.
func TestShouldIgnoreEarlyPreflightFailure(t *testing.T) {
	if shouldIgnoreEarlyPreflightFailure(errors.New("Newshosting attempt 1: article missing")) {
		t.Fatal("article missing is a confirmed-gone signal, must not be advisory-only")
	}
	if shouldIgnoreEarlyPreflightFailure(errors.New("article not found (cached): abc@host")) {
		t.Fatal("cached article-not-found is a confirmed-gone signal, must not be advisory-only")
	}
	if shouldIgnoreEarlyPreflightFailure(errors.New("yenc crc mismatch")) {
		t.Fatal("crc mismatch should stay fatal for early preflight")
	}
	if !shouldIgnoreEarlyPreflightFailure(errors.New("i/o timeout")) {
		t.Fatal("a genuine connection timeout is ambiguous and should be advisory-only")
	}
}

func TestShouldIgnorePreflightFailure(t *testing.T) {
	if shouldIgnorePreflightFailure(errors.New("preflight: first segment unavailable: article not found (cached)")) {
		t.Fatal("article-not-found is a confirmed-gone signal, must not be advisory-only")
	}
	if shouldIgnorePreflightFailure(errors.New("preflight: first segment unavailable: Newshosting attempt 1: article missing")) {
		t.Fatal("article-missing is a confirmed-gone signal, must not be advisory-only")
	}
	if shouldIgnorePreflightFailure(errors.New("strict health: first segment unavailable: yenc crc mismatch")) {
		t.Fatal("strict health crc mismatch should stay fatal")
	}
	if shouldIgnorePreflightFailure(errors.New("preflight: first segment unavailable: yenc crc mismatch")) {
		t.Fatal("preflight crc mismatch should stay fatal")
	}
	if !shouldIgnorePreflightFailure(errors.New("preflight: first segment unavailable: i/o timeout")) {
		t.Fatal("a genuine connection timeout is ambiguous and should be advisory-only")
	}
}

func TestSearchLibraryUsesStructuredTVDBSearchRequest(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "episode",
			Title:         "Loki S02E03",
			ShowTitle:     "Loki",
			ShowIMDbID:    "tt9140554",
			ShowTVDBID:    362472,
			ShowYear:      2021,
			SeasonNumber:  2,
			EpisodeNumber: 3,
		},
	}
	var requests []hydra.SearchRequest
	service := NewService(repo, seerrStub{}, hydraStub{
		byQuery: map[string][]hydra.SearchResult{
			"Loki": {
				{Title: "Loki.S02E03.1080p.WEB-DL", Link: "http://example/episode", Indexer: "hydra", SizeBytes: 1200, PublishedAt: time.Now()},
			},
		},
		requests: &requests,
	})
	service.fetcher = fetcherStub{
		fileName: "loki.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Loki S02E03.mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.tv</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil {
		t.Fatalf("expected selected release, got %+v", result)
	}
	if len(requests) == 0 || requests[0].TVDBID != 362472 || requests[0].SeasonNumber != 2 || requests[0].EpisodeNumber != 3 {
		t.Fatalf("expected structured tv request, got %+v", requests)
	}
}

func TestSearchLibraryUsesWholeShowRequestsWhenEpisodeMissing(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "episode",
			Title:         "Wednesday",
			ShowTitle:     "Wednesday",
			ShowIMDbID:    "tt13443470",
			ShowTVDBID:    397060,
			ShowYear:      2022,
		},
	}
	var requests []hydra.SearchRequest
	service := NewService(repo, seerrStub{}, hydraStub{
		byQuery: map[string][]hydra.SearchResult{
			"tt13443470": {
				{Title: "Wednesday.2022.S01.1080p.NF.WEB-DL", Link: "http://example/show", Indexer: "hydra", SizeBytes: 1200, PublishedAt: time.Now()},
			},
		},
		requests: &requests,
	})
	service.fetcher = fetcherStub{
		fileName: "wednesday.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Wednesday S01E01.mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.tv</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil {
		t.Fatalf("expected selected release, got %+v", result)
	}
	if len(requests) == 0 {
		t.Fatal("expected at least one hydra request")
	}
	if requests[0].SeasonNumber != 0 || requests[0].EpisodeNumber != 0 {
		t.Fatalf("whole-show request should not send season/episode, got %+v", requests[0])
	}
	if requests[0].TVDBID != 397060 {
		t.Fatalf("expected tvdb id, got %+v", requests[0])
	}
}

func TestSearchLibraryKeepsEarlierUsableCandidatesWhenLaterQueryErrors(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			IMDbID:        "tt1160419",
			MovieYear:     2021,
		},
		history: map[string]database.CandidateHistory{
			"http://example/good": {
				ExternalURL:       "http://example/good",
				FailureCount:      1,
				LastFailureReason: "interrupted_by_restart",
			},
		},
	}
	var queries []string
	service := NewService(repo, seerrStub{}, hydraStub{
		byQuery: map[string][]hydra.SearchResult{
			"tt1160419": {
				{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/good", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
			},
		},
		errByQuery: map[string]error{
			"Dune 2021": errors.New("temporary hydra failure"),
		},
		queries: &queries,
	})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil {
		t.Fatalf("expected selected release, got %+v", result)
	}
	if len(queries) < 2 || queries[0] != "tt1160419" || queries[1] != "Dune 2021" {
		t.Fatalf("expected both queries to be attempted, got %+v", queries)
	}
	if repo.searchApplied[0].ExternalURL != "http://example/good" {
		t.Fatalf("expected earlier good candidate to survive later error, got %+v", repo.searchApplied)
	}
}

func TestSearchLibraryReturnsErrorWhenAllQueriesFail(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			IMDbID:        "tt1160419",
			MovieYear:     2021,
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{
		errByQuery: map[string]error{
			"tt1160419": errors.New("hydra unavailable"),
			"Dune 2021": errors.New("hydra unavailable"),
			"Dune":      errors.New("hydra unavailable"),
		},
	})

	if _, err := service.SearchLibrary(context.Background(), 42); err == nil {
		t.Fatal("expected hydra error when all queries fail")
	}
	if len(repo.searchFailed) != 1 || repo.searchFailed[0] != "42:search_error" {
		t.Fatalf("expected durable search failure state, got %+v", repo.searchFailed)
	}
}

func TestSearchLibraryClassifiesTimeoutFailure(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			IMDbID:        "tt1160419",
			MovieYear:     2021,
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{
		errByQuery: map[string]error{
			"tt1160419": context.DeadlineExceeded,
			"Dune 2021": context.DeadlineExceeded,
			"Dune":      context.DeadlineExceeded,
		},
	})

	if _, err := service.SearchLibrary(context.Background(), 42); err == nil {
		t.Fatal("expected hydra timeout")
	}
	if len(repo.searchFailed) != 1 || repo.searchFailed[0] != "42:search_timeout" {
		t.Fatalf("expected timeout failure state, got %+v", repo.searchFailed)
	}
}

func TestClassifySearchFailureReason(t *testing.T) {
	cases := map[string]string{
		"nzbhydra2 search status 401":                 "search_auth_error",
		"nzbhydra2 search status 429":                 "search_rate_limited",
		"nzbhydra2 search status 503":                 "search_unavailable",
		"nzbhydra2 cloudflare unavailable status 522": "search_unavailable",
		"nzbhydra2 cloudflare timeout status 524":     "search_timeout",
		"dial tcp: connection refused":                "search_unavailable",
		"something else":                              "search_error",
	}
	for input, expected := range cases {
		if got := classifySearchFailureReason(errors.New(input)); got != expected {
			t.Fatalf("for %q expected %q, got %q", input, expected, got)
		}
	}
}

func TestSearchLibraryRetriesTransientHydraFailure(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			IMDbID:        "tt1160419",
			MovieYear:     2021,
		},
	}
	var queries []string
	service := NewService(repo, seerrStub{}, hydraStub{
		seqByQuery: map[string][]hydraReply{
			"tt1160419": {
				{err: context.DeadlineExceeded},
				{results: []hydra.SearchResult{
					{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/good", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
				}},
			},
		},
		queries: &queries,
	})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil {
		t.Fatalf("expected selected release, got %+v", result)
	}
	if len(queries) != 2 || queries[0] != "tt1160419" || queries[1] != "tt1160419" {
		t.Fatalf("expected one retry of same query, got %+v", queries)
	}
}

func TestSearchLibraryDoesNotRetryAuthFailure(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			IMDbID:        "tt1160419",
			MovieYear:     2021,
		},
	}
	var queries []string
	service := NewService(repo, seerrStub{}, hydraStub{
		errByQuery: map[string]error{
			"tt1160419": errors.New("nzbhydra2 search status 401"),
			"Dune 2021": errors.New("nzbhydra2 search status 401"),
			"Dune":      errors.New("nzbhydra2 search status 401"),
		},
		queries: &queries,
	})

	if _, err := service.SearchLibrary(context.Background(), 42); err == nil {
		t.Fatal("expected auth failure")
	}
	if len(queries) != 3 || queries[0] != "tt1160419" || queries[1] != "Dune 2021" || queries[2] != "Dune" {
		t.Fatalf("expected no per-query retry on auth failure, got %+v", queries)
	}
}

func TestManualSearchRetriesTransientHydraFailure(t *testing.T) {
	var queries []string
	service := NewService(&repoStub{}, seerrStub{}, hydraStub{
		seqByQuery: map[string][]hydraReply{
			"dune": {
				{err: errors.New("nzbhydra2 cloudflare timeout status 524")},
				{results: []hydra.SearchResult{
					{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/good", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
				}},
			},
		},
		queries: &queries,
	})

	items, err := service.ManualSearch(context.Background(), "dune")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one result, got %+v", items)
	}
	if len(queries) != 2 || queries[0] != "dune" || queries[1] != "dune" {
		t.Fatalf("expected one retry of same query, got %+v", queries)
	}
}

func TestMaxInlineFallbackDepthUsesBusyQueueLimit(t *testing.T) {
	service := NewService(&repoStub{}, seerrStub{}, hydraStub{})
	queue := newWorkQueueStub()
	for i := 0; i < busyQueueDepthThreshold; i++ {
		queue.Push(context.Background(), int64(i+1), 0)
	}
	service.WorkQueue = queue
	if got := service.maxInlineFallbackDepth(context.Background()); got != busyInlineFallbackDepth {
		t.Fatalf("expected busy inline depth %d, got %d", busyInlineFallbackDepth, got)
	}
}

func TestMaxInlineFallbackDepthUsesDefaultWhenQueueSmall(t *testing.T) {
	service := NewService(&repoStub{}, seerrStub{}, hydraStub{})
	queue := newWorkQueueStub()
	for i := 0; i < 10; i++ {
		queue.Push(context.Background(), int64(i+1), 0)
	}
	service.WorkQueue = queue
	if got := service.maxInlineFallbackDepth(context.Background()); got != defaultInlineFallbackDepth {
		t.Fatalf("expected default inline depth %d, got %d", defaultInlineFallbackDepth, got)
	}
}

func TestMaxInlineFallbackDepthUsesFastLaneOverride(t *testing.T) {
	service := NewService(&repoStub{backlog: busyQueueDepthThreshold + 50}, seerrStub{}, hydraStub{})
	if got := service.maxInlineFallbackDepth(withCompletionFastLane(context.Background())); got != fastLaneInlineFallbackDepth {
		t.Fatalf("expected fast-lane depth %d, got %d", fastLaneInlineFallbackDepth, got)
	}
}

func TestSearchPendingLibrary(t *testing.T) {
	// Item 42 and 43 must resolve to genuinely different releases/URLs here —
	// recentlyDispatchedURL's cooldown (see fetchIndexAndRelease) is keyed
	// purely on ExternalURL, matching real-world duplicate-download
	// prevention, so two items coincidentally sharing a URL in this fixture
	// would (correctly) have the second one skipped rather than re-fetched.
	repo := &repoStub{
		pending: []database.PendingLibrarySearchTarget{
			{LibraryItemID: 42},
			{LibraryItemID: 43},
		},
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
		searchInputByItem: map[int64]database.LibrarySearchInput{
			43: {LibraryItemID: 43, MediaType: "movie", Title: "Arrival", MovieYear: 2016},
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{byQuery: map[string][]hydra.SearchResult{
		"Dune 2021":    {{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/dune.nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()}},
		"Arrival 2016": {{Title: "Arrival.2016.1080p.WEB-DL.x265-GRP", Link: "http://example/arrival.nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()}},
	}})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	// Disable the work queue so SearchPendingLibrary searches synchronously in tests.
	service.WorkQueue = nil
	result, err := service.SearchPendingLibrary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 2 || result.Searched != 2 || result.Selected != 2 || result.Failed != 0 {
		t.Fatalf("unexpected bulk result %+v", result)
	}
	if len(result.ProcessedItems) != 2 || result.ProcessedItems[0] != 42 || result.ProcessedItems[1] != 43 {
		t.Fatalf("unexpected processed items %+v", result.ProcessedItems)
	}
}

func TestProcessLibraryItemResumesSelectedRelease(t *testing.T) {
	repo := &repoStub{
		selected: database.ReleaseSummary{
			SelectedReleaseID: 303,
			LibraryItemID:     42,
			ExternalURL:       "http://example/retry.nzb",
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})
	service.fetcher = fetcherStub{
		fileName: "retry.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Retry (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	if err := service.ProcessLibraryItem(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if repo.fetching != 303 {
		t.Fatalf("expected selected release fetch to resume, got fetching=%d", repo.fetching)
	}
}

// countingFetcherStub tracks how many times Fetch was actually called, so
// tests can assert an NZB was (or wasn't) re-downloaded.
type countingFetcherStub struct {
	fileName string
	raw      []byte
	calls    *int
}

func (f countingFetcherStub) Fetch(ctx context.Context, rawURL string) (string, []byte, error) {
	*f.calls++
	return f.fileName, f.raw, nil
}

// TestFetchIndexAndReleaseSkipsRecentlyDispatchedURL guards a real production
// incident: an indexer (NZB Finder) flagged repeated duplicate downloads of
// the same NZB, threatening account termination. shouldDispatchSelectedTarget
// already had a 30-minute per-URL cooldown, but it only guarded the passive
// pending-dispatch resume path -- an active search that immediately fetches
// what it just selected (the far more common path: SearchLibrary, backlog
// search, season-pack search, manual retry) never consulted it at all, so
// re-discovering the same top candidate (nothing upstream had changed) kept
// re-fetching it. The cooldown must be enforced at the actual fetch point
// (fetchIndexAndRelease) so every caller is covered uniformly.
func TestFetchIndexAndReleaseSkipsRecentlyDispatchedURL(t *testing.T) {
	repo := &repoStub{
		selected: database.ReleaseSummary{
			SelectedReleaseID: 303,
			LibraryItemID:     42,
			ExternalURL:       "http://example/retry.nzb",
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})
	calls := 0
	service.fetcher = countingFetcherStub{
		fileName: "retry.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Retry (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
		calls:    &calls,
	}

	if err := service.ProcessLibraryItem(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 fetch on first attempt, got %d", calls)
	}
	// A second attempt moments later (e.g. a fresh search rediscovering the
	// same candidate) must not re-fetch the identical URL.
	if err := service.ProcessLibraryItem(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected the second attempt to skip re-fetching the same URL, got %d total fetches", calls)
	}
}

// TestFetchAndImportSelectedReleaseDepthSkipsRecentlyDispatchedURL guards a
// second real production incident (a NZB Finder duplicate-download warning
// jumped from 7 to 18 affected releases even after the fetchIndexAndRelease
// fix above shipped): fetchAndImportSelectedReleaseDepth is a separate,
// near-duplicate fetch path used by the recursive candidate-promotion chain
// (promoteNextAfterFailureDepth) that was missed when recentlyDispatchedURL
// was added -- it called s.fetcher.Fetch directly with no cooldown check at
// all, so promoting through the candidate list could re-fetch a URL another
// path had just tried moments earlier. Two different selectedReleaseIDs
// resolving to the same ExternalURL (the promotion chain moving to a
// "different" candidate that happens to share a URL with one already tried)
// must only result in one real fetch.
func TestFetchAndImportSelectedReleaseDepthSkipsRecentlyDispatchedURL(t *testing.T) {
	const sharedURL = "http://example/shared-release.nzb"
	repo := &repoStub{
		selectedByID: map[int64]database.ReleaseSummary{
			501: {SelectedReleaseID: 501, LibraryItemID: 42, ExternalURL: sharedURL},
			502: {SelectedReleaseID: 502, LibraryItemID: 42, ExternalURL: sharedURL},
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})
	calls := 0
	service.fetcher = countingFetcherStub{
		fileName: "shared-release.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Shared (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
		calls:    &calls,
	}

	if _, err := service.fetchAndImportSelectedReleaseDepth(context.Background(), 501, 0); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 fetch for the first release, got %d", calls)
	}
	// A "different" candidate (502) that happens to share the same URL --
	// exactly what the recursive promotion chain does when it moves to the
	// next candidate -- must not re-fetch it.
	if _, err := service.fetchAndImportSelectedReleaseDepth(context.Background(), 502, 0); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected the second candidate sharing a URL to skip re-fetching, got %d total fetches", calls)
	}
}

// TestFetchIndexAndReleaseSkipsPersistedlyDispatchedURLAfterRestart guards
// against the in-memory-cooldown restart gap found in the 2026-07-17
// exhaustive audit: recentURLHits (the in-memory 30-min per-URL fetch
// cooldown) is wiped on every process restart. A release fetch interrupted
// mid-flight by a redeploy (or a resume-dispatch pass racing a fresh
// process start) would otherwise be re-dispatched with zero cooldown
// protection. This simulates exactly that: an empty in-memory map (as if
// the process just restarted) but a persisted dispatch record surviving
// from before the restart -- the fetch must still be skipped.
func TestFetchIndexAndReleaseSkipsPersistedlyDispatchedURLAfterRestart(t *testing.T) {
	const restartURL = "http://example/restart-window.nzb"
	repo := &repoStub{
		selected: database.ReleaseSummary{
			SelectedReleaseID: 303,
			LibraryItemID:     42,
			ExternalURL:       restartURL,
		},
		persistedDispatchedURLs: map[string]bool{restartURL: true},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})
	calls := 0
	service.fetcher = countingFetcherStub{
		fileName: "restart-window.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Restart (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
		calls:    &calls,
	}
	// service.recentURLHits is intentionally left empty -- simulating a
	// process that just restarted and lost its in-memory cooldown state.

	if err := service.ProcessLibraryItem(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("expected the persisted dispatch record to block the fetch even with an empty in-memory cooldown map, got %d fetches", calls)
	}
}

func TestSearchPendingLibraryQueuesAllItems(t *testing.T) {
	const total = pendingQueueBatchSize + 10
	pending := make([]database.PendingLibrarySearchTarget, 0, total)
	for i := range total {
		pending = append(pending, database.PendingLibrarySearchTarget{LibraryItemID: int64(i + 1)})
	}
	repo := &repoStub{pending: pending}
	service := NewService(repo, seerrStub{}, hydraStub{})
	service.WorkQueue = newWorkQueueStub()

	result, err := service.SearchPendingLibrary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != pendingQueueBatchSize || result.Searched != pendingQueueBatchSize {
		t.Fatalf("expected only %d items queued per tick, got %+v", pendingQueueBatchSize, result)
	}
	if depth := service.WorkQueue.Depth(context.Background()); depth != pendingQueueBatchSize {
		t.Fatalf("expected workqueue depth=%d got %d", pendingQueueBatchSize, depth)
	}
}

func TestSearchPendingLibraryDispatchesSelectedAndQueuesSearchItems(t *testing.T) {
	repo := &repoStub{
		selectedBacklog: 2,
		pending: []database.PendingLibrarySearchTarget{
			{LibraryItemID: 1, Selected: true, SelectedReleaseID: 101},
			{LibraryItemID: 2, Selected: false},
			{LibraryItemID: 3, Selected: true, SelectedReleaseID: 103},
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})
	service.WorkQueue = newWorkQueueStub()

	result, err := service.SearchPendingLibrary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 3 || result.Searched != 3 {
		t.Fatalf("unexpected bulk result %+v", result)
	}
	if depth := service.WorkQueue.Depth(context.Background()); depth != 1 {
		t.Fatalf("expected only non-selected item in work queue, got depth %d", depth)
	}
}

// TestSearchPendingLibraryQueuesEveryMissingEpisode verifies every missing
// episode gets its own search turn each cycle, matching Sonarr's
// EpisodeSearchService (searches every missing episode/group, not a rotating
// per-season representative). Season-pack request dedup is handled separately
// and independently by ShouldAttemptSeasonPack's own cooldown.
func TestSearchPendingLibraryQueuesEveryMissingEpisode(t *testing.T) {
	repo := &repoStub{
		pending: []database.PendingLibrarySearchTarget{
			{LibraryItemID: 1, MediaType: "episode", TVShowID: 500},
			{LibraryItemID: 2, MediaType: "episode", TVShowID: 500},
			{LibraryItemID: 3, MediaType: "episode", TVShowID: 500},
			{LibraryItemID: 4, MediaType: "episode", TVShowID: 600},
			{LibraryItemID: 5, MediaType: "movie"},
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})
	service.WorkQueue = newWorkQueueStub()

	result, err := service.SearchPendingLibrary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 5 || result.Searched != 5 {
		t.Fatalf("expected every missing item to be searched, got %+v", result)
	}
	if got := service.WorkQueue.Depth(context.Background()); got != 5 {
		t.Fatalf("expected workqueue depth 5, got %d", got)
	}
	for i, id := range []int64{1, 2, 3, 4, 5} {
		if result.ProcessedItems[i] != id {
			t.Fatalf("unexpected processed items %+v", result.ProcessedItems)
		}
	}
}

func TestDispatchAutomaticPendingOnlyResumesSelectedItems(t *testing.T) {
	now := time.Now()
	repo := &repoStub{
		pending: []database.PendingLibrarySearchTarget{
			{LibraryItemID: 1, Selected: true, SelectedReleaseID: 101, State: database.QueueRequested, UpdatedAt: now},
			{LibraryItemID: 2, MediaType: "movie", Selected: false, SelectedReleaseID: 0, State: database.QueueRequested, UpdatedAt: now},
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})

	result, err := service.DispatchAutomaticPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 1 || result.Searched != 1 {
		t.Fatalf("expected only selected item to auto-dispatch, got %+v", result)
	}
}

func TestShouldDispatchSelectedTargetAllowsFreshSelectedFallback(t *testing.T) {
	now := time.Now()
	service := NewService(&repoStub{}, seerrStub{}, hydraStub{})
	target := database.PendingLibrarySearchTarget{
		LibraryItemID:     42,
		SelectedReleaseID: 303,
		State:             database.QueueSelected,
		UpdatedAt:         now,
	}
	if !service.shouldDispatchSelectedTarget(target, now) {
		t.Fatal("expected selected fallback candidate to dispatch immediately")
	}
}

func TestShouldDispatchSelectedTargetImmediatelyDispatchesRequestedSelection(t *testing.T) {
	now := time.Now()
	service := NewService(&repoStub{}, seerrStub{}, hydraStub{})
	target := database.PendingLibrarySearchTarget{
		LibraryItemID:     42,
		SelectedReleaseID: 303,
		State:             database.QueueRequested,
		UpdatedAt:         now,
	}
	if !service.shouldDispatchSelectedTarget(target, now) {
		t.Fatal("expected requested item with a selected release to dispatch immediately")
	}
}

// TestShouldDispatchSelectedTargetBlocksRecentlyDispatchedSelectedFallback
// guards the account-risk production fix: a candidate left in QueueSelected
// (mid-fallback, after promoteNextAfterFailureDepth defers the rest of the
// chain to a later pass) previously bypassed the cooldown entirely, so the
// 30-second pending-dispatch loop re-submitted an actual NZB download for
// the same release on every tick. An indexer flagged this live as repeated
// duplicate downloads of the same NZB files with an account termination
// warning. The cooldown must apply regardless of state.
func TestShouldDispatchSelectedTargetBlocksRecentlyDispatchedSelectedFallback(t *testing.T) {
	now := time.Now()
	service := NewService(&repoStub{}, seerrStub{}, hydraStub{})
	rawURL := "http://example/release.nzb"
	service.markSelectedReleaseURLDispatched(rawURL, now.Add(-selectedURLCooldown+time.Minute))
	target := database.PendingLibrarySearchTarget{
		LibraryItemID:     42,
		SelectedReleaseID: 303,
		ExternalURL:       rawURL,
		State:             database.QueueSelected,
		UpdatedAt:         now,
	}
	if service.shouldDispatchSelectedTarget(target, now) {
		t.Fatal("expected a recently-dispatched URL to stay in cooldown even in QueueSelected state")
	}
	// Once the cooldown window has genuinely elapsed, dispatch must resume.
	past := now.Add(-selectedURLCooldown - time.Minute)
	service.recentURLMu.Lock()
	service.recentURLHits[rawURL] = past
	service.recentURLMu.Unlock()
	if !service.shouldDispatchSelectedTarget(target, now) {
		t.Fatal("expected dispatch to resume once the cooldown window elapsed")
	}
}

func TestShouldDispatchSelectedTargetBlocksRecentlyDispatchedSameURL(t *testing.T) {
	now := time.Now()
	service := NewService(&repoStub{}, seerrStub{}, hydraStub{})
	rawURL := "http://example/release.nzb"
	service.markSelectedReleaseURLDispatched(rawURL, now.Add(-selectedURLCooldown+time.Minute))
	target := database.PendingLibrarySearchTarget{
		LibraryItemID:     42,
		SelectedReleaseID: 303,
		ExternalURL:       rawURL,
		State:             database.QueueRequested,
		UpdatedAt:         now,
	}
	if service.shouldDispatchSelectedTarget(target, now) {
		t.Fatal("expected same external_url to stay in cooldown")
	}
	target.ExternalURL = "http://example/other.nzb"
	if !service.shouldDispatchSelectedTarget(target, now) {
		t.Fatal("expected different external_url to bypass duplicate cooldown")
	}
}

func TestSearchRecentPendingMovieSelectsWithoutActiveHydraSearch(t *testing.T) {
	repo := &repoStub{
		pending: []database.PendingLibrarySearchTarget{{LibraryItemID: 42}},
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			IMDbID:        "tt1160419",
			MovieYear:     2021,
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{
		recent: map[string][]hydra.SearchResult{
			"movie": {
				{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
			},
		},
	})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SearchRecentPending(context.Background(), "movie")
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 1 || result.Searched != 1 || result.Selected != 1 || result.Failed != 0 {
		t.Fatalf("unexpected result %+v", result)
	}
}

// TestSearchRecentPendingSkipsItemAlreadyInFlight guards against a real gap
// found in the 2026-07-17 exhaustive audit: SearchRecentPending (the RSS
// "recent feed" pass) computed candidates and called ReplaceSearchCandidates
// + fetchAndImportSelectedRelease directly, without ever checking
// s.searchInflight -- the only per-library-item mutual-exclusion guard
// SearchLibrary uses. A manual search, backlog_search, or queue_housekeeping
// retry racing this same item could independently select and fetch a
// different candidate's URL for it at the same time.
func TestSearchRecentPendingSkipsItemAlreadyInFlight(t *testing.T) {
	repo := &repoStub{
		pending: []database.PendingLibrarySearchTarget{{LibraryItemID: 42}},
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			IMDbID:        "tt1160419",
			MovieYear:     2021,
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{
		recent: map[string][]hydra.SearchResult{
			"movie": {
				{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
			},
		},
	})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	// Simulate another goroutine (a manual search, backlog_search, or
	// queue_housekeeping retry) already searching/selecting for this item.
	service.searchInflight.Store(int64(42), struct{}{})

	result, err := service.SearchRecentPending(context.Background(), "movie")
	if err != nil {
		t.Fatal(err)
	}
	if result.Searched != 0 || result.Selected != 0 {
		t.Fatalf("expected the in-flight item to be skipped without searching/selecting, got %+v", result)
	}
	if len(repo.searchApplied) != 0 {
		t.Fatalf("expected ReplaceSearchCandidates not to be called for an in-flight item, got %+v", repo.searchApplied)
	}
}

func TestSearchRecentPendingSkipsNonMatchingMediaType(t *testing.T) {
	repo := &repoStub{
		pending: []database.PendingLibrarySearchTarget{{LibraryItemID: 42}},
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{
		recent: map[string][]hydra.SearchResult{
			"tv": {
				{Title: "Loki.S02E03.1080p.WEB-DL.x265-GRP", Link: "http://example/nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
			},
		},
	})
	result, err := service.SearchRecentPending(context.Background(), "tv")
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 0 || result.Searched != 0 || result.Selected != 0 {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestSyncRequestsDoesNotQueueCreatedItems(t *testing.T) {
	service := NewService(&repoStub{}, seerrStub{
		requests: []seerr.Request{
			{ID: 1, Type: "movie", TMDBID: 11, MediaTitle: "Dune", MediaYear: 2021},
		},
	}, hydraStub{})
	service.WorkQueue = newWorkQueueStub()
	if _, err := service.SyncRequests(context.Background()); err != nil {
		t.Fatal(err)
	}
	if depth := service.WorkQueue.Depth(context.Background()); depth != 0 {
		t.Fatalf("expected no auto-queued items after sync, got depth=%d", depth)
	}
}

func TestRestoreRejectedReleases(t *testing.T) {
	repo := &repoStub{}
	service := NewService(repo, seerrStub{}, hydraStub{})

	result, err := service.RestoreRejectedReleases(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.LibraryItemID != 42 || result.Restored != 2 {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(repo.restoredGroup) != 1 || repo.restoredGroup[0] != 42 {
		t.Fatalf("unexpected restored groups %+v", repo.restoredGroup)
	}
}

func TestRetryFailedQueue(t *testing.T) {
	repo := &repoStub{
		failedQueues: []database.FailedQueueRetryTarget{
			{QueueItemID: 55, LibraryItemID: 42, FailureReason: "interrupted_by_restart", HasSelectedRelease: true, CandidateFailureCount: 0},
			{QueueItemID: 56, LibraryItemID: 43, FailureReason: "stale_worker", HasSelectedRelease: true, CandidateFailureCount: 0},
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})

	result, err := service.RetryFailedQueue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 2 || result.Retried != 2 || result.Failed != 0 {
		t.Fatalf("unexpected bulk retry result %+v", result)
	}
	if len(result.ProcessedQueues) != 2 || result.ProcessedQueues[0] != 55 || result.ProcessedQueues[1] != 56 {
		t.Fatalf("unexpected processed queues %+v", result.ProcessedQueues)
	}
	if len(repo.requeued) != 2 || repo.requeued[0] != 55 || repo.requeued[1] != 56 {
		t.Fatalf("unexpected requeued items %+v", repo.requeued)
	}
}

func TestSearchLibraryFallsBackToNextCandidate(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
		next: &database.ReleaseSummary{
			SelectedReleaseID:  89,
			ReleaseCandidateID: 2,
			LibraryItemID:      42,
			Title:              "Dune.2021.720p.WEB-DL.x264-GRP2",
			ExternalURL:        "http://example/next.nzb",
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{results: []hydra.SearchResult{
		{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/bad.nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
		{Title: "Dune.2021.720p.WEB-DL.x264-GRP2", Link: "http://example/next.nzb", Indexer: "hydra", SizeBytes: 4567, PublishedAt: time.Now()},
	}})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}
	service.fetcher = &sequenceFetcher{
		results: []fetchResult{
			{err: context.DeadlineExceeded},
			{fileName: "next.nzb", raw: []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`)},
		},
	}

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil || *result.SelectedReleaseID != 89 {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(repo.failed) != 1 || repo.fetching != 89 || repo.indexed != 99 {
		t.Fatalf("unexpected fallback state failed=%v fetching=%d indexed=%d", repo.failed, repo.fetching, repo.indexed)
	}
}

func TestSearchLibraryFallsBackWhenPublishFails(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
		next: &database.ReleaseSummary{
			SelectedReleaseID:  89,
			ReleaseCandidateID: 2,
			LibraryItemID:      42,
			Title:              "Dune.2021.720p.WEB-DL.x264-GRP2",
			ExternalURL:        "http://example/next.nzb",
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{results: []hydra.SearchResult{
		{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/first.nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
		{Title: "Dune.2021.720p.WEB-DL.x264-GRP2", Link: "http://example/next.nzb", Indexer: "hydra", SizeBytes: 4567, PublishedAt: time.Now()},
	}})
	service.fetcher = &sequenceFetcher{
		results: []fetchResult{
			{fileName: "first.nzb", raw: []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`)},
			{fileName: "next.nzb", raw: []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`)},
		},
	}
	calls := 0
	service.SetPostImportHook(func(ctx context.Context, item database.QueueSnapshot) error {
		calls++
		if calls == 1 {
			return library.ErrNoVirtualFiles
		}
		return nil
	})

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil || *result.SelectedReleaseID != 89 {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(repo.failed) != 1 || repo.failed[0] != library.ErrNoVirtualFiles.Error() {
		t.Fatalf("unexpected failed reasons %+v", repo.failed)
	}
}

func TestSearchLibraryFallsBackWithArchiveRejectReason(t *testing.T) {
	repo := &repoStub{
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
		next: &database.ReleaseSummary{
			SelectedReleaseID:  89,
			ReleaseCandidateID: 2,
			LibraryItemID:      42,
			Title:              "Dune.2021.720p.WEB-DL.x264-GRP2",
			ExternalURL:        "http://example/next.nzb",
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{results: []hydra.SearchResult{
		{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/first.nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
		{Title: "Dune.2021.720p.WEB-DL.x264-GRP2", Link: "http://example/next.nzb", Indexer: "hydra", SizeBytes: 4567, PublishedAt: time.Now()},
	}})
	service.fetcher = &sequenceFetcher{
		results: []fetchResult{
			{fileName: "first.nzb", raw: []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Movie.part01.rar&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`)},
			{fileName: "next.nzb", raw: []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`)},
		},
	}
	calls := 0
	service.SetPostImportHook(func(ctx context.Context, item database.QueueSnapshot) error {
		calls++
		if calls == 1 {
			repo.selected.ArchiveRejects = "archive_video_not_found"
			return library.ErrNoVirtualFiles
		}
		return nil
	})

	result, err := service.SearchLibrary(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil || *result.SelectedReleaseID != 89 {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(repo.failed) != 1 || repo.failed[0] != "archive_video_not_found" {
		t.Fatalf("unexpected failed reasons %+v", repo.failed)
	}
}

func TestImportSelectedReleaseFallsBackBeforePublishHookWhenArchiveRejectHasNoVirtualFiles(t *testing.T) {
	repo := &repoStub{
		selectedByID: map[int64]database.ReleaseSummary{
			88: {
				SelectedReleaseID:  88,
				ReleaseCandidateID: 1,
				LibraryItemID:      42,
				Title:              "First.Release",
				ExternalURL:        "http://example/first.nzb",
				ArchiveRejects:     "archive_video_not_found",
				VirtualFileCount:   0,
			},
		},
		next: &database.ReleaseSummary{
			SelectedReleaseID:  89,
			ReleaseCandidateID: 2,
			LibraryItemID:      42,
			Title:              "Next.Release",
			ExternalURL:        "http://example/next.nzb",
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})
	service.fetcher = &sequenceFetcher{
		results: []fetchResult{
			{fileName: "next.nzb", raw: []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`)},
		},
	}
	postImportCalls := 0
	service.SetPostImportHook(func(ctx context.Context, item database.QueueSnapshot) error {
		postImportCalls++
		return nil
	})

	result, err := service.importSelectedRelease(context.Background(), database.ReleaseSummary{
		SelectedReleaseID:  88,
		ReleaseCandidateID: 1,
		LibraryItemID:      42,
		Title:              "First.Release",
		ExternalURL:        "http://example/first.nzb",
	}, database.ImportedNZB{
		FileName: "first.nzb",
		XML:      []byte(`<nzb/>`),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || *result != 89 {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(repo.failed) != 1 || repo.failed[0] != "archive_video_not_found" {
		t.Fatalf("unexpected failed reasons %+v", repo.failed)
	}
	if postImportCalls != 1 {
		t.Fatalf("expected post import only for promoted candidate, got %d", postImportCalls)
	}
}

func TestSelectRelease(t *testing.T) {
	repo := &repoStub{}
	service := NewService(repo, seerrStub{}, hydraStub{})
	service.fetcher = fetcherStub{
		fileName: "manual.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Manual (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SelectRelease(context.Background(), 77)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil || *result.SelectedReleaseID != 101 {
		t.Fatalf("unexpected result %+v", result)
	}
	if repo.fetching != 101 || repo.indexed != 99 {
		t.Fatalf("unexpected state fetching=%d indexed=%d", repo.fetching, repo.indexed)
	}
}

func TestRejectReleasePromotesNext(t *testing.T) {
	repo := &repoStub{
		next: &database.ReleaseSummary{
			SelectedReleaseID:  202,
			ReleaseCandidateID: 9,
			LibraryItemID:      42,
			Title:              "Promoted.Release",
			ExternalURL:        "http://example/promoted.nzb",
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})
	service.fetcher = fetcherStub{
		fileName: "promoted.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Promoted (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.RejectRelease(context.Background(), 8, "manual_reject")
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedReleaseID == nil || *result.SelectedReleaseID != 202 {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(repo.rejected) != 1 || repo.fetching != 202 {
		t.Fatalf("unexpected state rejected=%v fetching=%d", repo.rejected, repo.fetching)
	}
}

func TestRestoreRelease(t *testing.T) {
	repo := &repoStub{}
	service := NewService(repo, seerrStub{}, hydraStub{})

	result, err := service.RestoreRelease(context.Background(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "restored" || result.ReleaseCandidateID != 8 {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(repo.rejected) != 1 || repo.rejected[0] != "restored" {
		t.Fatalf("unexpected restore state %+v", repo.rejected)
	}
}

func TestSkipReleasePromotesNext(t *testing.T) {
	repo := &repoStub{
		next: &database.ReleaseSummary{
			SelectedReleaseID:  202,
			ReleaseCandidateID: 9,
			LibraryItemID:      42,
			Title:              "Promoted.Release",
			ExternalURL:        "http://example/promoted.nzb",
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})
	service.fetcher = fetcherStub{
		fileName: "promoted.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Promoted (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.SkipRelease(context.Background(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "skipped" || result.SelectedReleaseID == nil || *result.SelectedReleaseID != 202 {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(repo.skipped) != 1 || repo.skipped[0] != 8 || repo.fetching != 202 {
		t.Fatalf("unexpected skip state skipped=%v fetching=%d", repo.skipped, repo.fetching)
	}
}

func TestRetryQueueItemSelectedRelease(t *testing.T) {
	repo := &repoStub{
		retryTarget: database.QueueRetryTarget{
			QueueItemID:       55,
			LibraryItemID:     42,
			SelectedReleaseID: func() *int64 { v := int64(303); return &v }(),
		},
		selected: database.ReleaseSummary{
			SelectedReleaseID: 303,
			LibraryItemID:     42,
			ExternalURL:       "http://example/retry.nzb",
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})
	service.fetcher = fetcherStub{
		fileName: "retry.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Retry (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.RetryQueueItem(context.Background(), 55)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "retried_selected_release" || result.SelectedReleaseID == nil || *result.SelectedReleaseID != 303 {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestRetryQueueItemStoredNZB(t *testing.T) {
	repo := &repoStub{
		retryTarget: database.QueueRetryTarget{
			QueueItemID:       58,
			LibraryItemID:     42,
			SelectedReleaseID: func() *int64 { v := int64(304); return &v }(),
		},
		selected: database.ReleaseSummary{
			SelectedReleaseID: 304,
			LibraryItemID:     42,
			ExternalURL:       "",
		},
		stored: database.StoredNZBDocument{
			SelectedReleaseID: 304,
			FileName:          "stored-retry.nzb",
			XML:               []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Stored Retry (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})

	result, err := service.RetryQueueItem(context.Background(), 58)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "retried_stored_nzb" || result.SelectedReleaseID == nil || *result.SelectedReleaseID != 304 {
		t.Fatalf("unexpected result %+v", result)
	}
	if repo.fetching != 304 || repo.imported.FileName != "stored-retry.nzb" || repo.indexed != 99 {
		t.Fatalf("unexpected stored retry state fetching=%d imported=%+v indexed=%d", repo.fetching, repo.imported, repo.indexed)
	}
}

func TestRetryQueueItemUsesAlternativeCandidateWhenSelectedHasFailureHistory(t *testing.T) {
	repo := &repoStub{
		retryTarget: database.QueueRetryTarget{
			QueueItemID:       59,
			LibraryItemID:     42,
			SelectedReleaseID: func() *int64 { v := int64(305); return &v }(),
		},
		selected: database.ReleaseSummary{
			SelectedReleaseID:  305,
			ReleaseCandidateID: 9001,
			LibraryItemID:      42,
			ExternalURL:        "http://example/old-retry.nzb",
			FailureCount:       2,
		},
		alternative: &database.ReleaseSummary{
			SelectedReleaseID: 406,
			LibraryItemID:     42,
			ExternalURL:       "http://example/alt.nzb",
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})
	service.fetcher = fetcherStub{
		fileName: "alt.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Alternative (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.RetryQueueItem(context.Background(), 59)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "retried_alternative_candidate" || result.SelectedReleaseID == nil || *result.SelectedReleaseID != 406 {
		t.Fatalf("unexpected result %+v", result)
	}
	if repo.fetching != 406 {
		t.Fatalf("expected alternative release fetch, got %d", repo.fetching)
	}
}

func TestRetryQueueItemResearchesLibrary(t *testing.T) {
	repo := &repoStub{
		retryTarget: database.QueueRetryTarget{
			QueueItemID:   56,
			LibraryItemID: 42,
		},
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{results: []hydra.SearchResult{
		{Title: "Dune.2021.1080p.WEB-DL.x265-GRP", Link: "http://example/nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
	}})
	service.fetcher = fetcherStub{
		fileName: "dune.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.RetryQueueItem(context.Background(), 56)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "researched_library_item" || result.SearchCandidateCnt != 1 {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestRetryQueueItemUsesExistingCandidateBeforeResearch(t *testing.T) {
	repo := &repoStub{
		retryTarget: database.QueueRetryTarget{
			QueueItemID:   57,
			LibraryItemID: 42,
		},
		promoted: &database.ReleaseSummary{
			SelectedReleaseID: 404,
			LibraryItemID:     42,
			ExternalURL:       "http://example/existing.nzb",
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{results: []hydra.SearchResult{
		{Title: "Should.Not.Be.Used", Link: "http://example/new.nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
	}})
	service.fetcher = fetcherStub{
		fileName: "existing.nzb",
		raw:      []byte(`<?xml version="1.0" encoding="UTF-8"?><nzb><file subject="&quot;Existing (2021).mkv&quot;" poster="poster" date="1710000000"><groups><group>alt.binaries.movies</group></groups><segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments></file></nzb>`),
	}

	result, err := service.RetryQueueItem(context.Background(), 57)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "retried_existing_candidate" || result.SelectedReleaseID == nil || *result.SelectedReleaseID != 404 {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(repo.searchApplied) != 0 {
		t.Fatalf("did not expect fresh search, got candidates %+v", repo.searchApplied)
	}
}

func TestSearchUpgradesRequiresMinimumCustomFormatScoreIncrement(t *testing.T) {
	repo := &repoStub{
		upgradable: []int64{42},
		itemProfile: &database.QualityProfile{
			Name:                            "Upgrade Gate",
			AllowUpgrade:                    true,
			MinimumUpgradeCustomFormatScore: 100,
		},
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
		selected: database.ReleaseSummary{
			SelectedReleaseID:  90,
			LibraryItemID:      42,
			CustomFormatScore:  50,
			ReleaseCandidateID: 9,
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{results: []hydra.SearchResult{
		{Title: "Dune.2021.1080p.WEB-DL.Atmos-GRP", Link: "http://example/nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
	}})

	result, err := service.SearchUpgrades(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Upgraded != 0 {
		t.Fatalf("expected upgrade to be blocked by CF threshold, got %+v", result)
	}
	if len(repo.searchApplied) != 1 {
		t.Fatalf("expected one candidate, got %+v", repo.searchApplied)
	}
	if !repo.searchApplied[0].Rejected || repo.searchApplied[0].RejectReason != "upgrade_custom_format_score" {
		t.Fatalf("expected upgrade_custom_format_score reject, got %+v", repo.searchApplied[0])
	}
}

// TestSearchUpgradesSkipsItemAlreadyInFlight is the SearchUpgrades
// counterpart to TestSearchRecentPendingSkipsItemAlreadyInFlight: it called
// searchLibraryOnceWithMode directly, bypassing the same s.searchInflight
// guard SearchLibrary uses.
func TestSearchUpgradesSkipsItemAlreadyInFlight(t *testing.T) {
	repo := &repoStub{
		upgradable: []int64{42},
		itemProfile: &database.QualityProfile{
			Name:         "Upgrade Gate",
			AllowUpgrade: true,
		},
		searchInput: database.LibrarySearchInput{
			LibraryItemID: 42,
			MediaType:     "movie",
			Title:         "Dune",
			MovieYear:     2021,
		},
		selected: database.ReleaseSummary{
			SelectedReleaseID:  90,
			LibraryItemID:      42,
			CustomFormatScore:  50,
			ReleaseCandidateID: 9,
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{results: []hydra.SearchResult{
		{Title: "Dune.2021.2160p.WEB-DL.Atmos-GRP", Link: "http://example/nzb", Indexer: "hydra", SizeBytes: 1234, PublishedAt: time.Now()},
	}})

	// Simulate another goroutine (a manual search, backlog_search, or
	// queue_housekeeping retry) already searching/selecting for this item.
	service.searchInflight.Store(int64(42), struct{}{})

	result, err := service.SearchUpgrades(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Upgraded != 0 || result.Failed != 0 {
		t.Fatalf("expected the in-flight item to be skipped without upgrading or failing, got %+v", result)
	}
	if len(repo.searchApplied) != 0 {
		t.Fatalf("expected no search candidates to be applied for an in-flight item, got %+v", repo.searchApplied)
	}
}

func TestFillMissingEpisodesBatchesWithoutAutoQueueingNewItems(t *testing.T) {
	repo := &repoStub{
		missingShows: []database.ShowWithMissingEpisodes{{
			TVShowID:  77,
			TMDBID:    1234,
			ShowTitle: "Loki",
		}},
		batchCreatedIDs: []int64{501, 502},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})
	service.SetTMDBClient(tmdbFillMissingStub{})
	service.WorkQueue = newWorkQueueStub()

	result, err := service.FillMissingEpisodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.ShowsProcessed != 1 || result.EpisodesFound != 2 || result.ItemsCreated != 2 {
		t.Fatalf("unexpected result %+v", result)
	}
	if repo.batchCreatedBatches != 1 {
		t.Fatalf("expected one batch insert, got %d", repo.batchCreatedBatches)
	}
	if depth := service.WorkQueue.Depth(context.Background()); depth != 0 {
		t.Fatalf("expected no auto-queued items, got %d", depth)
	}
}

func TestFillMissingEpisodesQueuesRecentlyAiredNewItems(t *testing.T) {
	repo := &repoStub{
		missingShows: []database.ShowWithMissingEpisodes{{
			TVShowID:  88,
			TMDBID:    5678,
			ShowTitle: "The Bear",
		}},
		batchCreatedIDs: []int64{601, 602},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})
	service.SetTMDBClient(tmdbFillMissingRecentStub{})
	service.WorkQueue = newWorkQueueStub()

	result, err := service.FillMissingEpisodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.ShowsProcessed != 1 || result.EpisodesFound != 2 || result.ItemsCreated != 2 || result.Queued != 2 {
		t.Fatalf("unexpected result %+v", result)
	}
	if repo.batchCreatedBatches != 1 {
		t.Fatalf("expected one batch insert, got %d", repo.batchCreatedBatches)
	}
	if depth := service.WorkQueue.Depth(context.Background()); depth != 2 {
		t.Fatalf("expected two queued recent items, got %d", depth)
	}
}

type fetchResult struct {
	fileName string
	raw      []byte
	err      error
}

type sequenceFetcher struct {
	results []fetchResult
	index   int
}

func (f *sequenceFetcher) Fetch(ctx context.Context, rawURL string) (string, []byte, error) {
	result := f.results[f.index]
	if f.index < len(f.results)-1 {
		f.index++
	}
	return result.fileName, result.raw, result.err
}

func TestCalibrateImportedDocumentBestEffortAsyncDedupes(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var calls atomic.Int32
	repo := &repoStub{
		calibrateFn: func(_ context.Context, id int64) error {
			if id != 42 {
				t.Errorf("unexpected nzb document id %d", id)
			}
			if calls.Add(1) == 1 {
				select {
				case started <- struct{}{}:
				default:
				}
				<-release
			}
			return nil
		},
	}
	service := NewService(repo, seerrStub{}, hydraStub{})

	start := time.Now()
	service.calibrateImportedDocumentBestEffort(42)
	service.calibrateImportedDocumentBestEffort(42)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("calibration scheduling blocked for %s", elapsed)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background calibration did not start")
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) && calls.Load() < 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected one in-flight calibration, got %d", got)
	}

	done := make(chan struct{})
	go func() {
		service.calibrateImportedDocumentBestEffort(42)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("duplicate calibration schedule blocked while first run was in flight")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected duplicate schedule to be deduped, got %d calls", got)
	}

	close(release)

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := service.calibrateInflight.Load(int64(42)); !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := service.calibrateInflight.Load(int64(42)); ok {
		t.Fatal("calibration inflight marker was not cleared")
	}

	service.calibrateImportedDocumentBestEffort(42)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) && calls.Load() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected calibration to be schedulable again after completion, got %d calls", got)
	}
}

// TestForceSearchBypassesDedupCooldown verifies WithForceSearch lets a
// user-initiated re-search through even when an identical request was just
// remembered as skippable -- without it, "Search Again"/"Search Now" clicked
// twice in quick succession would silently no-op (see shouldSkipSearchRequest).
func TestForceSearchBypassesDedupCooldown(t *testing.T) {
	service := NewService(&repoStub{}, seerrStub{}, hydraStub{})
	req := hydraSearchRequestForTest()
	now := time.Now()

	if service.shouldSkipSearchRequest(context.Background(), 1, req, now) {
		t.Fatal("first request should never be skipped")
	}
	service.rememberSearchRequest(1, req, "empty", now)

	if !service.shouldSkipSearchRequest(context.Background(), 1, req, now) {
		t.Fatal("identical request within cooldown should be skipped for a normal (automated) caller")
	}
	if service.shouldSkipSearchRequest(WithForceSearch(context.Background()), 1, req, now) {
		t.Fatal("WithForceSearch should bypass the dedup cooldown for manual search callers")
	}
}

func hydraSearchRequestForTest() hydra.SearchRequest {
	return hydra.SearchRequest{MediaType: "movie", Query: "Test Movie", TMDBID: 123}
}

// TestParseCandidateDetectsSDResolutions guards a real production incident: the
// automated search path's resolution detection only recognized "2160p",
// "1080p", "720p" tokens (unlike the manual-import path, which also checks
// "576p"/"480p"), so an SD release's Candidate.Resolution came out blank.
// Combined with a since-fixed ranking bug where a blank resolution skipped the
// profile's allow-list entirely, this let an SD DVD rip get selected under a
// profile locked to HD resolutions only.
func TestParseCandidateDetectsSDResolutions(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"Movie.Title.2021.480p.WEB-DL-GROUP", "480p"},
		{"Movie.Title.2021.576p.WEBRip-GROUP", "576p"},
		{"Movie.Title.2021.720p.WEB-DL-GROUP", "720p"},
	}
	for _, tc := range cases {
		result := hydra.SearchResult{Title: tc.title}
		candidate := parseCandidate(result, database.CandidateHistory{}, 0, false, nil)
		if candidate.Resolution != tc.want {
			t.Fatalf("title=%q: expected resolution %q, got %q", tc.title, tc.want, candidate.Resolution)
		}
	}
}

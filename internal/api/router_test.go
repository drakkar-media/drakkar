package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hjongedijk/drakkar/internal/cache"
	"github.com/hjongedijk/drakkar/internal/config"
	"github.com/hjongedijk/drakkar/internal/database"
	"github.com/hjongedijk/drakkar/internal/library"
	"github.com/hjongedijk/drakkar/internal/maintenance"
	"github.com/hjongedijk/drakkar/internal/nzb"
	"github.com/hjongedijk/drakkar/internal/probe"
	"github.com/hjongedijk/drakkar/internal/queue"
	intsub "github.com/hjongedijk/drakkar/internal/subtitles"
	"github.com/hjongedijk/drakkar/internal/workflow"
)

type statusStub struct{}

type workflowStub struct {
	requests   []database.MediaRequestSummary
	sync       workflow.SyncResult
	pending    workflow.BulkSearchResult
	retryAll   workflow.BulkQueueRetryResult
	search     workflow.SearchResult
	selectr    workflow.ReleaseActionResult
	reject     workflow.ReleaseActionResult
	restore    workflow.ReleaseActionResult
	restoreAll database.RejectedReleaseRestoreResult
	skip       workflow.ReleaseActionResult
	retry      workflow.QueueRetryResult
}

type publicationStub struct {
	republished int64
	pending     library.BulkRepublishResult
}

type maintenanceStub struct{}
type cacheStub struct{}
type probeStub struct {
	report probe.Report
}
type subtitleStub struct {
	items      []database.SubtitleFileSummary
	candidates []database.SubtitleCandidateSummary
	search     intsub.SearchResult
	download   intsub.UploadResult
	upload     intsub.UploadResult
	deleted    int64
}
type blocklistStub struct {
	items   []database.BlocklistItemSummary
	cleared int64
	all     database.BlocklistClearResult
}

func (statusStub) Status() Status {
	return Status{Service: "drakkar", Healthy: true, StartedAt: time.Now().UTC()}
}

func (w workflowStub) ListRequests(ctx context.Context) ([]database.MediaRequestSummary, error) {
	return w.requests, nil
}

func (w workflowStub) SyncRequests(ctx context.Context) (workflow.SyncResult, error) {
	return w.sync, nil
}

func (w workflowStub) CreateSeerrRequest(ctx context.Context, mediaType string, tmdbID int64) (workflow.SyncResult, error) {
	return w.sync, nil
}

func (w workflowStub) SearchPendingLibrary(ctx context.Context) (workflow.BulkSearchResult, error) {
	return w.pending, nil
}

func (w workflowStub) RetryFailedQueue(ctx context.Context) (workflow.BulkQueueRetryResult, error) {
	return w.retryAll, nil
}

func (w workflowStub) SearchLibrary(ctx context.Context, libraryItemID int64) (workflow.SearchResult, error) {
	return w.search, nil
}

func (w workflowStub) SelectRelease(ctx context.Context, releaseCandidateID int64) (workflow.ReleaseActionResult, error) {
	return w.selectr, nil
}

func (w workflowStub) RejectRelease(ctx context.Context, releaseCandidateID int64, reason string) (workflow.ReleaseActionResult, error) {
	return w.reject, nil
}

func (w workflowStub) RetryQueueItem(ctx context.Context, queueItemID int64) (workflow.QueueRetryResult, error) {
	return w.retry, nil
}

func (w workflowStub) RestoreRelease(ctx context.Context, releaseCandidateID int64) (workflow.ReleaseActionResult, error) {
	return w.restore, nil
}

func (w workflowStub) RestoreRejectedReleases(ctx context.Context, libraryItemID int64) (database.RejectedReleaseRestoreResult, error) {
	return w.restoreAll, nil
}

func (w workflowStub) SkipRelease(ctx context.Context, releaseCandidateID int64) (workflow.ReleaseActionResult, error) {
	return w.skip, nil
}
func (w workflowStub) BackfillMetadata(_ context.Context) (workflow.BackfillMetadataResult, error) {
	return workflow.BackfillMetadataResult{}, nil
}
func (w workflowStub) ClearFailedQueue(_ context.Context) (int, error) { return 0, nil }
func (w workflowStub) FillMissingEpisodes(_ context.Context) (workflow.FillMissingEpisodesResult, error) {
	return workflow.FillMissingEpisodesResult{}, nil
}
func (w workflowStub) ManualSearch(_ context.Context, _ string) ([]workflow.ManualSearchItem, error) {
	return nil, nil
}
func (w workflowStub) ImportNZBFromPush(_ context.Context, _ []byte, _, _ string) (string, error) {
	return "", nil
}

func (p *publicationStub) RepublishLibraryItem(ctx context.Context, libraryItemID int64) error {
	p.republished = libraryItemID
	return nil
}

func (p *publicationStub) RepublishPendingLibrary(ctx context.Context) (library.BulkRepublishResult, error) {
	return p.pending, nil
}

func (maintenanceStub) RemoveOrphanedContent(ctx context.Context) (maintenance.Result, error) {
	return maintenance.Result{TaskName: "orphaned-content", DeletedFiles: 1}, nil
}

func (maintenanceStub) RemoveBrokenMediaSymlinks(ctx context.Context) (maintenance.Result, error) {
	return maintenance.Result{TaskName: "broken-media-symlinks", DeletedRows: 1}, nil
}

func (maintenanceStub) RemoveOrphanedCompletedSymlinks(ctx context.Context) (maintenance.Result, error) {
	return maintenance.Result{TaskName: "orphaned-completed-symlinks", DeletedRows: 1}, nil
}

func (cacheStub) Prune(ctx context.Context) (cache.PruneResult, error) {
	return cache.PruneResult{Root: "/mnt/drakkar/cache/blocks", FilesBefore: 5, FilesAfter: 3, DeletedFiles: 2}, nil
}

func (p probeStub) Probe(ctx context.Context) (probe.Report, error) {
	return p.report, nil
}

func (s subtitleStub) ListSubtitles(ctx context.Context, libraryItemID int64) ([]database.SubtitleFileSummary, error) {
	return s.items, nil
}

func (s subtitleStub) ListCandidates(ctx context.Context, libraryItemID int64) ([]database.SubtitleCandidateSummary, error) {
	return s.candidates, nil
}

func (s subtitleStub) SearchCandidates(ctx context.Context, libraryItemID int64, languages []string) (intsub.SearchResult, error) {
	return s.search, nil
}

func (s subtitleStub) DownloadCandidate(ctx context.Context, candidateID int64) (intsub.UploadResult, error) {
	return s.download, nil
}

func (s subtitleStub) UploadSubtitle(ctx context.Context, libraryItemID int64, language, fileName string, src io.Reader) (intsub.UploadResult, error) {
	body, _ := io.ReadAll(src)
	if len(body) == 0 {
		return intsub.UploadResult{}, io.EOF
	}
	if s.upload.Language != "" || len(s.upload.CreatedPaths) > 0 || s.upload.Provider != "" {
		return s.upload, nil
	}
	return intsub.UploadResult{
		LibraryItemID: libraryItemID,
		Language:      language,
		Provider:      "manual",
		CreatedPaths:  []string{fileName},
	}, nil
}

func (b *blocklistStub) List(ctx context.Context) ([]database.BlocklistItemSummary, error) {
	return b.items, nil
}

func (b *blocklistStub) Clear(ctx context.Context, id int64) error {
	b.cleared = id
	return nil
}

func (b *blocklistStub) ClearAll(ctx context.Context) (database.BlocklistClearResult, error) {
	return b.all, nil
}

func (b *blocklistStub) ClearByReason(ctx context.Context, reason string) (database.BlocklistClearResult, error) {
	return database.BlocklistClearResult{Cleared: 0}, nil
}

const sampleNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb>
  <file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000">
    <groups><group>alt.binaries.movies</group></groups>
    <segments>
      <segment bytes="1000" number="1">&lt;msg1&gt;</segment>
    </segments>
  </file>
</nzb>`

func TestImportNZBEndpoint(t *testing.T) {
	queueSvc := queue.NewService(queue.NewMemoryRepository(), nzb.NewImporter(t.TempDir(), 1024*1024))
	router := Router(statusStub{}, queueSvc, nil, nil, nil, nil, nil, nil, nil, nil, NewEventBroker(), nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/nzbs/import", strings.NewReader(sampleNZB))
	req.Header.Set("Content-Disposition", `attachment; filename="dune.nzb"`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var item database.QueueSnapshot
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&item); err != nil {
		t.Fatal(err)
	}
	if item.State != database.QueuePreflight {
		t.Fatalf("unexpected state %s", item.State)
	}
}

func (s *subtitleStub) DeleteSubtitle(ctx context.Context, subtitleID int64) error {
	s.deleted = subtitleID
	return nil
}

func TestCancelNZBEndpoint(t *testing.T) {
	queueSvc := queue.NewService(queue.NewMemoryRepository(), nzb.NewImporter(t.TempDir(), 1024*1024))
	item, err := queueSvc.ImportNZB(context.Background(), "dune.nzb", strings.NewReader(sampleNZB))
	if err != nil {
		t.Fatal(err)
	}
	router := Router(statusStub{}, queueSvc, nil, nil, nil, nil, nil, nil, nil, nil, NewEventBroker(), nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/nzbs/"+itoa(*item.NZBDocumentID), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	listReq := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	body, _ := io.ReadAll(listRec.Body)
	if !strings.Contains(string(body), `"failureReason":"cancelled"`) {
		t.Fatalf("unexpected queue body %s", string(body))
	}
}

func TestLibraryEndpoints(t *testing.T) {
	queueSvc := queue.NewService(queue.NewMemoryRepository(), nzb.NewImporter(t.TempDir(), 1024*1024))
	item, err := queueSvc.ImportNZB(context.Background(), "dune.nzb", strings.NewReader(sampleNZB))
	if err != nil {
		t.Fatal(err)
	}
	router := Router(statusStub{}, queueSvc, nil, nil, nil, nil, nil, nil, nil, nil, NewEventBroker(), nil, nil, nil, nil, nil, nil, nil, nil)

	libraryReq := httptest.NewRequest(http.MethodGet, "/api/library", nil)
	libraryRec := httptest.NewRecorder()
	router.ServeHTTP(libraryRec, libraryReq)
	if libraryRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", libraryRec.Code)
	}
	if !strings.Contains(libraryRec.Body.String(), `"title":"dune.nzb"`) {
		t.Fatalf("unexpected library body %s", libraryRec.Body.String())
	}

	releasesReq := httptest.NewRequest(http.MethodGet, "/api/releases/"+itoa(item.LibraryItemID), nil)
	releasesRec := httptest.NewRecorder()
	router.ServeHTTP(releasesRec, releasesReq)
	if releasesRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", releasesRec.Code)
	}
	if !strings.Contains(releasesRec.Body.String(), `"selected":true`) {
		t.Fatalf("unexpected releases body %s", releasesRec.Body.String())
	}

	missingReq := httptest.NewRequest(http.MethodGet, "/api/library/missing", nil)
	missingRec := httptest.NewRecorder()
	router.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", missingRec.Code)
	}
	if !strings.Contains(missingRec.Body.String(), `"available":false`) {
		t.Fatalf("unexpected missing body %s", missingRec.Body.String())
	}
}

func TestStatusFromConfigIncludesIntegrationReadiness(t *testing.T) {
	rt := config.DefaultRuntime()
	cfg := config.Settings{
		Database: config.DatabaseConfig{Host: "postgres", Port: 5432, Name: "drakkar", Username: "drakkar", Password: "secret"},
		Valkey:   config.ValkeyConfig{Host: "valkey", Port: 6379},
		NZBHydra2: config.ServiceConfig{
			URL: "http://nzbhydra2:5076",
		},
		Seerr: config.ServiceConfig{
			URL: "http://seerr:5055",
		},
		Usenet: config.UsenetConfig{
			Providers: []config.UsenetProvider{
				{Name: "primary", Enabled: true, Host: "", Username: "", Password: "", Port: 563, MaxConnections: 20},
			},
		},
		Metadata: config.MetadataConfig{
			TMDB: config.APIKeyConfig{APIKey: "tmdb-key"},
		},
		Subtitles: config.SubtitlesConfig{
			Enabled:   true,
			Languages: []string{"en"},
			Providers: map[string]config.SubtitleAuth{
				"subdl": {Enabled: true},
			},
		},
	}

	status := StatusFromConfig(rt, cfg, time.Unix(1710000000, 0).UTC(), true)

	if !status.Integrations.TMDB.Configured {
		t.Fatalf("expected tmdb configured")
	}
	if status.Integrations.Seerr.Configured {
		t.Fatalf("expected seerr unconfigured without api key")
	}
	if status.Integrations.NZBHydra2.Configured {
		t.Fatalf("expected hydra unconfigured without api key")
	}
	if status.Integrations.Subtitles.Configured {
		t.Fatalf("expected subtitles unconfigured without provider credentials")
	}
	if status.Integrations.SubtitleProviders["subdl"].Configured {
		t.Fatalf("expected subdl unconfigured without api key")
	}
	if status.Integrations.Usenet.Configured {
		t.Fatalf("expected usenet unconfigured without host and credentials")
	}
}

func TestWorkflowEndpoints(t *testing.T) {
	queueSvc := queue.NewService(queue.NewMemoryRepository(), nzb.NewImporter(t.TempDir(), 1024*1024))
	workflowSvc := workflowStub{
		requests:   []database.MediaRequestSummary{{ID: 1, ExternalID: "123", RequestType: "movie", Title: "Dune", MediaType: "movie"}},
		sync:       workflow.SyncResult{Seen: 2, Created: 1},
		pending:    workflow.BulkSearchResult{Processed: 2, Searched: 2, Selected: 1, Failed: 0},
		retryAll:   workflow.BulkQueueRetryResult{Processed: 3, Retried: 2, Failed: 1},
		search:     workflow.SearchResult{LibraryItemID: 42, Query: "Dune 2021", CandidateCount: 3},
		selectr:    workflow.ReleaseActionResult{ReleaseCandidateID: 7, Action: "selected", SelectedReleaseID: func() *int64 { v := int64(88); return &v }()},
		reject:     workflow.ReleaseActionResult{ReleaseCandidateID: 8, Action: "rejected", SelectedReleaseID: func() *int64 { v := int64(89); return &v }()},
		restore:    workflow.ReleaseActionResult{ReleaseCandidateID: 8, Action: "restored"},
		restoreAll: database.RejectedReleaseRestoreResult{LibraryItemID: 42, Restored: 2},
		skip:       workflow.ReleaseActionResult{ReleaseCandidateID: 8, Action: "skipped", SelectedReleaseID: func() *int64 { v := int64(91); return &v }()},
		retry:      workflow.QueueRetryResult{QueueItemID: 4, Action: "retried_selected_release", SelectedReleaseID: func() *int64 { v := int64(90); return &v }()},
	}
	pub := &publicationStub{}
	pub.pending = library.BulkRepublishResult{Processed: 2, Republished: 1, Failed: 1}
	maint := maintenanceStub{}
	cacheSvc := cacheStub{}
	subtitles := &subtitleStub{
		items:      []database.SubtitleFileSummary{{ID: 1, LibraryItemID: 42, Provider: "manual", Language: "en", Path: "/mnt/drakkar/media/movies/Dune (2021) {tmdb-438631}/Dune (2021).en.srt", CreatedAt: time.Now().UTC()}},
		candidates: []database.SubtitleCandidateSummary{{ID: 3, LibraryItemID: 42, Provider: "subdl", Language: "en", Title: "Dune.2021.en.srt", ReleaseName: "Dune.2021.1080p.WEB-DL", Format: "srt", Score: 155, ExternalID: "file123", CreatedAt: time.Now().UTC()}},
		search: intsub.SearchResult{
			LibraryItemID:  42,
			CandidateCount: 1,
		},
		download: intsub.UploadResult{
			LibraryItemID: 42,
			Language:      "en",
			Provider:      "subdl",
			CreatedPaths:  []string{"/mnt/drakkar/media/movies/Dune (2021) {tmdb-438631}/Dune (2021).en.srt"},
		},
		upload: intsub.UploadResult{
			LibraryItemID: 42,
			Language:      "en",
			Provider:      "manual",
			CreatedPaths:  []string{"/mnt/drakkar/media/movies/Dune (2021) {tmdb-438631}/Dune (2021).en.srt"},
		},
	}
	blocklist := &blocklistStub{
		items: []database.BlocklistItemSummary{{ID: 9, Key: "external_url:http://example/blocked.nzb", Reason: "manual_reject"}},
		all:   database.BlocklistClearResult{Cleared: 1},
	}
	probes := probeStub{report: probe.Report{
		CheckedAt: time.Now().UTC(),
		Results: []probe.Result{
			{Name: "seerr", OK: true, Detail: "ok", CheckedAt: time.Now().UTC(), DurationMS: 12},
		},
	}}
	router := Router(statusStub{}, queueSvc, workflowSvc, pub, maint, cacheSvc, subtitles, blocklist, probes, nil, NewEventBroker(), nil, nil, nil, nil, nil, nil, nil, nil)

	requestsReq := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
	requestsRec := httptest.NewRecorder()
	router.ServeHTTP(requestsRec, requestsReq)
	if requestsRec.Code != http.StatusOK || !strings.Contains(requestsRec.Body.String(), `"externalId":"123"`) {
		t.Fatalf("unexpected requests response %d %s", requestsRec.Code, requestsRec.Body.String())
	}

	syncReq := httptest.NewRequest(http.MethodPost, "/api/requests/sync", nil)
	syncRec := httptest.NewRecorder()
	router.ServeHTTP(syncRec, syncReq)
	if syncRec.Code != http.StatusAccepted || !strings.Contains(syncRec.Body.String(), `"created":1`) {
		t.Fatalf("unexpected sync response %d %s", syncRec.Code, syncRec.Body.String())
	}

	pendingReq := httptest.NewRequest(http.MethodPost, "/api/library/search-pending", nil)
	pendingRec := httptest.NewRecorder()
	router.ServeHTTP(pendingRec, pendingReq)
	if pendingRec.Code != http.StatusAccepted || !strings.Contains(pendingRec.Body.String(), `"processed":2`) {
		t.Fatalf("unexpected pending search response %d %s", pendingRec.Code, pendingRec.Body.String())
	}

	retryReq := httptest.NewRequest(http.MethodPost, "/api/queue/4/retry", nil)
	retryRec := httptest.NewRecorder()
	router.ServeHTTP(retryRec, retryReq)
	if retryRec.Code != http.StatusAccepted || !strings.Contains(retryRec.Body.String(), `"action":"retried_selected_release"`) {
		t.Fatalf("unexpected retry response %d %s", retryRec.Code, retryRec.Body.String())
	}

	retryAllReq := httptest.NewRequest(http.MethodPost, "/api/queue/retry-failed", nil)
	retryAllRec := httptest.NewRecorder()
	router.ServeHTTP(retryAllRec, retryAllReq)
	if retryAllRec.Code != http.StatusAccepted || !strings.Contains(retryAllRec.Body.String(), `"processed":3`) {
		t.Fatalf("unexpected bulk retry response %d %s", retryAllRec.Code, retryAllRec.Body.String())
	}

	searchReq := httptest.NewRequest(http.MethodPost, "/api/library/42/search", nil)
	searchRec := httptest.NewRecorder()
	router.ServeHTTP(searchRec, searchReq)
	if searchRec.Code != http.StatusAccepted || !strings.Contains(searchRec.Body.String(), `"candidateCount":3`) {
		t.Fatalf("unexpected search response %d %s", searchRec.Code, searchRec.Body.String())
	}

	selectReq := httptest.NewRequest(http.MethodPost, "/api/releases/7/select", nil)
	selectRec := httptest.NewRecorder()
	router.ServeHTTP(selectRec, selectReq)
	if selectRec.Code != http.StatusAccepted || !strings.Contains(selectRec.Body.String(), `"action":"selected"`) {
		t.Fatalf("unexpected select response %d %s", selectRec.Code, selectRec.Body.String())
	}

	rejectReq := httptest.NewRequest(http.MethodPost, "/api/releases/8/reject", strings.NewReader(`{"reason":"bad_release"}`))
	rejectRec := httptest.NewRecorder()
	router.ServeHTTP(rejectRec, rejectReq)
	if rejectRec.Code != http.StatusAccepted || !strings.Contains(rejectRec.Body.String(), `"action":"rejected"`) {
		t.Fatalf("unexpected reject response %d %s", rejectRec.Code, rejectRec.Body.String())
	}

	restoreReq := httptest.NewRequest(http.MethodPost, "/api/releases/8/restore", nil)
	restoreRec := httptest.NewRecorder()
	router.ServeHTTP(restoreRec, restoreReq)
	if restoreRec.Code != http.StatusAccepted || !strings.Contains(restoreRec.Body.String(), `"action":"restored"`) {
		t.Fatalf("unexpected restore response %d %s", restoreRec.Code, restoreRec.Body.String())
	}

	restoreAllReq := httptest.NewRequest(http.MethodPost, "/api/library/42/restore-rejected", nil)
	restoreAllRec := httptest.NewRecorder()
	router.ServeHTTP(restoreAllRec, restoreAllReq)
	if restoreAllRec.Code != http.StatusAccepted || !strings.Contains(restoreAllRec.Body.String(), `"restored":2`) {
		t.Fatalf("unexpected restore-all response %d %s", restoreAllRec.Code, restoreAllRec.Body.String())
	}

	skipReq := httptest.NewRequest(http.MethodPost, "/api/releases/8/skip", nil)
	skipRec := httptest.NewRecorder()
	router.ServeHTTP(skipRec, skipReq)
	if skipRec.Code != http.StatusAccepted || !strings.Contains(skipRec.Body.String(), `"action":"skipped"`) {
		t.Fatalf("unexpected skip response %d %s", skipRec.Code, skipRec.Body.String())
	}

	republishReq := httptest.NewRequest(http.MethodPost, "/api/library/42/republish", nil)
	republishRec := httptest.NewRecorder()
	router.ServeHTTP(republishRec, republishReq)
	if republishRec.Code != http.StatusAccepted || pub.republished != 42 {
		t.Fatalf("unexpected republish response %d %s", republishRec.Code, republishRec.Body.String())
	}

	republishPendingReq := httptest.NewRequest(http.MethodPost, "/api/library/republish-pending", nil)
	republishPendingRec := httptest.NewRecorder()
	router.ServeHTTP(republishPendingRec, republishPendingReq)
	if republishPendingRec.Code != http.StatusAccepted || !strings.Contains(republishPendingRec.Body.String(), `"processed":2`) {
		t.Fatalf("unexpected bulk republish response %d %s", republishPendingRec.Code, republishPendingRec.Body.String())
	}

	maintReq := httptest.NewRequest(http.MethodPost, "/api/maintenance/orphaned-content", nil)
	maintRec := httptest.NewRecorder()
	router.ServeHTTP(maintRec, maintReq)
	if maintRec.Code != http.StatusAccepted || !strings.Contains(maintRec.Body.String(), `"deletedFiles":1`) {
		t.Fatalf("unexpected maintenance response %d %s", maintRec.Code, maintRec.Body.String())
	}

	cacheReq := httptest.NewRequest(http.MethodPost, "/api/cache/prune", nil)
	cacheRec := httptest.NewRecorder()
	router.ServeHTTP(cacheRec, cacheReq)
	if cacheRec.Code != http.StatusAccepted || !strings.Contains(cacheRec.Body.String(), `"deletedFiles":2`) {
		t.Fatalf("unexpected cache response %d %s", cacheRec.Code, cacheRec.Body.String())
	}

	subtitleListReq := httptest.NewRequest(http.MethodGet, "/api/subtitles/42", nil)
	subtitleListRec := httptest.NewRecorder()
	router.ServeHTTP(subtitleListRec, subtitleListReq)
	if subtitleListRec.Code != http.StatusOK || !strings.Contains(subtitleListRec.Body.String(), `"language":"en"`) {
		t.Fatalf("unexpected subtitle list response %d %s", subtitleListRec.Code, subtitleListRec.Body.String())
	}

	subtitleCandidateListReq := httptest.NewRequest(http.MethodGet, "/api/subtitle-candidates/42", nil)
	subtitleCandidateListRec := httptest.NewRecorder()
	router.ServeHTTP(subtitleCandidateListRec, subtitleCandidateListReq)
	if subtitleCandidateListRec.Code != http.StatusOK || !strings.Contains(subtitleCandidateListRec.Body.String(), `"provider":"subdl"`) {
		t.Fatalf("unexpected subtitle candidate list response %d %s", subtitleCandidateListRec.Code, subtitleCandidateListRec.Body.String())
	}

	subtitleSearchReq := httptest.NewRequest(http.MethodPost, "/api/subtitles/42/search", strings.NewReader(`{"languages":["en"]}`))
	subtitleSearchRec := httptest.NewRecorder()
	router.ServeHTTP(subtitleSearchRec, subtitleSearchReq)
	if subtitleSearchRec.Code != http.StatusAccepted || !strings.Contains(subtitleSearchRec.Body.String(), `"candidateCount":1`) {
		t.Fatalf("unexpected subtitle search response %d %s", subtitleSearchRec.Code, subtitleSearchRec.Body.String())
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("language", "en")
	part, err := writer.CreateFormFile("file", "subtitle.srt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(part, strings.NewReader("1\n00:00:01,000 --> 00:00:02,000\nHello\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	subtitleUploadReq := httptest.NewRequest(http.MethodPost, "/api/subtitles/42/upload", &body)
	subtitleUploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	subtitleUploadRec := httptest.NewRecorder()
	router.ServeHTTP(subtitleUploadRec, subtitleUploadReq)
	if subtitleUploadRec.Code != http.StatusCreated || !strings.Contains(subtitleUploadRec.Body.String(), `"provider":"manual"`) {
		t.Fatalf("unexpected subtitle upload response %d %s", subtitleUploadRec.Code, subtitleUploadRec.Body.String())
	}

	subtitleDownloadReq := httptest.NewRequest(http.MethodPost, "/api/subtitle-candidates/3/download", nil)
	subtitleDownloadRec := httptest.NewRecorder()
	router.ServeHTTP(subtitleDownloadRec, subtitleDownloadReq)
	if subtitleDownloadRec.Code != http.StatusCreated || !strings.Contains(subtitleDownloadRec.Body.String(), `"provider":"subdl"`) {
		t.Fatalf("unexpected subtitle download response %d %s", subtitleDownloadRec.Code, subtitleDownloadRec.Body.String())
	}

	subtitleDeleteReq := httptest.NewRequest(http.MethodDelete, "/api/subtitle-files/1", nil)
	subtitleDeleteRec := httptest.NewRecorder()
	router.ServeHTTP(subtitleDeleteRec, subtitleDeleteReq)
	if subtitleDeleteRec.Code != http.StatusOK || !strings.Contains(subtitleDeleteRec.Body.String(), `"status":"deleted"`) || subtitles.deleted != 1 {
		t.Fatalf("unexpected subtitle delete response %d %s deleted=%d", subtitleDeleteRec.Code, subtitleDeleteRec.Body.String(), subtitles.deleted)
	}

	blocklistReq := httptest.NewRequest(http.MethodGet, "/api/blocklist", nil)
	blocklistRec := httptest.NewRecorder()
	router.ServeHTTP(blocklistRec, blocklistReq)
	if blocklistRec.Code != http.StatusOK || !strings.Contains(blocklistRec.Body.String(), `"manual_reject"`) {
		t.Fatalf("unexpected blocklist response %d %s", blocklistRec.Code, blocklistRec.Body.String())
	}

	blocklistClearAllReq := httptest.NewRequest(http.MethodDelete, "/api/blocklist", nil)
	blocklistClearAllRec := httptest.NewRecorder()
	router.ServeHTTP(blocklistClearAllRec, blocklistClearAllReq)
	if blocklistClearAllRec.Code != http.StatusOK || !strings.Contains(blocklistClearAllRec.Body.String(), `"cleared":1`) {
		t.Fatalf("unexpected blocklist clear-all response %d %s", blocklistClearAllRec.Code, blocklistClearAllRec.Body.String())
	}

	clearReq := httptest.NewRequest(http.MethodDelete, "/api/blocklist/9", nil)
	clearRec := httptest.NewRecorder()
	router.ServeHTTP(clearRec, clearReq)
	if clearRec.Code != http.StatusOK || blocklist.cleared != 9 {
		t.Fatalf("unexpected blocklist clear response %d %s", clearRec.Code, clearRec.Body.String())
	}

	probeReq := httptest.NewRequest(http.MethodPost, "/api/integrations/probe", nil)
	probeRec := httptest.NewRecorder()
	router.ServeHTTP(probeRec, probeReq)
	if probeRec.Code != http.StatusAccepted || !strings.Contains(probeRec.Body.String(), `"name":"seerr"`) {
		t.Fatalf("unexpected probe response %d %s", probeRec.Code, probeRec.Body.String())
	}
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

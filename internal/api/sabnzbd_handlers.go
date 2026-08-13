package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rs/zerolog"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/nzb"
)

const sabVersion = "4.5.1"

// sabRepository is the persistence dependency of sabHandler: listing queue
// and history items and dismissing ones the client has already consumed.
type sabRepository interface {
	ListSabQueueItems(ctx context.Context, category string, start, limit int) ([]database.SabQueueItem, int, error)
	ListSabHistoryItems(ctx context.Context, category string, start, limit int) ([]database.SabHistoryItem, int, error)
	DismissSabItems(ctx context.Context, libraryItemIDs []int64) error
}

type sabSecurityConfig struct {
	enabled             bool
	trustedUpstreamURLs []string
}

// sabHandler implements a SABnzbd-compatible HTTP API so download-client
// integrations that only speak the SABnzbd protocol (Sonarr, Radarr) can
// operate against Drakkar without any changes on their end.
type sabHandler struct {
	importFn      func(ctx context.Context, content io.Reader, filename, mediaType string) (string, error)
	repo          sabRepository
	fuseMountPath string
	log           zerolog.Logger
	// loadSecurity reads current settings for every request so enablement and
	// upstream changes apply without restarting Drakkar. enabled and
	// trustedUpstreamURLs support isolated handler tests.
	loadSecurity        func(ctx context.Context) (sabSecurityConfig, error)
	enabled             bool
	trustedUpstreamURLs []string
	// authenticateToken validates SABnzbd's apikey against the same hashed API
	// token store used by Seerr webhooks and normal Bearer authentication.
	authenticateToken func(ctx context.Context, rawToken string) bool
	// claimURLForFetch provides handleAddURL the exact same atomic per-URL
	// fetch claim workflow.Service's own dispatch pipeline uses
	// (Service.ClaimURLForFetch, shared by fetchIndexAndRelease/
	// fetchAndImportSelectedReleaseDepth), so a Radarr/Sonarr addurl retry --
	// its own download-client retry logic, or a resubmission after Drakkar
	// restarts mid-request -- doesn't trigger a second live NZB fetch from
	// the indexer for the same URL. Returns true if the caller must skip
	// (someone already holds a live claim on this URL). This handler used to
	// call two separate, in-memory-only functions (recentlyDispatchedURL/
	// markURLDispatched) -- found 2026-07-18 to have no restart-surviving
	// (Postgres-persisted) coverage at all, unlike the two internal fetch
	// call sites, directly contradicting this field's own prior doc comment.
	// Nil-safe: a nil claimURLForFetch always proceeds, matching the
	// historical no-guard behavior.
	claimURLForFetch func(ctx context.Context, rawURL string) bool
	// fetchFn defaults to fetchRemoteURL; overridable in tests.
	fetchFn func(ctx context.Context, rawURL string) ([]byte, error)
}

func (h *sabHandler) fetchRemote(ctx context.Context, rawURL string, trustedUpstreamURLs []string) ([]byte, error) {
	if h.fetchFn != nil {
		return h.fetchFn(ctx, rawURL)
	}
	return fetchRemoteURLFromUpstreams(ctx, rawURL, trustedUpstreamURLs)
}

func (h *sabHandler) securityConfig(ctx context.Context) (sabSecurityConfig, error) {
	if h.loadSecurity != nil {
		return h.loadSecurity(ctx)
	}
	return sabSecurityConfig{enabled: h.enabled, trustedUpstreamURLs: h.trustedUpstreamURLs}, nil
}

// ServeHTTP implements the SABnzbd HTTP API: every operation is dispatched
// by a single "mode" query/form parameter, matching SABnzbd's own endpoint
// design so Sonarr/Radarr's SABnzbd download-client integration works
// unmodified against Drakkar. When enabled, every request must present a
// valid Drakkar API token via SABnzbd's "apikey" query parameter.
func (h *sabHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	security, err := h.securityConfig(r.Context())
	if err != nil {
		h.log.Error().Err(err).Msg("sabnzbd: load security settings")
		h.writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{"status": false, "error": "SAB API authentication unavailable"})
		return
	}
	if !security.enabled {
		h.writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{"status": false, "error": "SAB API is disabled"})
		return
	}
	if h.authenticateToken == nil {
		h.writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{"status": false, "error": "SAB API authentication unavailable"})
		return
	}
	presentedKey := r.URL.Query().Get("apikey")
	if !h.authenticateToken(r.Context(), presentedKey) {
		h.writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"status": false, "error": "API Key Incorrect"})
		return
	}
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	switch {
	case strings.HasPrefix(contentType, "multipart/form-data"):
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			h.writeRequestError(w, "parse multipart form", err)
			return
		}
		defer r.MultipartForm.RemoveAll()
	case strings.HasPrefix(contentType, "application/x-www-form-urlencoded"):
		if err := r.ParseForm(); err != nil {
			h.writeRequestError(w, "parse form", err)
			return
		}
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = r.FormValue("mode")
	}
	switch mode {
	case "version":
		h.writeJSON(w, map[string]any{"version": sabVersion, "status": true})
	case "status":
		h.writeJSON(w, map[string]any{"status": true})
	case "fullstatus":
		// SABnzbd spec: {"status": {"completedir": "..."}}
		h.writeJSON(w, map[string]any{
			"status": map[string]any{
				"completedir": filepath.Join(h.fuseMountPath, "content"),
			},
		})
	case "get_cats":
		h.writeJSON(w, map[string]any{"categories": []string{"movies", "tv"}})
	case "get_config":
		h.handleGetConfig(w, r)
	case "addfile":
		h.handleAddFile(w, r)
	case "addurl":
		h.handleAddURL(w, r, security.trustedUpstreamURLs)
	case "queue":
		name := r.FormValue("name")
		if name == "" {
			name = r.URL.Query().Get("name")
		}
		if name == "delete" {
			h.handleQueueDelete(w, r)
		} else {
			h.handleQueue(w, r)
		}
	case "history":
		name := r.FormValue("name")
		if name == "" {
			name = r.URL.Query().Get("name")
		}
		if name == "delete" {
			h.handleHistoryDelete(w, r)
		} else {
			h.handleHistory(w, r)
		}
	default:
		h.writeError(w, "invalid mode: "+mode)
	}
}

// handleGetConfig implements SABnzbd's get_config mode, reporting just
// enough of the config shape (complete/download dirs, categories) for
// Sonarr/Radarr's download-client connection test and category setup to
// succeed.
func (h *sabHandler) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	completeDir := filepath.Join(h.fuseMountPath, "content")
	h.writeJSON(w, map[string]any{
		"status": true,
		"config": map[string]any{
			"misc": map[string]any{
				"complete_dir": completeDir,
				"download_dir": completeDir,
				"version":      sabVersion,
			},
			"categories": []map[string]any{
				{"name": "movies", "order": 0, "pp": "3", "script": "None", "dir": "movies", "priority": -100},
				{"name": "tv", "order": 1, "pp": "3", "script": "None", "dir": "tv", "priority": -100},
			},
		},
	})
}

// handleAddFile implements SABnzbd's addfile mode: a multipart NZB upload.
// It accepts the file under any of nzbFile, nzbfile, or name, since
// SABnzbd-client implementations disagree on the field name.
func (h *sabHandler) handleAddFile(w http.ResponseWriter, r *http.Request) {
	if r.MultipartForm == nil {
		h.writeError(w, "multipart form required")
		return
	}
	file := r.MultipartForm.File["nzbFile"]
	if len(file) == 0 {
		file = r.MultipartForm.File["nzbfile"]
	}
	if len(file) == 0 {
		file = r.MultipartForm.File["name"]
	}
	if len(file) == 0 {
		h.writeError(w, "missing nzbFile, nzbfile, or name field")
		return
	}
	fh := file[0]
	f, err := fh.Open()
	if err != nil {
		h.writeError(w, "open upload: "+err.Error())
		return
	}
	defer f.Close()
	filename := multipartFilename(r, fh.Filename)
	nzoID, err := h.importFn(r.Context(), f, filename, catToMediaType(sabCategory(r)))
	if err != nil {
		h.writeRequestError(w, "import", err)
		return
	}
	h.writeJSON(w, map[string]any{"status": true, "nzo_ids": []string{nzoID}})
}

// handleAddURL allows public destinations plus exact configured upstream
// authorities. Private redirects to any other service remain blocked.
func (h *sabHandler) handleAddURL(w http.ResponseWriter, r *http.Request, trustedUpstreamURLs []string) {
	nzbURL := r.FormValue("name")
	if nzbURL == "" {
		nzbURL = r.URL.Query().Get("name")
	}
	if nzbURL == "" {
		h.writeError(w, "missing name (URL) param")
		return
	}
	nzbName := r.FormValue("nzbname")
	if nzbName == "" {
		nzbName = r.URL.Query().Get("nzbname")
	}

	if h.claimURLForFetch != nil && h.claimURLForFetch(r.Context(), nzbURL) {
		h.writeError(w, "recently added, skipping duplicate fetch")
		return
	}

	content, err := h.fetchRemote(r.Context(), nzbURL, trustedUpstreamURLs)
	if err != nil {
		h.writeError(w, err.Error())
		return
	}

	filename := nzbName
	if filename == "" {
		filename = filenameFromURL(nzbURL)
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".nzb") {
		filename += ".nzb"
	}

	nzoID, err := h.importFn(r.Context(), bytes.NewReader(content), filename, catToMediaType(sabCategory(r)))
	if err != nil {
		h.writeRequestError(w, "import", err)
		return
	}
	h.writeJSON(w, map[string]any{"status": true, "nzo_ids": []string{nzoID}})
}

// handleQueue implements SABnzbd's queue mode, listing in-progress items as
// "slots" in the shape Sonarr/Radarr expect. Progress fields (percentage,
// timeleft, mb/mbleft) are always reported as zero/unknown.
func (h *sabHandler) handleQueue(w http.ResponseWriter, r *http.Request) {
	start := intParam(r, "start", 0)
	limit := intParam(r, "limit", 100)

	items, total, err := h.repo.ListSabQueueItems(r.Context(), sabCategory(r), start, limit)
	if err != nil {
		h.writeError(w, err.Error())
		return
	}

	slots := make([]map[string]any, 0, len(items))
	for i, it := range items {
		slots = append(slots, map[string]any{
			"index":           i,
			"nzo_id":          fmt.Sprintf("item-%d", it.LibraryItemID),
			"priority":        "Normal",
			"filename":        it.Title + ".nzb",
			"cat":             mediaTypeToCat(it.MediaType),
			"percentage":      "0",
			"true_percentage": "0",
			"status":          sabQueueStatus(it.State),
			"timeleft":        "0:0:0:0",
			"mb":              "0.00",
			"mbleft":          "0.00",
		})
	}
	h.writeJSON(w, map[string]any{
		"status": true,
		"queue": map[string]any{
			"paused":    false,
			"slots":     slots,
			"noofslots": total,
		},
	})
}

// handleQueueDelete dismisses in-progress items from future queue polls.
func (h *sabHandler) handleQueueDelete(w http.ResponseWriter, r *http.Request) {
	ids := parseSabNzoIDs(r)
	if len(ids) > 0 {
		if err := h.repo.DismissSabItems(r.Context(), ids); err != nil {
			h.log.Warn().Err(err).Msg("sabnzbd: queue delete failed")
		}
	}
	h.writeJSON(w, map[string]any{"status": true})
}

// handleHistory implements SABnzbd's history mode, listing completed/failed
// items as "slots". The "storage" field points at the imported release's
// path under fuseMountPath — how Sonarr/Radarr locate the file to import —
// and is only populated once the item reaches an available/degraded state.
// An optional nzo_ids param restricts the result to specific items.
func (h *sabHandler) handleHistory(w http.ResponseWriter, r *http.Request) {
	start := intParam(r, "start", 0)
	limit := intParam(r, "limit", 100)

	items, total, err := h.repo.ListSabHistoryItems(r.Context(), sabCategory(r), start, limit)
	if err != nil {
		h.writeError(w, err.Error())
		return
	}

	// Optional nzo_ids filter: Radarr/Sonarr may request specific items by ID.
	filterIDs := parseSabNzoIDSet(r)

	slots := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if len(filterIDs) > 0 {
			if _, ok := filterIDs[it.LibraryItemID]; !ok {
				continue
			}
		}
		storage := ""
		sabStatus := "Failed"
		failMsg := it.FailureReason
		if it.State == string(database.QueueAvailable) || it.State == string(database.QueueDegraded) {
			sabStatus = "Completed"
			failMsg = ""
			if it.SelectedReleaseID > 0 {
				storage = filepath.Join(h.fuseMountPath, "content", "releases", strconv.FormatInt(it.SelectedReleaseID, 10))
			}
		}
		slots = append(slots, map[string]any{
			"nzo_id":        fmt.Sprintf("item-%d", it.LibraryItemID),
			"nzb_name":      it.Title + ".nzb",
			"name":          it.Title,
			"category":      mediaTypeToCat(it.MediaType),
			"status":        sabStatus,
			"bytes":         it.TotalBytes,
			"storage":       storage,
			"download_time": 0,
			"fail_message":  failMsg,
		})
	}
	h.writeJSON(w, map[string]any{
		"status": true,
		"history": map[string]any{
			"slots":     slots,
			"noofslots": total,
		},
	})
}

// handleHistoryDelete dismisses completed/failed items from future history polls.
// Radarr/Sonarr send this after successfully importing a downloaded release.
func (h *sabHandler) handleHistoryDelete(w http.ResponseWriter, r *http.Request) {
	ids := parseSabNzoIDs(r)
	if len(ids) > 0 {
		if err := h.repo.DismissSabItems(r.Context(), ids); err != nil {
			h.log.Warn().Err(err).Msg("sabnzbd: history delete failed")
		}
	}
	h.writeJSON(w, map[string]any{"status": true})
}

func (h *sabHandler) writeJSON(w http.ResponseWriter, v any) {
	h.writeJSONStatus(w, http.StatusOK, v)
}

func (h *sabHandler) writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.log.Error().Err(err).Msg("sabnzbd: encode response")
	}
}

// writeError logs the failure and writes it back in SABnzbd's
// {"status":false,"error":...} error shape.
func (h *sabHandler) writeError(w http.ResponseWriter, msg string) {
	h.log.Warn().Str("error", msg).Msg("sabnzbd api error")
	h.writeJSONStatus(w, http.StatusBadRequest, map[string]any{"status": false, "error": msg})
}

func (h *sabHandler) writeRequestError(w http.ResponseWriter, operation string, err error) {
	if isRequestBodyTooLarge(err) || errors.Is(err, nzb.ErrUploadTooLarge) {
		h.log.Warn().Str("operation", operation).Msg("sabnzbd request body too large")
		h.writeJSONStatus(w, http.StatusRequestEntityTooLarge, map[string]any{
			"status": false,
			"error":  errRequestBodyTooLarge.Error(),
		})
		return
	}
	h.writeError(w, operation+": "+err.Error())
}

// parseSabNzoIDs reads one or more `value` params (Radarr/Sonarr send each id
// as a separate value=item-42 param) and returns libraryItemIDs.
func parseSabNzoIDs(r *http.Request) []int64 {
	if err := r.ParseForm(); err != nil {
		return nil
	}
	var ids []int64
	for _, v := range r.Form["value"] {
		if id, ok := nzoIDToLibraryItemID(v); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// parseSabNzoIDSet reads the `nzo_ids` param (comma-separated) used by
// Radarr/Sonarr to filter history results to specific items.
func parseSabNzoIDSet(r *http.Request) map[int64]struct{} {
	raw := r.FormValue("nzo_ids")
	if raw == "" {
		raw = r.URL.Query().Get("nzo_ids")
	}
	if raw == "" {
		return nil
	}
	out := make(map[int64]struct{})
	for _, part := range strings.Split(raw, ",") {
		if id, ok := nzoIDToLibraryItemID(strings.TrimSpace(part)); ok {
			out[id] = struct{}{}
		}
	}
	return out
}

// nzoIDToLibraryItemID parses "item-<n>" → n.
func nzoIDToLibraryItemID(nzoID string) (int64, bool) {
	s, ok := strings.CutPrefix(nzoID, "item-")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

func catToMediaType(cat string) string {
	switch cat {
	case "movies":
		return "movie"
	case "tv":
		return "tv"
	default:
		return ""
	}
}

func mediaTypeToCat(mediaType string) string {
	switch mediaType {
	case "movie":
		return "movies"
	case "tv", "episode", "manual_nzb":
		return "tv"
	default:
		return mediaType
	}
}

// sabQueueStatus collapses Drakkar's internal queue states into the two
// coarse statuses SABnzbd clients distinguish: "Queued" while searching or
// ranking, "Downloading" for everything else in progress.
func sabQueueStatus(state string) string {
	switch database.QueueState(state) {
	case database.QueueSearching, database.QueueRanking:
		return "Queued"
	default:
		return "Downloading"
	}
}

func filenameFromURL(rawURL string) string {
	parts := strings.Split(rawURL, "/")
	if len(parts) > 0 {
		name := parts[len(parts)-1]
		if idx := strings.Index(name, "?"); idx >= 0 {
			name = name[:idx]
		}
		if name != "" {
			return name
		}
	}
	return "download.nzb"
}

// multipartFilename resolves the NZB filename for an addfile upload from the
// nzbname param, falling back to the uploaded file's own name, and always
// guarantees a .nzb suffix.
func multipartFilename(r *http.Request, fallback string) string {
	name := strings.TrimSpace(r.FormValue("nzbname"))
	if name == "" {
		name = strings.TrimSpace(r.URL.Query().Get("nzbname"))
	}
	if name == "" {
		name = fallback
	}
	if !strings.HasSuffix(strings.ToLower(name), ".nzb") {
		name += ".nzb"
	}
	return name
}

// sabCategory reads the category from whichever of "cat"/"category" (form or
// query) the client used — SABnzbd clients are inconsistent about which
// param name and transport they send it with.
func sabCategory(r *http.Request) string {
	cat := strings.TrimSpace(r.FormValue("cat"))
	if cat == "" {
		cat = strings.TrimSpace(r.URL.Query().Get("cat"))
	}
	if cat == "" {
		cat = strings.TrimSpace(r.FormValue("category"))
	}
	if cat == "" {
		cat = strings.TrimSpace(r.URL.Query().Get("category"))
	}
	return cat
}

func intParam(r *http.Request, key string, defaultVal int) int {
	s := r.FormValue(key)
	if s == "" {
		s = r.URL.Query().Get(key)
	}
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return defaultVal
}

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rs/zerolog"

	"github.com/drakkar-media/drakkar/internal/database"
)

const sabVersion = "4.5.1"

type sabRepository interface {
	ListSabQueueItems(ctx context.Context, category string, start, limit int) ([]database.SabQueueItem, int, error)
	ListSabHistoryItems(ctx context.Context, category string, start, limit int) ([]database.SabHistoryItem, int, error)
	DismissSabItems(ctx context.Context, libraryItemIDs []int64) error
}

type sabHandler struct {
	importFn      func(ctx context.Context, content []byte, filename, mediaType string) (string, error)
	repo          sabRepository
	fuseMountPath string
	log           zerolog.Logger
	// apiKey, if non-empty, must be supplied by callers via the "apikey" param
	// (matching real SABnzbd behavior). Empty preserves the historical
	// no-auth behavior for existing Sonarr/Radarr download-client configs.
	apiKey string
	// recentlyDispatchedURL/markURLDispatched provide handleAddURL the same
	// per-URL fetch cooldown workflow.Service's own dispatch pipeline uses
	// (fetchIndexAndRelease/fetchAndImportSelectedReleaseDepth), so a
	// Radarr/Sonarr addurl retry -- its own download-client retry logic, or
	// a resubmission after Drakkar restarts mid-request -- doesn't trigger a
	// second live NZB fetch from the indexer for the same URL. This handler
	// has no access to workflow.Service's private cooldown map, so these are
	// injected as plain functions. Nil-safe: a nil recentlyDispatchedURL
	// always proceeds, matching the historical no-guard behavior.
	recentlyDispatchedURL func(rawURL string) bool
	markURLDispatched     func(rawURL string)
	// fetchFn defaults to fetchRemoteURL; overridable in tests.
	fetchFn func(ctx context.Context, rawURL string) ([]byte, error)
}

func (h *sabHandler) fetchRemote(ctx context.Context, rawURL string) ([]byte, error) {
	if h.fetchFn != nil {
		return h.fetchFn(ctx, rawURL)
	}
	return fetchRemoteURL(ctx, rawURL)
}

func (h *sabHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.apiKey != "" {
		key := r.FormValue("apikey")
		if key == "" {
			key = r.URL.Query().Get("apikey")
		}
		if key != h.apiKey {
			h.writeJSON(w, map[string]any{"status": false, "error": "API Key Incorrect"})
			return
		}
	}
	mode := r.FormValue("mode")
	if mode == "" {
		mode = r.URL.Query().Get("mode")
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
		h.handleAddURL(w, r)
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"status":false,"error":"invalid mode: %s"}`, mode)
	}
}

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

func (h *sabHandler) handleAddFile(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		h.writeError(w, "parse multipart form: "+err.Error())
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
	content, err := io.ReadAll(f)
	if err != nil {
		h.writeError(w, "read upload: "+err.Error())
		return
	}
	filename := multipartFilename(r, fh.Filename)
	nzoID, err := h.importFn(r.Context(), content, filename, catToMediaType(sabCategory(r)))
	if err != nil {
		h.writeError(w, "import: "+err.Error())
		return
	}
	h.writeJSON(w, map[string]any{"status": true, "nzo_ids": []string{nzoID}})
}

// handleAddURL is reachable via the unauthenticated SABnzbd-compatible shim,
// so the remote fetch below (fetchRemoteURL) must defend against SSRF (a
// caller using this server to probe/reach internal-only services) and
// against an unbounded/slow response tying up the request indefinitely.
func (h *sabHandler) handleAddURL(w http.ResponseWriter, r *http.Request) {
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

	if h.recentlyDispatchedURL != nil && h.recentlyDispatchedURL(nzbURL) {
		h.writeError(w, "recently added, skipping duplicate fetch")
		return
	}
	if h.markURLDispatched != nil {
		h.markURLDispatched(nzbURL)
	}

	content, err := h.fetchRemote(r.Context(), nzbURL)
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

	nzoID, err := h.importFn(r.Context(), content, filename, catToMediaType(sabCategory(r)))
	if err != nil {
		h.writeError(w, "import: "+err.Error())
		return
	}
	h.writeJSON(w, map[string]any{"status": true, "nzo_ids": []string{nzoID}})
}

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
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.log.Error().Err(err).Msg("sabnzbd: encode response")
	}
}

func (h *sabHandler) writeError(w http.ResponseWriter, msg string) {
	h.log.Warn().Str("error", msg).Msg("sabnzbd api error")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, `{"status":false,"error":%q}`, msg)
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

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/hjongedijk/drakkar/internal/database"
	"github.com/hjongedijk/drakkar/internal/ranking"
)

// registerProfileRoutes mounts custom-format, release-block-rule, indexer-policy,
// and subtitle-profile endpoints onto r. All routes are no-ops when repo is nil.
func registerProfileRoutes(r chi.Router, repo ProfilesRepository) {
	// ── Custom formats ───────────────────────────────────────────────────────────
	r.Get("/api/custom-formats", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondJSON(w, http.StatusOK, map[string]any{"items": []any{}})
			return
		}
		items, err := repo.ListCustomFormats(rq.Context())
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		if items == nil {
			items = []database.CustomFormat{}
		}
		respondJSON(w, http.StatusOK, map[string]any{"items": items})
	})
	r.Post("/api/custom-formats", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		var f database.CustomFormat
		if err := json.NewDecoder(rq.Body).Decode(&f); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		saved, err := repo.UpsertCustomFormat(rq.Context(), f)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusOK, saved)
	})
	r.Put("/api/custom-formats/{id}", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		var f database.CustomFormat
		if err := json.NewDecoder(rq.Body).Decode(&f); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		f.ID = id
		updated, err := repo.UpdateCustomFormat(rq.Context(), f)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusOK, updated)
	})
	r.Delete("/api/custom-formats/{id}", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		if err := repo.DeleteCustomFormat(rq.Context(), id); err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"deleted": id})
	})
	r.Post("/api/custom-formats/import", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		var formats []database.CustomFormat
		if err := json.NewDecoder(rq.Body).Decode(&formats); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		imported := 0
		for _, f := range formats {
			f.Source = "trash"
			if _, err := repo.UpsertCustomFormatByName(rq.Context(), f); err == nil {
				imported++
			}
		}
		respondJSON(w, http.StatusOK, map[string]any{"imported": imported, "total": len(formats)})
	})

	// ── Release block rules ──────────────────────────────────────────────────────
	r.Get("/api/release-block-rules", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondJSON(w, http.StatusOK, map[string]any{"items": []any{}})
			return
		}
		items, err := repo.ListReleaseBlockRules(rq.Context())
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		if items == nil {
			items = []database.ReleaseBlockRule{}
		}
		respondJSON(w, http.StatusOK, map[string]any{"items": items})
	})
	r.Post("/api/release-block-rules", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		var rule database.ReleaseBlockRule
		if err := json.NewDecoder(rq.Body).Decode(&rule); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		if err := validateReleaseBlockRule(rule); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		saved, err := repo.UpsertReleaseBlockRule(rq.Context(), rule)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusOK, saved)
	})
	r.Put("/api/release-block-rules/{id}", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		var rule database.ReleaseBlockRule
		if err := json.NewDecoder(rq.Body).Decode(&rule); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		rule.ID = id
		if err := validateReleaseBlockRule(rule); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		updated, err := repo.UpdateReleaseBlockRule(rq.Context(), rule)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusOK, updated)
	})
	r.Post("/api/release-block-rules/import", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		var rules []database.ReleaseBlockRule
		if err := json.NewDecoder(rq.Body).Decode(&rules); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		for i, rule := range rules {
			if err := validateReleaseBlockRule(rule); err != nil {
				respondError(w, http.StatusBadRequest, fmt.Errorf("rule[%d]: %w", i, err))
				return
			}
		}
		imported := 0
		for _, rule := range rules {
			if _, err := repo.UpsertReleaseBlockRule(rq.Context(), rule); err == nil {
				imported++
			}
		}
		respondJSON(w, http.StatusOK, map[string]any{"imported": imported, "total": len(rules)})
	})
	r.Delete("/api/release-block-rules/{id}", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		if err := repo.DeleteReleaseBlockRule(rq.Context(), id); err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"deleted": id})
	})
	r.Post("/api/release-block-rules/test", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		var req struct {
			Title     string `json:"title"`
			MediaType string `json:"mediaType"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		dbRules, err := repo.ListReleaseBlockRules(rq.Context())
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		rules := make([]ranking.BlockRule, len(dbRules))
		for i, rr := range dbRules {
			rules[i] = ranking.BlockRule{
				ID: rr.ID, Type: rr.Type, Pattern: rr.Pattern,
				MediaType: rr.MediaType, Action: rr.Action,
				ScorePenalty: rr.ScorePenalty, Enabled: rr.Enabled,
				Source: rr.Source, Note: rr.Note,
			}
		}
		result := ranking.TestBlockRules(rules, req.Title, req.MediaType)
		respondJSON(w, http.StatusOK, result)
	})

	// ── Indexer policies ─────────────────────────────────────────────────────────
	r.Get("/api/indexer-policies", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondJSON(w, http.StatusOK, map[string]any{"items": []any{}})
			return
		}
		items, err := repo.ListIndexerPolicies(rq.Context())
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		if items == nil {
			items = []database.IndexerPolicy{}
		}
		respondJSON(w, http.StatusOK, map[string]any{"items": items})
	})
	r.Post("/api/indexer-policies", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		var p database.IndexerPolicy
		if err := json.NewDecoder(rq.Body).Decode(&p); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(p.IndexerName) == "" {
			respondError(w, http.StatusBadRequest, errors.New("indexerName is required"))
			return
		}
		saved, err := repo.UpsertIndexerPolicy(rq.Context(), p)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusOK, saved)
	})
	r.Put("/api/indexer-policies/{id}", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		var p database.IndexerPolicy
		if err := json.NewDecoder(rq.Body).Decode(&p); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		p.ID = id
		updated, err := repo.UpdateIndexerPolicy(rq.Context(), p)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusOK, updated)
	})
	r.Delete("/api/indexer-policies/{id}", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		if err := repo.DeleteIndexerPolicy(rq.Context(), id); err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"deleted": id})
	})

	// ── Subtitle profiles ─────────────────────────────────────────────────────────
	r.Get("/api/subtitle-profiles", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondJSON(w, http.StatusOK, map[string]any{"items": []any{}})
			return
		}
		items, err := repo.ListSubtitleProfiles(rq.Context())
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		if items == nil {
			items = []database.SubtitleProfile{}
		}
		respondJSON(w, http.StatusOK, map[string]any{"items": items})
	})
	r.Post("/api/subtitle-profiles", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		var p database.SubtitleProfile
		if err := json.NewDecoder(rq.Body).Decode(&p); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(p.Name) == "" {
			respondError(w, http.StatusBadRequest, errors.New("name is required"))
			return
		}
		saved, err := repo.CreateSubtitleProfile(rq.Context(), p)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusOK, saved)
	})
	r.Put("/api/subtitle-profiles/{id}", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		var p database.SubtitleProfile
		if err := json.NewDecoder(rq.Body).Decode(&p); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		p.ID = id
		updated, err := repo.UpdateSubtitleProfile(rq.Context(), p)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusOK, updated)
	})
	r.Delete("/api/subtitle-profiles/{id}", func(w http.ResponseWriter, rq *http.Request) {
		if repo == nil {
			respondError(w, http.StatusNotImplemented, errors.New("profiles unavailable"))
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		if err := repo.DeleteSubtitleProfile(rq.Context(), id); err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"deleted": id})
	})
}


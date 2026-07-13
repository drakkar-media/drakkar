package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/drakkar-media/drakkar/internal/auth"
)

func mountSetupRoutes(r chi.Router, repo UserRepository) {
	r.Get("/api/setup/status", handleSetupStatus(repo))
	r.Post("/api/setup/complete", handleSetupComplete(repo))
}

func handleSetupStatus(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, err := repo.CountUsers(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"required": n == 0,
		})
	}
}

func handleSetupComplete(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only allowed when no users exist yet.
		n, err := repo.CountUsers(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if n > 0 {
			http.Error(w, `{"error":"setup already complete"}`, http.StatusConflict)
			return
		}
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" || body.Password == "" {
			http.Error(w, `{"error":"username and password required"}`, http.StatusBadRequest)
			return
		}
		hash, err := auth.HashPassword(body.Password)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		user, err := repo.CreateUser(r.Context(), body.Username, hash, "admin")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Log the admin in immediately.
		token, tokenHash, err := auth.GenerateToken()
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		expiry := time.Now().Add(auth.SessionExpiry)
		if err := repo.CreateSession(r.Context(), user.ID, tokenHash, expiry); err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		auth.SetSessionCookie(w, token, expiry)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"username": user.Username,
			"role":     user.Role,
		})
	}
}

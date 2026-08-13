package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/drakkar-media/drakkar/internal/auth"
	"github.com/go-chi/chi/v5"
)

func mountSetupRoutes(r chi.Router, repo UserRepository, security auth.RequestSecurityConfig) {
	r.Get("/api/setup/status", handleSetupStatus(repo))
	r.Post("/api/setup/complete", handleSetupComplete(repo, security))
}

// handleSetupStatus reports whether first-run setup is still required (true
// when zero users exist), which the frontend uses to decide whether to show
// the setup wizard instead of the login form.
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

// handleSetupComplete provisions the first admin account and immediately
// logs it in via a session cookie. The repository serializes the final
// zero-user check and insert so concurrent setup requests cannot mint multiple
// admins.
func handleSetupComplete(repo UserRepository, security auth.RequestSecurityConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Reject initialized systems before bcrypt; the atomic create below remains
		// authoritative when concurrent first-run requests pass this fast path.
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
		if err := decodeJSONBody(r, &body); err != nil {
			if isRequestBodyTooLarge(err) {
				respondError(w, http.StatusRequestEntityTooLarge, err)
				return
			}
			http.Error(w, `{"error":"username and password required"}`, http.StatusBadRequest)
			return
		}
		if body.Username == "" || body.Password == "" {
			http.Error(w, `{"error":"username and password required"}`, http.StatusBadRequest)
			return
		}
		if err := auth.ValidatePassword(body.Password); err != nil {
			respondJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		hash, err := auth.HashPassword(body.Password)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		user, created, err := repo.CreateInitialAdmin(r.Context(), body.Username, hash)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !created {
			http.Error(w, `{"error":"setup already complete"}`, http.StatusConflict)
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
		auth.SetSessionCookie(w, token, expiry, security.SecureCookie(r))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"username": user.Username,
			"role":     user.Role,
		})
	}
}

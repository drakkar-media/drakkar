package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/drakkar-media/drakkar/internal/auth"
	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/go-chi/chi/v5"
)

// UserRepository covers user + session operations needed by auth handlers.
type UserRepository interface {
	CountUsers(ctx context.Context) (int, error)
	ListUsers(ctx context.Context) ([]database.User, error)
	CreateUser(ctx context.Context, username, passwordHash, role string) (database.User, error)
	GetUserByUsername(ctx context.Context, username string) (id int64, passwordHash, role string, err error)
	UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) error
	DeleteUser(ctx context.Context, id int64) error
	CreateSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error
	GetSessionByTokenHash(ctx context.Context, tokenHash string) (userID int64, username, role string, expiresAt time.Time, err error)
	DeleteSession(ctx context.Context, tokenHash string) error
	ListAPITokens(ctx context.Context, userID int64) ([]database.APIToken, error)
	CreateAPIToken(ctx context.Context, userID int64, name, tokenHash string, expiresAt *time.Time) (database.APIToken, error)
	GetAPITokenByHash(ctx context.Context, tokenHash string) (userID int64, username, role string, expiresAt *time.Time, err error)
	TouchAPITokenUsed(ctx context.Context, tokenHash string) error
	DeleteAPIToken(ctx context.Context, userID, tokenID int64) error
}

// parseInt64PathID parses the chi "id" URL param as an int64. Unlike
// router.go's parseInt64URLParam (which uses respondError's JSON encoder),
// this file's existing convention writes errors via http.Error with a
// hand-built JSON-shaped body, so this mirrors that exact behavior on
// failure: it writes {"error":"invalid id"} with 400 and returns ok=false.
func parseInt64PathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func mountAuthRoutes(r chi.Router, repo UserRepository) {
	r.Post("/api/auth/login", handleLogin(repo))
	r.Post("/api/auth/logout", handleLogout(repo))
	r.Get("/api/auth/me", handleMe())
	r.Get("/api/auth/tokens", handleListAPITokens(repo))
	r.Post("/api/auth/tokens", handleCreateAPIToken(repo))
	r.Delete("/api/auth/tokens/{id}", handleDeleteAPIToken(repo))
}

func mountUserRoutes(r chi.Router, repo UserRepository) {
	r.Get("/api/users", handleListUsers(repo))
	r.Post("/api/users", handleCreateUser(repo))
	r.Delete("/api/users/{id}", handleDeleteUser(repo))
	r.Put("/api/users/{id}/password", handleChangePassword(repo))
}

// handleLogin verifies a username/password pair, then issues a new session:
// a random token is generated, only its hash is persisted, and the raw token
// is set on the response as the session cookie with a lifetime of
// auth.SessionExpiry. Responds 401 on any lookup failure or password
// mismatch, without distinguishing the two.
func handleLogin(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
			return
		}
		userID, passwordHash, role, err := repo.GetUserByUsername(r.Context(), body.Username)
		if err != nil || !auth.CheckPassword(passwordHash, body.Password) {
			http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
			return
		}
		token, hash, err := auth.GenerateToken()
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		expiry := time.Now().Add(auth.SessionExpiry)
		if err := repo.CreateSession(r.Context(), userID, hash, expiry); err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		auth.SetSessionCookie(w, token, expiry)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"username": body.Username,
			"role":     role,
		})
	}
}

// handleLogout deletes the caller's server-side session (looked up by the
// hashed cookie value) and clears the session cookie. Always succeeds, even
// with no cookie or an already-expired/deleted session.
func handleLogout(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.CookieName)
		if err == nil {
			_ = repo.DeleteSession(r.Context(), auth.HashToken(cookie.Value))
		}
		auth.ClearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleMe returns the caller's identity as resolved from request context by
// the auth middleware; 401 if the request carries no valid session/token.
func handleMe() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.FromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":       claims.UserID,
			"username": claims.Username,
			"role":     claims.Role,
		})
	}
}

// handleListAPITokens lists API tokens belonging to the authenticated
// caller only; it never exposes another user's tokens.
func handleListAPITokens(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.FromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		items, err := repo.ListAPITokens(r.Context(), claims.UserID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if items == nil {
			items = []database.APIToken{}
		}
		respondJSON(w, http.StatusOK, items)
	}
}

// handleCreateAPIToken mints a new API token for the authenticated caller.
// The raw token value is returned in this response only — only its hash is
// ever persisted, so this is the caller's one chance to see it.
func handleCreateAPIToken(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.FromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var body struct {
			Name      string `json:"name"`
			ExpiresAt string `json:"expiresAt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			http.Error(w, `{"error":"name required"}`, http.StatusBadRequest)
			return
		}
		var expiresAt *time.Time
		if body.ExpiresAt != "" {
			ts, err := time.Parse(time.RFC3339, body.ExpiresAt)
			if err != nil {
				http.Error(w, `{"error":"invalid expiresAt"}`, http.StatusBadRequest)
				return
			}
			expiresAt = &ts
		}
		token, tokenHash, err := auth.GenerateToken()
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		item, err := repo.CreateAPIToken(r.Context(), claims.UserID, body.Name, tokenHash, expiresAt)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respondJSON(w, http.StatusCreated, map[string]any{
			"id":         item.ID,
			"userId":     item.UserID,
			"name":       item.Name,
			"createdAt":  item.CreatedAt,
			"lastUsedAt": item.LastUsedAt,
			"expiresAt":  item.ExpiresAt,
			"token":      token,
		})
	}
}

// handleDeleteAPIToken deletes an API token, scoped to the authenticated
// caller's own tokens (claims.UserID) so one user cannot delete another's.
func handleDeleteAPIToken(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.FromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		id, ok := parseInt64PathID(w, r)
		if !ok {
			return
		}
		if err := repo.DeleteAPIToken(r.Context(), claims.UserID, id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleListUsers requires the admin role and returns every user account.
func handleListUsers(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.FromContext(r.Context())
		if !ok || claims.Role != "admin" {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		users, err := repo.ListUsers(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if users == nil {
			users = []database.User{}
		}
		respondJSON(w, http.StatusOK, users)
	}
}

// handleCreateUser requires the admin role and creates a new account,
// defaulting Role to "user" when the request omits it.
func handleCreateUser(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.FromContext(r.Context())
		if !ok || claims.Role != "admin" {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Role     string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" || body.Password == "" {
			http.Error(w, `{"error":"username and password required"}`, http.StatusBadRequest)
			return
		}
		if body.Role == "" {
			body.Role = "user"
		}
		hash, err := auth.HashPassword(body.Password)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		user, err := repo.CreateUser(r.Context(), body.Username, hash, body.Role)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(user)
	}
}

// handleDeleteUser requires the admin role and refuses to delete the
// caller's own account, preventing an admin from locking themselves out.
func handleDeleteUser(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.FromContext(r.Context())
		if !ok || claims.Role != "admin" {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		id, ok := parseInt64PathID(w, r)
		if !ok {
			return
		}
		if claims.UserID == id {
			http.Error(w, `{"error":"cannot delete your own account"}`, http.StatusBadRequest)
			return
		}
		if err := repo.DeleteUser(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleChangePassword lets a user change their own password, or an admin
// change any user's password.
func handleChangePassword(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseInt64PathID(w, r)
		if !ok {
			return
		}
		claims, _ := auth.FromContext(r.Context())
		if claims.UserID != id && claims.Role != "admin" {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == "" {
			http.Error(w, `{"error":"password required"}`, http.StatusBadRequest)
			return
		}
		hash, err := auth.HashPassword(body.Password)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		if err := repo.UpdateUserPassword(r.Context(), id, hash); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

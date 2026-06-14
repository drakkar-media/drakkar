package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hjongedijk/drakkar/internal/auth"
	"github.com/hjongedijk/drakkar/internal/database"
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
}

func mountAuthRoutes(r chi.Router, repo UserRepository) {
	r.Post("/api/auth/login", handleLogin(repo))
	r.Post("/api/auth/logout", handleLogout(repo))
	r.Get("/api/auth/me", handleMe())
}

func mountUserRoutes(r chi.Router, repo UserRepository) {
	r.Get("/api/users", handleListUsers(repo))
	r.Post("/api/users", handleCreateUser(repo))
	r.Delete("/api/users/{id}", handleDeleteUser(repo))
	r.Put("/api/users/{id}/password", handleChangePassword(repo))
}

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

func handleListUsers(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

func handleCreateUser(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
			body.Role = "admin"
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

func handleDeleteUser(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
			return
		}
		claims, _ := auth.FromContext(r.Context())
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

func handleChangePassword(repo UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
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

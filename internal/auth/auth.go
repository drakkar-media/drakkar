package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	// CookieName is the name of the HTTP cookie used to carry the session
	// token.
	CookieName = "drakkar_session"
	// SessionExpiry is the lifetime of a newly created session, measured
	// from creation time.
	SessionExpiry = 30 * 24 * time.Hour
)

// Claims identifies the authenticated principal attached to a request
// context, whether resolved from a session cookie or an API token.
type Claims struct {
	UserID   int64
	Username string
	Role     string
}

type contextKey struct{}

// GenerateToken returns a random hex token and its SHA-256 hash.
// Store the hash in the DB; send the raw token in the cookie.
func GenerateToken() (token, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return
	}
	token = hex.EncodeToString(buf)
	hash = HashToken(token)
	return
}

// HashToken returns the SHA-256 hash of a raw token, hex-encoded. Used both
// to derive the value stored in the DB from a freshly generated token and to
// look up a session/API token from the raw value presented by a client.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// HashPassword hashes a plaintext password with bcrypt for storage.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}

// CheckPassword reports whether password matches the given bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// SetSessionCookie writes the session cookie for token, expiring at expiry.
// The cookie is HttpOnly and SameSite=Lax so it is inaccessible to page
// scripts and is not sent on cross-site requests.
func SetSessionCookie(w http.ResponseWriter, token string, expiry time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie expires the session cookie immediately, logging the
// client out.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// FromContext retrieves the Claims attached by the auth middleware (or by
// NewContext in tests). The second return value is false when ctx carries
// no Claims, e.g. for requests to exempt/unauthenticated routes.
func FromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(contextKey{}).(Claims)
	return c, ok
}

// NewContext attaches Claims to ctx so a downstream handler's FromContext
// call sees them. The auth middleware uses this on every authenticated
// request; tests that exercise a handler directly (bypassing the middleware)
// can call it too, to simulate a logged-in user of a given role.
func NewContext(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, contextKey{}, c)
}

// SessionLookup is the minimal interface the auth middleware needs.
type SessionLookup interface {
	GetSessionByTokenHash(ctx context.Context, tokenHash string) (userID int64, username, role string, expiresAt time.Time, err error)
	GetAPITokenByHash(ctx context.Context, tokenHash string) (userID int64, username, role string, expiresAt *time.Time, err error)
	TouchAPITokenUsed(ctx context.Context, tokenHash string) error
}

// apiTokenFromRequest extracts a bearer-style API token from either the
// X-Api-Key header or a "Bearer <token>" Authorization header, preferring
// X-Api-Key when both are present. Returns "" when neither is set.
func apiTokenFromRequest(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get("X-Api-Key")); token != "" {
		return token
	}
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authz), "bearer ") {
		return strings.TrimSpace(authz[7:])
	}
	return ""
}

// Middleware validates the session cookie on all /api/* routes except the
// given exempt prefixes. Non-API paths (static files, SPA routes) pass through.
func Middleware(repo SessionLookup, exemptPrefixes []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only gate API routes.
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				next.ServeHTTP(w, r)
				return
			}
			// Exempt specific prefixes.
			for _, prefix := range exemptPrefixes {
				if r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix) {
					next.ServeHTTP(w, r)
					return
				}
			}
			if token := apiTokenFromRequest(r); token != "" {
				hash := HashToken(token)
				userID, username, role, expiresAt, err := repo.GetAPITokenByHash(r.Context(), hash)
				if err == nil && (expiresAt == nil || time.Now().Before(*expiresAt)) {
					_ = repo.TouchAPITokenUsed(r.Context(), hash)
					next.ServeHTTP(w, r.WithContext(NewContext(r.Context(), Claims{
						UserID:   userID,
						Username: username,
						Role:     role,
					})))
					return
				}
			}
			cookie, err := r.Cookie(CookieName)
			if err != nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			hash := HashToken(cookie.Value)
			userID, username, role, expiresAt, err := repo.GetSessionByTokenHash(r.Context(), hash)
			if err != nil || time.Now().After(expiresAt) {
				ClearSessionCookie(w)
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(NewContext(r.Context(), Claims{
				UserID:   userID,
				Username: username,
				Role:     role,
			})))
		})
	}
}

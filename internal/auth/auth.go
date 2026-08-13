package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
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
	// MinPasswordBytes is the minimum accepted plaintext password length.
	MinPasswordBytes = 8
	// MaxPasswordBytes is bcrypt's maximum meaningful plaintext length.
	MaxPasswordBytes = 72
)

var (
	// ErrPasswordTooShort indicates that a password does not meet the minimum
	// server-side length policy.
	ErrPasswordTooShort = errors.New("password must be at least 8 bytes")
	// ErrPasswordTooLong indicates that bcrypt would reject or truncate the
	// supplied password.
	ErrPasswordTooLong = errors.New("password must be at most 72 bytes")
)

// RequestSecurityConfig controls security decisions that depend on how an
// HTTP request reached Drakkar.
//
// Proxy headers are ignored unless TrustProxyHeaders is explicitly enabled,
// because direct clients can forge them. ForceSecureCookies supports TLS
// termination configurations that do not forward the original scheme.
type RequestSecurityConfig struct {
	// ForceSecureCookies marks cookies Secure even when Drakkar sees plain HTTP.
	ForceSecureCookies bool
	// TrustProxyHeaders permits proxy-supplied scheme and client-IP headers.
	TrustProxyHeaders bool
}

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

// ValidatePassword enforces the shared server-side password policy used by
// setup, user creation, and password rotation.
//
// Length is measured in bytes because bcrypt accepts at most 72 bytes; no
// character-class rules are imposed so long passphrases remain valid.
func ValidatePassword(password string) error {
	if len(password) < MinPasswordBytes {
		return ErrPasswordTooShort
	}
	if len(password) > MaxPasswordBytes {
		return ErrPasswordTooLong
	}
	return nil
}

// SecureCookie reports whether a response to r must mark session cookies
// Secure. Direct TLS is always recognized; proxy scheme headers are honored
// only when explicitly trusted.
func (c RequestSecurityConfig) SecureCookie(r *http.Request) bool {
	if c.ForceSecureCookies || (r != nil && r.TLS != nil) {
		return true
	}
	if !c.TrustProxyHeaders || r == nil {
		return false
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// ClientIP returns the address used to scope login throttling.
//
// X-Forwarded-For and X-Real-IP are considered only when proxy headers are
// trusted. Invalid forwarded values fall back to the direct peer address.
func (c RequestSecurityConfig) ClientIP(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	if c.TrustProxyHeaders {
		for _, value := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
			if ip := normalizedIP(value); ip != "" {
				return ip
			}
		}
		if ip := normalizedIP(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}
	if ip := normalizedIP(r.RemoteAddr); ip != "" {
		return ip
	}
	return "unknown"
}

func normalizedIP(value string) string {
	value = strings.Trim(strings.TrimSpace(value), `"`)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	} else {
		value = strings.Trim(value, "[]")
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return ""
	}
	return ip.String()
}

// SetSessionCookie writes the session cookie for token, expiring at expiry.
// The cookie is HttpOnly and SameSite=Lax so it is inaccessible to page
// scripts and is not sent on cross-site requests. secure must reflect the
// external request scheme when TLS terminates at a trusted proxy.
func SetSessionCookie(w http.ResponseWriter, token string, expiry time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie expires the session cookie immediately, logging the
// client out.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
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
func Middleware(repo SessionLookup, exemptPrefixes []string, security RequestSecurityConfig) func(http.Handler) http.Handler {
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
				ClearSessionCookie(w, security.SecureCookie(r))
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

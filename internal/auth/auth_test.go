package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type lookupStub struct {
	apiTokenHash      string
	apiTokenExpiresAt *time.Time
	touchedHash       string
}

func (l *lookupStub) GetSessionByTokenHash(ctx context.Context, tokenHash string) (userID int64, username, role string, expiresAt time.Time, err error) {
	return 0, "", "", time.Time{}, context.Canceled
}

func (l *lookupStub) GetAPITokenByHash(ctx context.Context, tokenHash string) (userID int64, username, role string, expiresAt *time.Time, err error) {
	if tokenHash != l.apiTokenHash {
		return 0, "", "", nil, context.Canceled
	}
	return 42, "operator", "admin", l.apiTokenExpiresAt, nil
}

func (l *lookupStub) TouchAPITokenUsed(ctx context.Context, tokenHash string) error {
	l.touchedHash = tokenHash
	return nil
}

func TestMiddlewareAcceptsBearerAPIToken(t *testing.T) {
	raw, hashed, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	repo := &lookupStub{apiTokenHash: hashed}

	var claims Claims
	handler := Middleware(repo, nil, RequestSecurityConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		claims, ok = FromContext(r.Context())
		if !ok {
			t.Fatal("expected claims in context")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	if claims.UserID != 42 || claims.Username != "operator" || claims.Role != "admin" {
		t.Fatalf("unexpected claims %+v", claims)
	}
	if repo.touchedHash != hashed {
		t.Fatalf("expected touched hash %q, got %q", hashed, repo.touchedHash)
	}
}

func TestAuthenticateAPITokenRejectsExpiredToken(t *testing.T) {
	raw, hashed, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Second)
	repo := &lookupStub{apiTokenHash: hashed, apiTokenExpiresAt: &expired}

	if _, ok := AuthenticateAPIToken(context.Background(), repo, raw); ok {
		t.Fatal("expected expired API token to be rejected")
	}
	if repo.touchedHash != "" {
		t.Fatalf("expired token must not update last-used timestamp, got %q", repo.touchedHash)
	}
	if _, ok := AuthenticateAPIToken(context.Background(), nil, raw); ok {
		t.Fatal("expected unavailable token repository to fail closed")
	}
}

func TestValidatePasswordPolicy(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  error
	}{
		{name: "minimum", password: "12345678"},
		{name: "passphrase", password: "correct horse battery staple"},
		{name: "short", password: "1234567", wantErr: ErrPasswordTooShort},
		{name: "multibyte bytes", password: "\u00e5\u00e4\u00f6", wantErr: ErrPasswordTooShort},
		{name: "bcrypt maximum", password: strings.Repeat("x", MaxPasswordBytes)},
		{name: "too long", password: strings.Repeat("x", MaxPasswordBytes+1), wantErr: ErrPasswordTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePassword(tt.password)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidatePassword() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestRequestSecurityConfigSecureCookie(t *testing.T) {
	tests := []struct {
		name   string
		config RequestSecurityConfig
		url    string
		proto  string
		want   bool
	}{
		{name: "plain HTTP", url: "http://drakkar.test/login"},
		{name: "direct TLS", url: "https://drakkar.test/login", want: true},
		{name: "forced", config: RequestSecurityConfig{ForceSecureCookies: true}, url: "http://drakkar.test/login", want: true},
		{name: "trusted proxy", config: RequestSecurityConfig{TrustProxyHeaders: true}, url: "http://drakkar.test/login", proto: "https", want: true},
		{name: "trusted proxy first hop", config: RequestSecurityConfig{TrustProxyHeaders: true}, url: "http://drakkar.test/login", proto: "https, http", want: true},
		{name: "untrusted spoof", url: "http://drakkar.test/login", proto: "https"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			req.Header.Set("X-Forwarded-Proto", tt.proto)
			if got := tt.config.SecureCookie(req); got != tt.want {
				t.Fatalf("SecureCookie() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRequestSecurityConfigClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://drakkar.test/api/auth/login", nil)
	req.RemoteAddr = "192.0.2.10:4321"
	req.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.2")
	req.Header.Set("X-Real-IP", "203.0.113.9")

	if got := (RequestSecurityConfig{}).ClientIP(req); got != "192.0.2.10" {
		t.Fatalf("untrusted ClientIP() = %q", got)
	}
	if got := (RequestSecurityConfig{TrustProxyHeaders: true}).ClientIP(req); got != "198.51.100.7" {
		t.Fatalf("trusted ClientIP() = %q", got)
	}
}

func TestSessionCookieCarriesSecurePolicy(t *testing.T) {
	rec := httptest.NewRecorder()
	SetSessionCookie(rec, "token", time.Now().Add(time.Hour), true)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected session cookie: %+v", cookies)
	}

	rec = httptest.NewRecorder()
	ClearSessionCookie(rec, true)
	cookies = rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || cookies[0].MaxAge != -1 {
		t.Fatalf("unexpected cleared cookie: %+v", cookies)
	}
}

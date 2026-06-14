package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type lookupStub struct {
	apiTokenHash string
	touchedHash  string
}

func (l *lookupStub) GetSessionByTokenHash(ctx context.Context, tokenHash string) (userID int64, username, role string, expiresAt time.Time, err error) {
	return 0, "", "", time.Time{}, context.Canceled
}

func (l *lookupStub) GetAPITokenByHash(ctx context.Context, tokenHash string) (userID int64, username, role string, expiresAt *time.Time, err error) {
	if tokenHash != l.apiTokenHash {
		return 0, "", "", nil, context.Canceled
	}
	return 42, "operator", "admin", nil, nil
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
	handler := Middleware(repo, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

package tvdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hjongedijk/drakkar/internal/config"
)

func TestSeriesDetails(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v4/login":
			_, _ = w.Write([]byte(`{"data":{"token":"abc123"}}`))
		case "/v4/series/412567/extended":
			authHeader = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"data":{"name":"The Bear","year":2022,"remoteIds":[{"sourceName":"IMDb","id":"tt14452776"}]}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(config.MetadataConfig{TVDB: config.APIKeyConfig{APIKey: "key"}})
	client.baseURL = server.URL + "/v4"
	client.httpClient = server.Client()

	item, err := client.SeriesDetails(context.Background(), 412567)
	if err != nil {
		t.Fatal(err)
	}
	if authHeader != "Bearer abc123" {
		t.Fatalf("unexpected auth header %q", authHeader)
	}
	if item.Name != "The Bear" || item.Year != 2022 || item.IMDbID != "tt14452776" {
		t.Fatalf("unexpected series details %+v", item)
	}
}

// TestSeriesDetailsReLoginsOn401 simulates TVDB revoking/rotating the token
// server-side before the client's own 29-day cache expiry: the first series
// call with the cached token gets a 401, and the client must invalidate that
// token and re-login rather than keep sending the same dead token on every
// call until the cache naturally expires.
func TestSeriesDetailsReLoginsOn401(t *testing.T) {
	var loginCount, seriesCallCount int
	var lastAuthHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v4/login":
			loginCount++
			// The test primes client.token = "token-1" directly (simulating an
			// already-cached token) without ever calling login, so the only
			// real login() call in this test is the re-login after the 401.
			_, _ = w.Write([]byte(`{"data":{"token":"token-2"}}`))
		case "/v4/series/412567/extended":
			seriesCallCount++
			lastAuthHeader = r.Header.Get("Authorization")
			if lastAuthHeader == "Bearer token-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":{"name":"The Bear","year":2022}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(config.MetadataConfig{TVDB: config.APIKeyConfig{APIKey: "key"}})
	client.baseURL = server.URL + "/v4"
	client.httpClient = server.Client()

	// Prime the cache with the token that will be rejected.
	client.token = "token-1"
	client.tokenTime = time.Now().UTC()

	item, err := client.SeriesDetails(context.Background(), 412567)
	if err != nil {
		t.Fatalf("expected retry-after-401 to succeed, got: %v", err)
	}
	if item.Name != "The Bear" {
		t.Fatalf("unexpected series details %+v", item)
	}
	if seriesCallCount != 2 {
		t.Fatalf("expected 2 series calls (401 then retry), got %d", seriesCallCount)
	}
	if loginCount != 1 {
		t.Fatalf("expected exactly 1 fresh login after the 401, got %d", loginCount)
	}
	if lastAuthHeader != "Bearer token-2" {
		t.Fatalf("expected retry to use the freshly logged-in token, got %q", lastAuthHeader)
	}
}

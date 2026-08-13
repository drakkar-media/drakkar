package seerr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
)

func TestCreateTVSeasonRequestRecoversFromCloudflareTimeout(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/request":
			mu.Lock()
			posts++
			current := posts
			mu.Unlock()
			if current == 1 {
				w.WriteHeader(524)
				_, _ = w.Write([]byte("<html><title>524</title><body>Cloudflare timeout</body></html>"))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/request":
			mu.Lock()
			current := posts
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if current >= 2 {
				_, _ = w.Write([]byte(`{"pageInfo":{"results":1,"pageSize":5000,"page":1},"results":[{"id":1,"type":"tv","status":2,"media":{"tmdbId":84958,"tvdbId":362472,"title":"Loki","firstAirDate":"2021-06-09"},"episodes":[{"seasonNumber":2,"episodeNumber":1,"name":"Episode 1"}]}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"pageInfo":{"results":0,"pageSize":5000,"page":1},"results":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL})
	client.httpClient.Timeout = 2 * time.Second

	if err := client.CreateTVSeasonRequest(context.Background(), 84958, []int{2}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if posts != 2 {
		t.Fatalf("expected one retry after transient failure, got %d posts", posts)
	}
}

func TestCreateRequestTreatsExistingConflictAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/request":
			http.Error(w, "already requested", http.StatusConflict)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/request":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"pageInfo":{"results":1,"pageSize":5000,"page":1},"results":[{"id":1,"type":"movie","status":2,"media":{"tmdbId":11,"title":"Star Wars","releaseDate":"1977-05-25"},"episodes":[]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL})
	if err := client.CreateRequest(context.Background(), "movie", 11); err != nil {
		t.Fatal(err)
	}
}

func TestPendingRequestsClassifiesCloudflareHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>Cloudflare 502 Bad Gateway</body></html>"))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL})
	_, err := client.PendingRequests(context.Background())
	if err == nil {
		t.Fatal("expected cloudflare error")
	}
	if got := strings.ToLower(err.Error()); !strings.Contains(got, "cloudflare") || !strings.Contains(got, "502") {
		t.Fatalf("unexpected error %q", got)
	}
}

func TestRemoveMediaDeletesLinkedAndDiscoveredRequests(t *testing.T) {
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret" {
			http.Error(w, "missing API key", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/request":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"pageInfo":{"results":3,"pageSize":5000,"page":1},"results":[{"id":18,"type":"tv","media":{"tmdbId":900}},{"id":19,"type":"tv","media":{"tmdbId":901}},{"id":20,"type":"movie","media":{"tmdbId":900}}]}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/request/"):
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/watchlist/900":
			if r.URL.Query().Get("mediaType") != "tv" {
				http.Error(w, "wrong media type", http.StatusBadRequest)
				return
			}
			deleted = append(deleted, r.URL.RequestURI())
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "secret"})
	result, err := client.RemoveMedia(context.Background(), "episode", 900, []string{"17-s1e1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestsDeleted != 2 || !result.WatchlistRemoved {
		t.Fatalf("unexpected cleanup result: %+v", result)
	}
	want := []string{"/api/v1/request/17", "/api/v1/request/18", "/api/v1/watchlist/900?mediaType=tv"}
	if strings.Join(deleted, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected delete calls: %v", deleted)
	}
}

func TestRemoveMediaTreatsMissingUpstreamDataAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"pageInfo":{"results":0},"results":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "secret"})
	result, err := client.RemoveMedia(context.Background(), "movie", 12, []string{"77"})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestsDeleted != 0 || result.WatchlistRemoved {
		t.Fatalf("unexpected cleanup result: %+v", result)
	}
}

package opensubtitles

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hjongedijk/drakkar/internal/config"
	"github.com/hjongedijk/drakkar/internal/database"
)

func TestSearch(t *testing.T) {
	var authHeader string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":    "jwt-token",
				"base_url": strings.TrimPrefix(server.URL, "http://"),
			})
		case "/api/v1/subtitles":
			authHeader = r.Header.Get("Authorization")
			if got := r.Header.Get("Api-Key"); got != "api-key" {
				t.Fatalf("unexpected api key %q", got)
			}
			if got := r.URL.Query().Get("languages"); got != "en,nl" {
				t.Fatalf("unexpected languages %q", got)
			}
			if got := r.URL.Query().Get("tmdb_id"); got != "438631" {
				t.Fatalf("unexpected tmdb id %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"id": "1",
					"attributes": map[string]any{
						"language":         "en",
						"release":          "Dune.2021.1080p.WEB-DL",
						"hearing_impaired": false,
						"feature_details":  map[string]any{"title": "Dune"},
						"files":            []map[string]any{{"file_id": 777.0, "file_name": "Dune.2021.en.srt"}},
					},
				}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(config.SubtitleAuth{APIKey: "api-key", Username: "user", Password: "pass"})
	client.baseURL = server.URL + "/api/v1"
	client.httpClient = server.Client()

	items, err := client.Search(context.Background(), database.SubtitleSearchInput{
		LibraryItemID: 42,
		MediaType:     "movie",
		Title:         "Dune",
		MovieYear:     2021,
		TMDBID:        438631,
	}, []string{"en", "nl"})
	if err != nil {
		t.Fatal(err)
	}
	if authHeader != "Bearer jwt-token" {
		t.Fatalf("unexpected auth header %q", authHeader)
	}
	if len(items) != 1 || items[0].ExternalID != "777" || items[0].Format != "srt" {
		t.Fatalf("unexpected items %+v", items)
	}
}

func TestDownload(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":    "jwt-token",
				"base_url": strings.TrimPrefix(server.URL, "http://"),
			})
		case "/api/v1/download":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer jwt-token" {
				t.Fatalf("unexpected auth header %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"link":      server.URL + "/download/file123.srt",
				"file_name": "file123.srt",
			})
		case "/download/file123.srt":
			_, _ = w.Write([]byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(config.SubtitleAuth{APIKey: "api-key", Username: "user", Password: "pass"})
	client.baseURL = server.URL + "/api/v1"
	client.httpClient = server.Client()

	name, body, err := client.Download(context.Background(), "777")
	if err != nil {
		t.Fatal(err)
	}
	if name != "file123.srt" || !strings.Contains(string(body), "Hello") {
		t.Fatalf("unexpected download %q %q", name, string(body))
	}
}

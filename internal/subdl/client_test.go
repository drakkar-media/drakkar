package subdl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drakkar-media/drakkar/internal/database"
)

func TestSearchUsesUnpackFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/subtitles" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("tmdb_id"); got != "438631" {
			t.Fatalf("unexpected tmdb_id %q", got)
		}
		if got := r.URL.Query().Get("languages"); got != "EN,NL" {
			t.Fatalf("unexpected languages %q", got)
		}
		_, _ = w.Write([]byte(`{
			"status": true,
			"subtitles": [{
				"name": "Season.Pack.zip",
				"release_name": "Season Pack",
				"unpack_files": [{
					"file_n_id": "file123",
					"name": "Dune.2021.en.srt",
					"release_name": "Dune.2021.1080p.WEB-DL",
					"season": 0,
					"episode": 0,
					"language": "EN",
					"hi": false,
					"format": "srt",
					"url": "/subtitle/parent/file123"
				}]
			}]
		}`))
	}))
	defer server.Close()

	client := &Client{
		apiKey:      "key",
		baseURL:     server.URL + "/api/v1",
		downloadURL: server.URL,
		httpClient:  server.Client(),
	}
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
	if len(items) != 1 {
		t.Fatalf("unexpected item count %d", len(items))
	}
	if items[0].ExternalID != "file123" || !strings.HasSuffix(items[0].DownloadURL, "/subtitle/parent/file123") {
		t.Fatalf("unexpected candidate %+v", items[0])
	}
}

func TestSearchIncludesZipOnlyCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"status": true,
			"subtitles": [{
				"name": "The.Bear.S02E03.zip",
				"release_name": "The.Bear.S02E03.1080p.WEB",
				"season": 2,
				"episode": 3,
				"language": "EN",
				"hi": false,
				"format": "zip",
				"url": "/subtitle/zippack"
			}]
		}`))
	}))
	defer server.Close()

	client := &Client{
		apiKey:      "key",
		baseURL:     server.URL + "/api/v1",
		downloadURL: server.URL,
		httpClient:  server.Client(),
	}
	items, err := client.Search(context.Background(), database.SubtitleSearchInput{
		LibraryItemID: 42,
		MediaType:     "episode",
		ShowTitle:     "The Bear",
		ShowYear:      2022,
		SeasonNumber:  2,
		EpisodeNumber: 3,
		TVDBID:        412567,
	}, []string{"en"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Format != "zip" || !strings.HasSuffix(items[0].DownloadURL, "/subtitle/zippack") {
		t.Fatalf("unexpected items %+v", items)
	}
}

func TestDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/subtitle/parent/file123.srt" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"))
	}))
	defer server.Close()

	client := &Client{httpClient: server.Client()}
	name, body, err := client.Download(context.Background(), server.URL+"/subtitle/parent/file123.srt")
	if err != nil {
		t.Fatal(err)
	}
	if name != "file123.srt" {
		t.Fatalf("unexpected name %s", name)
	}
	if !strings.Contains(string(body), "Hello") {
		t.Fatalf("unexpected body %q", string(body))
	}
}

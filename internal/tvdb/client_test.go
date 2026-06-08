package tvdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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

package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hjongedijk/drakkar/internal/config"
)

func TestMovieDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/movie/438631" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("api_key"); got != "key" {
			t.Fatalf("unexpected api key %q", got)
		}
		if got := r.URL.Query().Get("append_to_response"); got != "alternative_titles,credits,recommendations,similar" {
			t.Fatalf("unexpected append value %q", got)
		}
		_, _ = w.Write([]byte(`{"title":"Dune","release_date":"2021-10-22","imdb_id":"tt1160419"}`))
	}))
	defer server.Close()

	client := NewClient(config.MetadataConfig{TMDB: config.APIKeyConfig{APIKey: "key"}})
	client.baseURL = server.URL + "/3"
	client.httpClient = server.Client()

	item, err := client.MovieDetails(context.Background(), 438631)
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Dune" || item.Year != 2021 || item.IMDbID != "tt1160419" {
		t.Fatalf("unexpected movie details %+v", item)
	}
}

func TestTVDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/84958" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("append_to_response"); got != "external_ids,alternative_titles,aggregate_credits,recommendations,similar" {
			t.Fatalf("unexpected append value %q", got)
		}
		_, _ = w.Write([]byte(`{"name":"Loki","first_air_date":"2021-06-09","external_ids":{"imdb_id":"tt9140554"},"alternative_titles":{"results":[]}}`))
	}))
	defer server.Close()

	client := NewClient(config.MetadataConfig{TMDB: config.APIKeyConfig{APIKey: "key"}})
	client.baseURL = server.URL + "/3"
	client.httpClient = server.Client()

	item, err := client.TVDetails(context.Background(), 84958)
	if err != nil {
		t.Fatal(err)
	}
	if item.Name != "Loki" || item.Year != 2021 || item.IMDbID != "tt9140554" {
		t.Fatalf("unexpected tv details %+v", item)
	}
}

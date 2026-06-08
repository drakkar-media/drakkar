package seerr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hjongedijk/drakkar/internal/config"
)

func TestPendingRequests(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/request" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		requests++
		w.Header().Set("Content-Type", "application/json")
		if got := r.URL.Query().Get("take"); got != "5000" {
			t.Fatalf("unexpected take %q", got)
		}
		switch r.URL.Query().Get("skip") {
		case "0":
			// status=3 (declined) is the only status Drakkar skips; status=5 is now included.
			_, _ = w.Write([]byte(`{"pageInfo":{"results":5001,"pageSize":5000,"page":1},"results":[{"id":12,"type":"movie","status":2,"media":{"tmdbId":438631,"title":"Dune","releaseDate":"2021-10-22"}},{"id":13,"type":"movie","status":3,"media":{"tmdbId":1,"title":"Declined","releaseDate":"2021-10-22"}}]}`))
		case "5000":
			_, _ = w.Write([]byte(`{"pageInfo":{"results":5001,"pageSize":5000,"page":2},"results":[{"id":14,"type":"tv","status":2,"media":{"tvdbId":362472,"tmdbId":84958,"name":"Loki","firstAirDate":"2021-06-09"},"episodes":[{"seasonNumber":2,"episodeNumber":1,"name":"Ouroboros"}]}]}`))
		default:
			t.Fatalf("unexpected skip %q", r.URL.Query().Get("skip"))
		}
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc"})
	got, err := client.PendingRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(got))
	}
	if got[0].MediaTitle != "Dune" || got[0].MediaYear != 2021 {
		t.Fatalf("unexpected first request %+v", got[0])
	}
	if got[1].SeasonNumber != 2 || got[1].EpisodeNumber != 1 {
		t.Fatalf("unexpected second request %+v", got[1])
	}
	if requests != 2 {
		t.Fatalf("expected 2 page requests, got %d", requests)
	}
}

func TestProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("X-Api-Key"); got != "abc" {
			t.Fatalf("expected api key header, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"version":"3.3.0"}`))
	}))
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL, APIKey: "abc"})
	if err := client.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
}

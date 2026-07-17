package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// TestHandleAddURLSkipsRecentlyDispatchedURL guards against a real gap found
// in the 2026-07-17 exhaustive audit: the SABnzbd-compatible addurl endpoint
// (used by Radarr/Sonarr as a download client) called fetchRemoteURL
// unconditionally, with no equivalent to workflow.Service's per-URL fetch
// cooldown. A Radarr/Sonarr retry of the identical addurl request (its own
// download-client retry logic, or a resubmission after Drakkar restarts
// mid-request) would trigger a second live NZB fetch from the indexer for
// the same URL -- the same duplicate-download signal that triggered the
// NZB Finder account-termination warning this session's other fixes address.
func TestHandleAddURLSkipsRecentlyDispatchedURL(t *testing.T) {
	fetchCalls := 0
	h := &sabHandler{
		importFn: func(_ context.Context, _ []byte, _, _ string) (string, error) {
			return "nzo-1", nil
		},
		log: zerolog.Nop(),
		fetchFn: func(_ context.Context, _ string) ([]byte, error) {
			fetchCalls++
			return []byte("<nzb></nzb>"), nil
		},
		recentlyDispatchedURL: func(rawURL string) bool {
			return rawURL == "http://indexer.example/get/duplicate.nzb"
		},
		markURLDispatched: func(_ string) {},
	}

	req := httptest.NewRequest(http.MethodGet, "/sabnzbd/api?"+url.Values{
		"mode": {"addurl"},
		"name": {"http://indexer.example/get/duplicate.nzb"},
	}.Encode(), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if fetchCalls != 0 {
		t.Fatalf("expected fetchRemote not to be called for a recently-dispatched URL, got %d calls", fetchCalls)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected a non-200 error response for a duplicate addurl, got status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "duplicate") {
		t.Fatalf("expected an error message mentioning the duplicate skip, got %s", rec.Body.String())
	}
}

// TestHandleAddURLMarksURLDispatchedBeforeFetching confirms the happy path
// still fetches and marks the URL as dispatched (so a near-simultaneous
// retry is caught even before this first fetch completes).
func TestHandleAddURLMarksURLDispatchedBeforeFetching(t *testing.T) {
	var markedURL string
	fetchCalls := 0
	h := &sabHandler{
		importFn: func(_ context.Context, _ []byte, _, _ string) (string, error) {
			return "nzo-1", nil
		},
		log: zerolog.Nop(),
		fetchFn: func(_ context.Context, _ string) ([]byte, error) {
			fetchCalls++
			if markedURL == "" {
				t.Fatal("expected the URL to be marked dispatched before fetchRemote is called")
			}
			return []byte("<nzb></nzb>"), nil
		},
		recentlyDispatchedURL: func(_ string) bool { return false },
		markURLDispatched: func(rawURL string) {
			markedURL = rawURL
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/sabnzbd/api?"+url.Values{
		"mode": {"addurl"},
		"name": {"http://indexer.example/get/fresh.nzb"},
	}.Encode(), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if fetchCalls != 1 {
		t.Fatalf("expected exactly 1 fetch for a fresh URL, got %d", fetchCalls)
	}
	if markedURL != "http://indexer.example/get/fresh.nzb" {
		t.Fatalf("expected the fetched URL to be marked dispatched, got %q", markedURL)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected a successful response, got status %d body %s", rec.Code, rec.Body.String())
	}
}

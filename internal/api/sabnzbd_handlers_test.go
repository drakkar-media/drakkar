package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/nzb"
	"github.com/rs/zerolog"
)

func sabTestAuthenticator(validToken string) func(context.Context, string) bool {
	return func(_ context.Context, rawToken string) bool {
		return rawToken == validToken
	}
}

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
		enabled:           true,
		authenticateToken: sabTestAuthenticator("sab-test-key"),
		importFn: func(_ context.Context, _ io.Reader, _, _ string) (string, error) {
			return "nzo-1", nil
		},
		log: zerolog.Nop(),
		fetchFn: func(_ context.Context, _ string) ([]byte, error) {
			fetchCalls++
			return []byte("<nzb></nzb>"), nil
		},
		claimURLForFetch: func(_ context.Context, rawURL string) bool {
			return rawURL == "http://indexer.example/get/duplicate.nzb"
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/sabnzbd/api?"+url.Values{
		"mode":   {"addurl"},
		"name":   {"http://indexer.example/get/duplicate.nzb"},
		"apikey": {"sab-test-key"},
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

// TestHandleAddURLClaimsURLBeforeFetching confirms the happy path calls
// claimURLForFetch (which atomically claims the URL) before fetchRemote, so
// a near-simultaneous retry is caught even before this first fetch completes.
func TestHandleAddURLClaimsURLBeforeFetching(t *testing.T) {
	var claimedURL string
	fetchCalls := 0
	h := &sabHandler{
		enabled:           true,
		authenticateToken: sabTestAuthenticator("sab-test-key"),
		importFn: func(_ context.Context, _ io.Reader, _, _ string) (string, error) {
			return "nzo-1", nil
		},
		log: zerolog.Nop(),
		fetchFn: func(_ context.Context, _ string) ([]byte, error) {
			fetchCalls++
			if claimedURL == "" {
				t.Fatal("expected the URL to be claimed before fetchRemote is called")
			}
			return []byte("<nzb></nzb>"), nil
		},
		claimURLForFetch: func(_ context.Context, rawURL string) bool {
			claimedURL = rawURL
			return false // not already claimed -- caller may proceed
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/sabnzbd/api?"+url.Values{
		"mode":   {"addurl"},
		"name":   {"http://indexer.example/get/fresh.nzb"},
		"apikey": {"sab-test-key"},
	}.Encode(), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if fetchCalls != 1 {
		t.Fatalf("expected exactly 1 fetch for a fresh URL, got %d", fetchCalls)
	}
	if claimedURL != "http://indexer.example/get/fresh.nzb" {
		t.Fatalf("expected the fetched URL to be claimed, got %q", claimedURL)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected a successful response, got status %d body %s", rec.Code, rec.Body.String())
	}
}

type sabAuthEffects struct {
	listCalls    int
	dismissCalls int
	imports      int
	fetches      int
}

type sabAuthRepository struct {
	effects *sabAuthEffects
}

func (r *sabAuthRepository) ListSabQueueItems(context.Context, string, int, int) ([]database.SabQueueItem, int, error) {
	r.effects.listCalls++
	return nil, 0, nil
}

func (r *sabAuthRepository) ListSabHistoryItems(context.Context, string, int, int) ([]database.SabHistoryItem, int, error) {
	r.effects.listCalls++
	return nil, 0, nil
}

func (r *sabAuthRepository) DismissSabItems(context.Context, []int64) error {
	r.effects.dismissCalls++
	return nil
}

func sabAuthRequest(t *testing.T, operation, apiKey string) *http.Request {
	t.Helper()
	values := url.Values{"mode": {operation}}
	if apiKey != "" {
		values.Set("apikey", apiKey)
	}
	switch operation {
	case "queue":
		return httptest.NewRequest(http.MethodGet, "/sabnzbd/api?"+values.Encode(), nil)
	case "delete":
		values.Set("mode", "queue")
		values.Set("name", "delete")
		values.Set("value", "item-42")
		return httptest.NewRequest(http.MethodGet, "/sabnzbd/api?"+values.Encode(), nil)
	case "addurl":
		values.Set("name", "https://indexer.example/release.nzb")
		return httptest.NewRequest(http.MethodGet, "/sabnzbd/api?"+values.Encode(), nil)
	case "addfile":
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if err := writer.WriteField("mode", "addfile"); err != nil {
			t.Fatal(err)
		}
		part, err := writer.CreateFormFile("nzbfile", "release.nzb")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("<nzb></nzb>")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/dav/api?"+values.Encode(), &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		return req
	default:
		t.Fatalf("unknown operation %q", operation)
		return nil
	}
}

func TestSABAPIAuthenticationProtectsEveryOperation(t *testing.T) {
	const validKey = "sab-test-key"
	operations := []struct {
		name string
		want sabAuthEffects
	}{
		{name: "queue", want: sabAuthEffects{listCalls: 1}},
		{name: "delete", want: sabAuthEffects{dismissCalls: 1}},
		{name: "addfile", want: sabAuthEffects{imports: 1}},
		{name: "addurl", want: sabAuthEffects{imports: 1, fetches: 1}},
	}
	credentials := []struct {
		name       string
		apiKey     string
		wantStatus int
	}{
		{name: "anonymous", wantStatus: http.StatusUnauthorized},
		{name: "wrong-key", apiKey: "wrong", wantStatus: http.StatusUnauthorized},
		{name: "correct-key", apiKey: validKey, wantStatus: http.StatusOK},
	}

	for _, operation := range operations {
		for _, credential := range credentials {
			t.Run(operation.name+"/"+credential.name, func(t *testing.T) {
				effects := &sabAuthEffects{}
				h := &sabHandler{
					enabled:           true,
					authenticateToken: sabTestAuthenticator(validKey),
					repo:              &sabAuthRepository{effects: effects},
					importFn: func(context.Context, io.Reader, string, string) (string, error) {
						effects.imports++
						return "item-42", nil
					},
					fetchFn: func(context.Context, string) ([]byte, error) {
						effects.fetches++
						return []byte("<nzb></nzb>"), nil
					},
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, sabAuthRequest(t, operation.name, credential.apiKey))

				if rec.Code != credential.wantStatus {
					t.Fatalf("expected status %d, got %d: %s", credential.wantStatus, rec.Code, rec.Body.String())
				}
				var response map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatalf("expected valid JSON response, got %q: %v", rec.Body.String(), err)
				}
				want := sabAuthEffects{}
				if credential.apiKey == validKey {
					want = operation.want
				}
				if *effects != want {
					t.Fatalf("unexpected protected operation effects: got %+v want %+v", *effects, want)
				}
			})
		}
	}
}

func TestSABAPIDisabledByDefault(t *testing.T) {
	h := &sabHandler{}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sabnzbd/api?mode=version", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("expected valid JSON response, got %q: %v", rec.Body.String(), err)
	}
	if response["error"] != "SAB API is disabled" {
		t.Fatalf("unexpected disabled response: %+v", response)
	}
}

func TestSABAPIFailsClosedWithoutTokenValidator(t *testing.T) {
	h := &sabHandler{enabled: true}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sabnzbd/api?mode=version", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSABAPIErrorMessagesAreJSONEncoded(t *testing.T) {
	h := &sabHandler{enabled: true, authenticateToken: sabTestAuthenticator("sab-test-key")}
	req := httptest.NewRequest(http.MethodGet, "/sabnzbd/api?"+url.Values{
		"mode":   {`invalid"mode`},
		"apikey": {"sab-test-key"},
	}.Encode(), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("expected valid JSON response, got %q: %v", rec.Body.String(), err)
	}
	if response["error"] != `invalid mode: invalid"mode` {
		t.Fatalf("unexpected error response: %+v", response)
	}
}

func TestSABAPIUploadLimitReturns413(t *testing.T) {
	h := &sabHandler{
		enabled:           true,
		authenticateToken: sabTestAuthenticator("sab-test-key"),
		importFn: func(context.Context, io.Reader, string, string) (string, error) {
			return "", nzb.ErrUploadTooLarge
		},
	}
	req := sabAuthRequest(t, "addfile", "sab-test-key")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":false`) {
		t.Fatalf("unexpected SAB response %s", rec.Body.String())
	}
}

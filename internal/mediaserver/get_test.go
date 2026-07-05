package mediaserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetSetsAuthHeaderAndDecodesJSON(t *testing.T) {
	var gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Test-Token")
		_, _ = w.Write([]byte(`{"name":"ok"}`))
	}))
	defer server.Close()

	var out struct {
		Name string `json:"name"`
	}
	err := Get(context.Background(), server.Client(), server.URL, "/path", "X-Test-Token", "secret", "testsvc", &out)
	if err != nil {
		t.Fatal(err)
	}
	if gotHeader != "secret" {
		t.Fatalf("expected auth header to be set, got %q", gotHeader)
	}
	if out.Name != "ok" {
		t.Fatalf("expected decoded JSON, got %+v", out)
	}
}

func TestGetReturnsErrorOnHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	err := Get(context.Background(), server.Client(), server.URL, "/path", "X-Test-Token", "secret", "testsvc", nil)
	if err == nil || !strings.Contains(err.Error(), "testsvc HTTP 403") {
		t.Fatalf("expected testsvc HTTP 403 error, got %v", err)
	}
}

func TestGetSkipsDecodeWhenOutNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not valid json"))
	}))
	defer server.Close()

	if err := Get(context.Background(), server.Client(), server.URL, "/path", "X-Test-Token", "secret", "testsvc", nil); err != nil {
		t.Fatalf("expected no error when out is nil (body not decoded), got %v", err)
	}
}

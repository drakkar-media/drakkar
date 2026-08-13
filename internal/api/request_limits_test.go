package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type requestLimitStatusStub struct {
	limit int64
}

func (s requestLimitStatusStub) Status() Status {
	return Status{NZBUploadLimitBytes: s.limit}
}

func TestRequestBodyLimitUsesRouteSpecificCaps(t *testing.T) {
	const nzbLimit = int64(1234)
	tests := []struct {
		name        string
		path        string
		contentType string
		want        int64
	}{
		{name: "default json", path: "/api/auth/login", want: defaultRequestBodyLimitBytes},
		{name: "settings", path: "/api/settings", want: bulkRequestBodyLimitBytes},
		{name: "bulk import", path: "/api/custom-formats/import", want: bulkRequestBodyLimitBytes},
		{name: "raw nzb", path: "/api/nzbs/import", want: nzbLimit},
		{name: "multipart nzb", path: "/api/nzbs/import", contentType: "multipart/form-data; boundary=test", want: nzbLimit + multipartOverheadBytes},
		{name: "manual nzb", path: "/api/library/42/manual-import/upload", contentType: "multipart/form-data; boundary=test", want: nzbLimit + multipartOverheadBytes},
		{name: "sab alias", path: "/dav/api", contentType: "multipart/form-data; boundary=test", want: nzbLimit + multipartOverheadBytes},
		{name: "sab form", path: "/dav/api", contentType: "application/x-www-form-urlencoded", want: defaultRequestBodyLimitBytes},
		{name: "raw subtitle", path: "/api/subtitles/42/upload", want: subtitleUploadLimitBytes},
		{name: "multipart subtitle", path: "/api/subtitles/42/upload", contentType: "multipart/form-data; boundary=test", want: subtitleUploadLimitBytes + multipartOverheadBytes},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, test.path, http.NoBody)
			req.Header.Set("Content-Type", test.contentType)
			if got := requestBodyLimit(req, nzbLimit); got != test.want {
				t.Fatalf("request limit = %d, want %d", got, test.want)
			}
		})
	}
}

func TestRequestBodyLimitRejectsDeclaredOversizeBeforeHandler(t *testing.T) {
	called := false
	handler := requestBodyLimitMiddleware(requestLimitStatusStub{limit: 4})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/nzbs/import", strings.NewReader("12345"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("oversized request reached handler")
	}
}

func TestRequestBodyLimitRejectsChunkedJSONAndTrailingData(t *testing.T) {
	handler := requestBodyLimitMiddleware(requestLimitStatusStub{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := decodeJSONBody(r, &body); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		respondJSON(w, http.StatusOK, body)
	}))
	payload := `{}` + strings.Repeat(" ", int(defaultRequestBodyLimitBytes))
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/seerr", strings.NewReader(payload))
	req.ContentLength = -1
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected chunked overflow to return 413, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRequestBodyLimitUsesSABErrorShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeRequestBodyTooLarge(rec, "/sabnzbd/api")

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"status":false`) || !strings.Contains(body, `"error":"request body too large"`) {
		t.Fatalf("unexpected SAB error body %s", body)
	}
}

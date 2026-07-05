package httperr

import (
	"net/http"
	"strings"
	"testing"
)

func TestClassifyStatusCloudflare(t *testing.T) {
	err := ClassifyStatus("seerr", "search", 524, []byte("cloudflare 524 timeout"))
	if err == nil || !strings.Contains(err.Error(), "cloudflare timeout") {
		t.Fatalf("expected cloudflare timeout error, got %v", err)
	}
}

func TestClassifyStatusPlain(t *testing.T) {
	err := ClassifyStatus("nzbhydra2", "search", http.StatusInternalServerError, nil)
	if err == nil || !strings.Contains(err.Error(), "nzbhydra2 search status 500") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDetectResponseErrorFindsCloudflareHTML(t *testing.T) {
	err := DetectResponseError("seerr", "request list", 200, []byte("<html>cloudflare bad gateway</html>"))
	if err == nil {
		t.Fatal("expected an error for cloudflare bad-gateway HTML body")
	}
}

func TestDetectResponseErrorReturnsNilForNormalBody(t *testing.T) {
	if err := DetectResponseError("seerr", "request list", 200, []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("expected nil for a normal JSON body, got %v", err)
	}
}

func TestSummarizeBodyTruncatesAndCollapsesWhitespace(t *testing.T) {
	got := SummarizeBody([]byte("line one\r\nline   two\n" + strings.Repeat("x", 200)))
	if len(got) != 160 {
		t.Fatalf("expected 160-char snippet, got %d chars", len(got))
	}
	if strings.Contains(got, "\n") || strings.Contains(got, "\r") {
		t.Fatalf("expected newlines stripped, got %q", got)
	}
}

func TestSummarizeBodyEmpty(t *testing.T) {
	if got := SummarizeBody(nil); got != "" {
		t.Fatalf("expected empty string for nil body, got %q", got)
	}
}

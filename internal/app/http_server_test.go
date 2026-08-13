package app

import (
	"net/http"
	"testing"
)

func TestBoundedHTTPServerConfiguresReadAndIdleDeadlines(t *testing.T) {
	server := newBoundedHTTPServer(":0", http.NotFoundHandler())

	if server.ReadHeaderTimeout <= 0 || server.ReadTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatalf("expected positive read/idle deadlines, got header=%s read=%s idle=%s", server.ReadHeaderTimeout, server.ReadTimeout, server.IdleTimeout)
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("streaming-compatible WriteTimeout must remain unset, got %s", server.WriteTimeout)
	}
}

package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRejectSSRFIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"169.254.1.1", true},
		{"10.0.0.5", true},
		{"172.16.0.5", true},
		{"192.168.1.5", true},
		{"0.0.0.0", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
	}
	for _, c := range cases {
		err := rejectSSRFIP(net.ParseIP(c.ip))
		if c.blocked && err == nil {
			t.Errorf("expected %s to be blocked, got nil error", c.ip)
		}
		if !c.blocked && err != nil {
			t.Errorf("expected %s to be allowed, got error: %v", c.ip, err)
		}
	}
}

func TestFetchRemoteURLRejectsLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("should never be reached"))
	}))
	defer srv.Close()

	_, err := fetchRemoteURL(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected fetchRemoteURL to reject a loopback target, got nil error")
	}
	if !strings.Contains(err.Error(), "non-public address") {
		t.Fatalf("expected SSRF rejection error, got: %v", err)
	}
}

func TestFetchRemoteURLRejectsBadScheme(t *testing.T) {
	_, err := fetchRemoteURL(context.Background(), "file:///etc/passwd")
	if err == nil {
		t.Fatal("expected fetchRemoteURL to reject a non-http(s) scheme")
	}
}
